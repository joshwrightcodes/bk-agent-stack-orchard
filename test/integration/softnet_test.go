//go:build integration && tart

package integration_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/vmspec"
)

// probeScript tries each destination from inside the machine, and writes one line for each:
// "probe <name> ok" or "probe <name> blocked". The gateway of the machine is the host, seen from
// the network of the machines.
const probeScript = `probe() {
  name=$1; shift
  if "$@" >/dev/null 2>&1; then echo "probe $name ok"; else echo "probe $name blocked"; fi
}
gateway=$(route -n get default | awk '/gateway:/ {print $2}')
echo "probe gateway-address $gateway"
probe public-address nc -z -G 5 1.1.1.1 443
probe public-name nc -z -G 5 example.com 443
probe host-gateway nc -z -G 5 "$gateway" "$BKASO_PROBE_PORT"
probe host-lan nc -z -G 5 "$BKASO_PROBE_LAN" "$BKASO_PROBE_PORT"
probe lan-device nc -z -G 5 "$BKASO_PROBE_DEVICE" "$BKASO_PROBE_DEVICE_PORT"
if [ -n "$BKASO_PROBE_OTHER" ]; then probe other-vm nc -z -G 5 "$BKASO_PROBE_OTHER" 22; fi
echo "probe done"
`

// TestSoftnetKeepsABuildAwayFromTheHostAndThePrivateNetwork (a preview of T-101): the machine
// that the controller specifies reaches the Internet, and not the host, another machine, or a
// private address. softnetAllow opens exactly the addresses that it names.
//
// Softnet lets a machine reach the gateway of its network on every port, and the gateway is the
// host, so the pf rules of the hosts close it (deploy/hosts, templates/vm-isolation.pf.j2). This
// test checks the host that runs it, so that Mac needs the same rules, with the router of the LAN
// as the private address that the profile of the control permits:
//
//	deploy/hosts/test/pf.sh load "$(route -n get default | awk '/gateway:/ {print $2}')/32"
func TestSoftnetKeepsABuildAwayFromTheHostAndThePrivateNetwork(t *testing.T) {
	image := os.Getenv("BKASO_TART_IMAGE")
	if image == "" {
		t.Skip("BKASO_TART_IMAGE names no agent image; build one with `mise run agent-image`")
	}

	requireTart(t)
	startRealOrchard(t, orchardBinary(t))

	port := listenOnEveryAddress(t)
	lan := lanAddress(t)
	device, devicePort := lanDevice(t)
	other := otherMachine(t)

	t.Logf("the host listens on port %d; its LAN address is %s; the LAN device is %s:%s; "+
		"the other machine is %q", port, lan, device, devicePort, other)

	env := map[string]string{
		"BKASO_PROBE_PORT":        strconv.Itoa(port),
		"BKASO_PROBE_LAN":         lan,
		"BKASO_PROBE_DEVICE":      device,
		"BKASO_PROBE_DEVICE_PORT": devicePort,
		"BKASO_PROBE_OTHER":       other,
	}

	closed := probeFrom(t, image, "018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a70", nil, env)
	t.Logf("the default profile: %v", closed)

	// The machine keeps DNS, which the host serves, and reaches no other service of the host.
	want := map[string]string{
		"public-address": "ok",
		"public-name":    "ok",
		"host-gateway":   "blocked",
		"host-lan":       "blocked",
		"lan-device":     "blocked",
	}
	// The other machine runs on the shared network of Tart, whose bridge isolation also blocks
	// it, so this proves that the machine does not reach it, and not which layer blocks it.
	if other != "" {
		want["other-vm"] = "blocked"
	}

	for name, result := range want {
		if closed[name] != result {
			t.Errorf("with the default profile, %s is %q, want %q", name, closed[name], result)
		}
	}

	if closed["host-gateway"] == "ok" {
		t.Error("the machine reaches a service of the host through the gateway: load the pf " +
			"rules of the hosts on this Mac with deploy/hosts/test/pf.sh, as the comment of this " +
			"test shows")
	}

	// The control: the LAN device answers when the profile permits it, in Softnet and in the pf
	// rules, so its block above was real, not a device that does not answer. The host stays
	// blocked: pf blocks every address of the host, whatever a profile permits.
	allow := []string{device + "/32", lan + "/32"}

	opened := probeFrom(t, image, "018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a71", allow, env)
	t.Logf("softnetAllow %v: %v", allow, opened)

	for name, result := range map[string]string{
		"lan-device":   "ok",
		"host-lan":     "blocked",
		"host-gateway": "blocked",
	} {
		if opened[name] != result {
			t.Errorf("with softnetAllow %v, %s is %q, want %q", allow, name, opened[name], result)
		}
	}
}

