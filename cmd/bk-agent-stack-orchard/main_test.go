// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors

package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	grpchealth "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func TestVersionPrintsTheVersionAndTheCommit(t *testing.T) {
	version, commit = "1.2.3", "abc1234"

	var out, problems bytes.Buffer
	if status := run([]string{"-version"}, &out, &problems); status != 0 {
		t.Fatalf("run() = %d, want 0", status)
	}

	got := strings.TrimSpace(out.String())
	want := "bk-agent-stack-orchard 1.2.3 (abc1234)"

	if got != want {
		t.Errorf("run() wrote %q, want %q", got, want)
	}
}

func TestVersionReportsUnknownWhenTheBuildDidNotSetOne(t *testing.T) {
	version, commit = "", ""

	var out, problems bytes.Buffer
	if status := run([]string{"-version"}, &out, &problems); status != 0 {
		t.Fatalf("run() = %d, want 0", status)
	}

	if got := out.String(); !strings.Contains(got, "unknown") {
		t.Errorf("run() wrote %q, want it to contain %q", got, "unknown")
	}
}

// The platform sets the path of the configuration in the environment, so a process that gets no
// flag must read the file that the variable names.
func TestTheConfigPathComesFromTheEnvironmentWhenTheFlagIsMissing(t *testing.T) {
	environment := map[string]string{"BK_AGENT_STACK_ORCHARD_CONFIG": "/from/the/environment.yaml"}

	if got, _ := configPath("", func(key string) string { return environment[key] }); got !=
		"/from/the/environment.yaml" {
		t.Errorf("configPath() = %q, want the path of the environment", got)
	}
}

func TestTheDefaultConfigPath(t *testing.T) {
	got, _ := configPath("", func(string) string { return "" })

	if want := "/etc/bk-agent-stack-orchard/config.yaml"; got != want {
		t.Errorf("configPath() = %q, want %q", got, want)
	}
}

func TestTheFlagWinsOverTheEnvironment(t *testing.T) {
	environment := map[string]string{"BK_AGENT_STACK_ORCHARD_CONFIG": "/from/the/environment.yaml"}

	if got, _ := configPath("/from/the/flag.yaml", func(key string) string {
		return environment[key]
	}); got != "/from/the/flag.yaml" {
		t.Errorf("configPath() = %q, want the path of the flag", got)
	}
}

// A process with no flag reads the path of the environment, and a file that is not there must
// stop it with a message that names the configuration.
func TestRunReadsTheConfigurationThatTheEnvironmentNames(t *testing.T) {
	var out, problems bytes.Buffer

	missing := filepath.Join(t.TempDir(), "nothing.yaml")
	t.Setenv("BK_AGENT_STACK_ORCHARD_CONFIG", missing)

	if status := run(nil, &out, &problems); status != 1 {
		t.Fatalf("run() = %d, want 1", status)
	}

	if !strings.Contains(problems.String(), missing) {
		t.Errorf("run() reported %q, want it to name %q", problems.String(), missing)
	}
}

func TestRunRefusesAFlagThatDoesNotExist(t *testing.T) {
	var out, problems bytes.Buffer

	if status := run([]string{"-wat"}, &out, &problems); status != 2 {
		t.Errorf("run() = %d, want 2", status)
	}
}

// A configuration that the process cannot read is a failure of the deployment, and it must stop
// the process rather than start a controller with the defaults.
func TestRunStopsWhenTheConfigurationIsNotThere(t *testing.T) {
	var out, problems bytes.Buffer

	missing := filepath.Join(t.TempDir(), "nothing.yaml")
	if status := run([]string{"-config", missing}, &out, &problems); status != 1 {
		t.Fatalf("run() = %d, want 1", status)
	}

	if !strings.Contains(problems.String(), "configuration") {
		t.Errorf("run() reported %q, want it to name the configuration", problems.String())
	}
}

// failingWriter reports an error on every write, as a closed pipe does.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("the pipe is closed")
}

func TestRunReportsAWriteThatFails(t *testing.T) {
	var problems bytes.Buffer

	if status := run([]string{"-version"}, failingWriter{}, &problems); status != 1 {
		t.Errorf("run() = %d, want 1", status)
	}
}

// An operator who reads a failed start must learn where the path came from, because the file that
// the pod reads is not the file that the operator expects when the source is a surprise.
func TestTheStartErrorNamesWhereTheConfigPathCameFrom(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nothing.yaml")

	tests := []struct {
		name  string
		args  []string
		env   string
		other string
	}{
		{"flag", []string{"-config", missing}, "", "the -config flag"},
		{"environment", nil, missing, "the environment variable BK_AGENT_STACK_ORCHARD_CONFIG"},
		{"default", nil, "", "the default path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, problems bytes.Buffer

			t.Setenv("BK_AGENT_STACK_ORCHARD_CONFIG", tt.env)

			if status := run(tt.args, &out, &problems); status != 1 {
				t.Fatalf("run() = %d, want 1", status)
			}

			if !strings.Contains(problems.String(), tt.other) {
				t.Errorf("run() reported %q, want it to name %q", problems.String(), tt.other)
			}
		})
	}
}

// A server that accepts a call and never answers must not hold the loop for ever.
func TestTheClientsOfTheControllerCarryATimeout(t *testing.T) {
	for name, client := range map[string]*http.Client{
		"buildkite": newHTTPClient(nil),
		"orchard":   newHTTPClient(http.DefaultTransport),
	} {
		if client.Timeout != httpTimeout || httpTimeout <= 0 {
			t.Errorf("the %s client has timeout %s, want %s", name, client.Timeout, httpTimeout)
		}
	}

	if httpTimeout != 30*time.Second {
		t.Errorf("httpTimeout = %s, want 30s", httpTimeout)
	}
}

