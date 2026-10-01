//go:build e2e

package lab

import (
	"fmt"
	"strings"
	"testing"
)

// A network dropped from the config is removed completely: FRR VRF, BGP
// instance and the kernel devices. The extra network is applied once by hand;
// the sidecar, running with the real config and sharing the state file, has
// to clean it up.
func TestRemovesProvisionedNetwork(t *testing.T) {
	const node, vni = "gw-b1", "4099"
	if _, err := lab.Exec(node, "sh", "-c",
		`cat /etc/open-dci/config.yaml > /tmp/extra.yaml && echo '  - {vrf: vrf`+vni+`, vni: `+vni+`, routeTarget: "65535:1099", prefixes: [10.99.99.0/24 le 32]}' >> /tmp/extra.yaml`); err != nil {
		t.Fatal(err)
	}
	if _, err := lab.Exec(node, "/usr/local/bin/open-dci", "apply", "-c", "/tmp/extra.yaml"); err != nil {
		t.Fatal(err)
	}
	for _, dev := range []string{"vrf" + vni, "dcibr" + vni, "dcivx" + vni} {
		if _, err := lab.Exec(node, "ip", "link", "show", dev); err != nil {
			t.Fatalf("%s was not provisioned: %v", dev, err)
		}
	}
	if out, _ := lab.Vtysh(node, "show running-config"); !containsAll(out, "vrf vrf"+vni+"\n vni "+vni, "router bgp 4200000026 vrf vrf"+vni) {
		t.Fatalf("extra network was not applied:\n%s", out)
	}
	waitFor(t, heal, func() error {
		out, err := lab.Vtysh(node, "show running-config")
		if err != nil {
			return err
		}
		if strings.Contains(out, "vrf"+vni) {
			return fmt.Errorf("vrf%s still in the running config:\n%s", vni, out)
		}
		for _, dev := range []string{"vrf" + vni, "dcibr" + vni, "dcivx" + vni} {
			if _, err := lab.Exec(node, "ip", "link", "show", dev); err == nil {
				return fmt.Errorf("%s still exists", dev)
			}
		}
		return nil
	})
	// the real networks are unaffected
	waitFor(t, heal, func() error { return opendci(node, "status") })
	waitFor(t, heal, func() error { return lab.Ping("m-b2", mA2.v4, 0) })
}

// open-dci never takes over a VRF it did not create: a network whose VRF
// already exists (here created by hand) is refused before anything changes.
func TestRefusesForeignVRF(t *testing.T) {
	const node, vni = "gw-b1", "4098"
	if _, err := lab.Exec(node, "ip", "link", "add", "vrf"+vni, "type", "vrf", "table", vni); err != nil {
		t.Fatal(err)
	}
	defer lab.Exec(node, "ip", "link", "del", "vrf"+vni)
	if _, err := lab.Exec(node, "sh", "-c",
		`cat /etc/open-dci/config.yaml > /tmp/foreign.yaml && echo '  - {vrf: vrf`+vni+`, vni: `+vni+`, routeTarget: "65535:1098", prefixes: [10.99.98.0/24 le 32]}' >> /tmp/foreign.yaml`); err != nil {
		t.Fatal(err)
	}
	out, err := lab.Exec(node, "sh", "-c", "/usr/local/bin/open-dci apply -c /tmp/foreign.yaml --state /tmp/foreign.state 2>&1")
	if err == nil || !strings.Contains(out, "not created by open-dci") {
		t.Fatalf("want refusal, got err=%v:\n%s", err, out)
	}
	if out, _ := lab.Exec(node, "ip", "link", "show", "vrf"+vni); strings.Contains(out, "alias") {
		t.Fatalf("the foreign vrf was modified: %s", out)
	}
	waitFor(t, heal, func() error { return opendci(node, "status") })
}