// probeFrom runs the probe script in a machine of the controller specification, and gives the
// result of each destination.
func probeFrom(t *testing.T, image, id string, allow []string, env map[string]string,
) map[string]string {
	t.Helper()

	//nolint:gosec // G101: the job token of a probe, which no Buildkite knows.
	spec, err := vmspec.Build(vmspec.Request{
		ControllerID: "softnet",
		Queue:        "softnet",
		JobID:        job.ID(id),
		JobToken:     "softnet-probe",
		Profile: profile.Profile{
			Name:             "softnet",
			Image:            image,
			CPU:              4,
			MemoryMiB:        8192,
			DiskSizeGB:       50,
			SoftnetAllow:     allow,
			StartupTransport: profile.TransportGuestAgent,
		},
	})
	if err != nil {
		t.Fatalf("building the specification: %v", err)
	}

	if !spec.NetSoftnet {
		t.Fatal("the specification of the controller does not turn Softnet on")
	}

	spec.StartupScript.ScriptContent = probeScript
	for key, value := range env {
		spec.StartupScript.Env[key] = value
	}

	client := orchardClient(t)
	if err := client.CreateVM(t.Context(), spec); err != nil {
		t.Fatalf("creating the machine: %v", err)
	}

	// A failed test still deletes the machine from Orchard. The context of the test has ended by
	// then, so this call has its own.
	t.Cleanup(func() { _ = client.DeleteVM(context.Background(), spec.Name) })

	results := map[string]string{}

	within(t, tartMachineWait, "the probe of "+spec.Name+" ends", func() bool {
		events, err := client.Events(t.Context(), spec.Name)
		if err != nil {
			return false
		}

		for _, event := range events {
			fields := strings.Fields(event.Payload)
			if len(fields) == 3 && fields[0] == "probe" {
				results[fields[1]] = fields[2]
			}

			if strings.TrimSpace(event.Payload) == "probe done" {
				return true
			}
		}

		return false
	})

	if err := client.DeleteVM(t.Context(), spec.Name); err != nil {
		t.Errorf("deleting the machine: %v", err)
	}

	// The context of the test, and with it orchard dev, ends before a cleanup runs, so the wait
	// for the worker is here.
	within(t, 2*time.Minute, "the worker deletes "+spec.Name+" from Tart", func() bool {
		return !tartHolds(t, "orchard-"+spec.Name)
	})

	return results
}

// listenOnEveryAddress accepts and closes connections on a port of every address of the host,
// which is what a service of the host that a build must not reach does.
func listenOnEveryAddress(t *testing.T) int {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}

			_ = connection.Close()
		}
	}()

	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("the listener has the address %v", listener.Addr())
	}

	return address.Port
}

// lanAddress gives the private address that the host uses to reach the Internet. No packet
// leaves: a UDP socket only chooses its source address.
func lanAddress(t *testing.T) string {
	t.Helper()

	connection, err := (&net.Dialer{}).DialContext(t.Context(), "udp4", "1.1.1.1:53")
	if err != nil {
		t.Fatalf("finding the LAN address: %v", err)
	}

	defer func() { _ = connection.Close() }()

	address, ok := connection.LocalAddr().(*net.UDPAddr)
	if !ok || !address.IP.IsPrivate() {
		t.Skipf("the host reaches the Internet from %v, which is not a private address",
			connection.LocalAddr())
	}

	return address.IP.String()
}

// lanDevice gives the router of the host and a port on which it answers: a device of the LAN that
// is not the host.
func lanDevice(t *testing.T) (string, string) {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), "route", "-n", "get", "default").Output()
	if err != nil {
		t.Fatalf("reading the default route: %v", err)
	}

	var router string

	for line := range strings.Lines(string(out)) {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "gateway: "); ok {
			router = value
		}
	}

	if ip := net.ParseIP(router); ip == nil || !ip.IsPrivate() {
		t.Skipf("the router %q is not a private address of a LAN", router)
	}

	for _, port := range []string{"53", "80", "443", "22"} {
		//nolint:gosec // G204: the address comes from the routing table.
		if exec.CommandContext(t.Context(), "nc", "-z", "-G", "3", router, port).Run() == nil {
			return router, port
		}
	}

	t.Skipf("the router %s answers on no port that the probe tries", router)

	return "", ""
}

// otherMachine gives the address of another running Tart machine with SSH open, or nothing.
func otherMachine(t *testing.T) string {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), "tart", "list", "--source", "local",
		"--format", "json").Output()
	if err != nil {
		t.Fatalf("listing the machines of Tart: %v", err)
	}

	var local []struct {
		Name  string `json:"Name"`
		State string `json:"State"`
	}

	if err := json.Unmarshal(out, &local); err != nil {
		t.Fatalf("reading the machines of Tart: %v", err)
	}

	for _, vm := range local {
		if vm.State != "running" || strings.HasPrefix(vm.Name, "orchard-") {
			continue
		}

		//nolint:gosec // G204: the name comes from tart list.
		ip, err := exec.CommandContext(t.Context(), "tart", "ip", vm.Name).Output()
		if err != nil {
			continue
		}

		address := strings.TrimSpace(string(ip))

		// macOS lets only its own programs reach the network of the machines without the Local
		// Network permission, so nc checks the port, not this test.
		//nolint:gosec // G204: the address comes from tart ip.
		if exec.CommandContext(t.Context(), "nc", "-z", "-G", "3", address, "22").Run() == nil {
			return address
		}
	}

	return ""
}