// An unreachable collector makes each exporter retry for about a minute. The stop of telemetry
// must end inside the termination grace of the pod, and its failure goes where run reports.
func TestTheStopOfTelemetryHasADeadline(t *testing.T) {
	var problems bytes.Buffer

	var deadline time.Duration

	stopTelemetry(t.Context(), func(ctx context.Context) error {
		at, ok := ctx.Deadline()
		if !ok {
			t.Fatal("the stop of telemetry has no deadline")
		}

		deadline = time.Until(at)

		return errors.New("the collector does not answer")
	}, &problems)

	if deadline <= 0 || deadline > telemetryGrace || telemetryGrace > 10*time.Second {
		t.Errorf("the stop of telemetry has %s left of %s, want at most 10s", deadline,
			telemetryGrace)
	}

	if !strings.Contains(problems.String(), "the collector does not answer") {
		t.Errorf("the problems read %q, want the failure of the stop", problems.String())
	}
}

// A signal cancels the context of the process before telemetry stops. The stop still gets its
// whole grace, because the last records are the interesting ones.
func TestTheStopOfTelemetryOutlivesTheSignal(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	stopTelemetry(ctx, func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			t.Errorf("the stop of telemetry got a context that ended: %v", err)
		}

		return nil
	}, &bytes.Buffer{})
}

// stoppedListener is a listener that fails at once, as a listener whose socket the system closed.
type stoppedListener struct{ net.Listener }

func (stoppedListener) Accept() (net.Conn, error) { return nil, errors.New("the socket is gone") }

// A probe server that stops on its own leaves a controller that looks dead to the platform, so
// the log must say why.
func TestAProbeServerThatFailsIsLogged(t *testing.T) {
	var log bytes.Buffer

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	serveProbes(t.Context(), newProbeServer(), stoppedListener{listener},
		slog.New(slog.NewTextHandler(&log, nil)))

	if !strings.Contains(log.String(), "the socket is gone") {
		t.Errorf("the log reads %q, want the failure of the probe server", log.String())
	}
}

// The stop of the process stops the probe server, which is no failure.
func TestAProbeServerThatIsStoppedLogsNothing(t *testing.T) {
	var log bytes.Buffer

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() = %v", err)
	}

	server := newProbeServer()
	server.Stop()

	serveProbes(t.Context(), server, listener, slog.New(slog.NewTextHandler(&log, nil)))

	if log.Len() != 0 {
		t.Errorf("the log reads %q, want nothing for a server that was stopped", log.String())
	}
}

// Each Watch of the health service is a stream without an end. The probe server limits the
// streams of one connection, so that one client cannot fill the memory of the pod with them.
func TestTheProbeServerLimitsTheStreamsOfAConnection(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() = %v", err)
	}

	server := newProbeServer()
	healthpb.RegisterHealthServer(server, grpchealth.NewServer())

	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient(listener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("NewClient() = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	client := healthpb.NewHealthClient(conn)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	for i := range probeStreams {
		watch, err := client.Watch(ctx, &healthpb.HealthCheckRequest{})
		if err != nil {
			t.Fatalf("Watch() %d = %v, want a stream", i, err)
		}

		if _, err := watch.Recv(); err != nil {
			t.Fatalf("Recv() of the stream %d = %v, want a status", i, err)
		}
	}

	short, stop := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer stop()

	if _, err := client.Check(short, &healthpb.HealthCheckRequest{}); err == nil {
		t.Errorf("a call beyond %d open streams = nil, want it to wait for a free stream",
			probeStreams)
	}
}

// The longest tick of the target design, section 4, is 23 calls of 10 s: 6 calls of the refresh,
// 2 for the progress notices and the extensions, 3 + 1 for the actions of the reaper and its
// notices, 1 + 3 + 3 + 1 for the reservation, the token calls and the creates inside their
// budgets, and "VM requested", and 3 for the skip notices. Liveness waits more than three times
// that tick, so only a tick that never ends fails it.
func TestTheLivenessStallOutlastsTheLongestTick(t *testing.T) {
	t.Parallel()

	const longestTick = 23 * 10 * time.Second

	if livenessStall <= 3*longestTick {
		t.Errorf("liveness waits %s, want more than three times the longest tick of %s",
			livenessStall, longestTick)
	}
}

// A ko build sets no ldflags, and Go stamps the module version from the commit. The telemetry
// carries that version, so a backend can tell two builds apart.
func TestTheTelemetryVersionFallsBackToTheVersionThatGoStamped(t *testing.T) {
	stamped := func(v string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Version: v}}, true
		}
	}

	for name, test := range map[string]struct {
		released string
		read     func() (*debug.BuildInfo, bool)
		want     string
	}{
		"the release version wins": {"1.2.3", stamped("v0.0.0-x"), "1.2.3"},
		"a ko build gives the stamped version": {
			"", stamped("v0.0.0-20261104101010-abcdef123456"), "v0.0.0-20261104101010-abcdef123456",
		},
		"a build without a stamp gives none": {"", stamped("(devel)"), ""},
		"no build information gives none": {
			"", func() (*debug.BuildInfo, bool) { return nil, false }, "",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := serviceVersion(test.released, test.read); got != test.want {
				t.Errorf("serviceVersion() = %q, want %q", got, test.want)
			}
		})
	}
}
