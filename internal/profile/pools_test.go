// SPDX-License-Identifier: AGPL-3.0-or-later

package profile

import (
	"os"
	"slices"
	"testing"

	"github.com/goccy/go-yaml"
)

// poolsOfSoftnet is the Softnet version whose subnet finder softnetPools copies. A new Softnet can
// pick the networks of the VMs from other ranges, so an upgrade of the hosts fails this test until
// a person reads lib/subnet_finder/mod.rs of the new version and changes both copies.
const poolsOfSoftnet = "0.24.1"

const (
	hostDefaults = "../../deploy/hosts/roles/orchard_worker/defaults/main.yml"
	hostVars     = "../../deploy/hosts/roles/orchard_worker/vars/main.yml"
)

// readYAML reads one YAML file of the role into a map.
func readYAML(t *testing.T, path string) map[string]any {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // G304: a file of the role that the test names
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var values map[string]any
	if err := yaml.Unmarshal(data, &values); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	return values
}

// The controller refuses a wide softnetAllow inside the networks of Softnet, and the pf rules of the
// hosts guard the gateways in the rest. Both read the same ranges of the same Softnet.
func TestTheSoftnetPoolsAgreeWithTheHosts(t *testing.T) {
	if got := readYAML(t, hostDefaults)["orchard_worker_softnet_version"]; got != poolsOfSoftnet {
		t.Errorf("the hosts install Softnet %v, and softnetPools copies the ranges of Softnet %s: "+
			"read lib/subnet_finder/mod.rs of the new version, then change softnetPools, "+
			"orchard_worker_softnet_pools, and poolsOfSoftnet", got, poolsOfSoftnet)
	}

	listed, ok := readYAML(t, hostVars)["orchard_worker_softnet_pools"].([]any)
	if !ok || len(listed) == 0 {
		t.Fatalf("%s has no list orchard_worker_softnet_pools", hostVars)
	}

	var hosts, controller []string
	for _, pool := range listed {
		text, _ := pool.(string)
		hosts = append(hosts, text)
	}

	for _, pool := range softnetPools {
		controller = append(controller, pool.String())
	}

	if !slices.Equal(hosts, controller) {
		t.Errorf("the pf rules of the hosts take the Softnet ranges %v, and the controller %v",
			hosts, controller)
	}
}
