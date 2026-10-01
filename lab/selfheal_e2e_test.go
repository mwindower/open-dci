//go:build e2e

package lab

import (
	"fmt"
	"testing"
	"time"
)

// open-dci adds to the gateway's base config. When the base system rewrites its
// config, the gateway must come back on its own within a few reconcile
// intervals (10 s in the lab).
const heal = 60 * time.Second

func opendci(node string, args ...string) error {
	_, err := lab.Exec(node, append([]string{"/usr/local/bin/open-dci"}, append(args, "-c", "/etc/open-dci/config.yaml")...)...)
	return err
}

func TestGatewaysHealthy(t *testing.T) {
	for _, gw := range append(append(append([]string{}, pairA...), pairB...), pairC...) {
		t.Run(gw, func(t *testing.T) {
			// "status" exits non-zero unless drift-free, kernel in place, peers
			// Established and a SID allocated for every network
			waitFor(t, heal, func() error { return opendci(gw, "status") })
		})
	}
}

// A configuration management renders the base frr.conf and applies it with
// frr-reload.py (as metal-networker does), which removes every line that is
// not in the file, i.e. all of open-dci's, including the tenant VRFs' FRR part.
func TestSelfHealAfterFRRReload(t *testing.T) {
	// frr-reload.py may exit non-zero because of its own second pass ("Refusing
	// to remove a non-existent route"); what matters is that it strips our lines
	if _, err := lab.Exec("gw-a1", "python3", "/usr/lib/frr/frr-reload.py", "--reload", "/etc/frr/frr.conf"); err != nil {
		t.Logf("frr-reload (ignored): %v", err)
	}
	if out, _ := lab.Vtysh("gw-a1", "show running-config"); containsAll(out, "locator DCI") {
		t.Log("note: open-dci re-applied before the check ran")
	}
	waitFor(t, heal, func() error { return opendci("gw-a1", "status") })
	waitFor(t, heal, func() error { return lab.Ping("m-a", mB.v4, 0) })
	waitFor(t, heal, func() error { return lab.Ping("m-b", mA.v6, 0) })
}

// Something (e.g. a networkd restart) resets the DCI devices to the base config's
// 9000; open-dci must raise them again, or full-size packets are black-holed.
func TestSelfHealMTU(t *testing.T) {
	for _, dev := range []string{"bridge", "vni104100", "vlan104100"} {
		if _, err := lab.Exec("gw-a1", "ip", "link", "set", dev, "mtu", "9000"); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, heal, func() error { return opendci("gw-a1", "status") })
	waitFor(t, heal, func() error { return lab.Ping("m-a", mB.v6, 9000-48) })
}

// Lines open-dci applied earlier but no longer wants (here: a peer dropped
// from the config) must be removed, without touching the base config. The
// extra peer is applied once by hand; the sidecar, running with the real
// config and sharing the state file, has to clean it up.
func TestRemovesStaleConfig(t *testing.T) {
	const extra = "fd00:dc1:ff::d1"
	if _, err := lab.Exec("gw-a1", "sh", "-c",
		`sed 's/^peers:.*$/peers:\n  - {address: "`+extra+`", asn: 4200000032}/' /etc/open-dci/config.yaml > /tmp/extra.yaml`); err != nil {
		t.Fatal(err)
	}
	if _, err := lab.Exec("gw-a1", "/usr/local/bin/open-dci", "apply", "-c", "/tmp/extra.yaml"); err != nil {
		t.Fatal(err)
	}
	if out, _ := lab.Vtysh("gw-a1", "show running-config"); !containsAll(out, "neighbor "+extra+" remote-as") {
		t.Fatalf("extra peer was not applied:\n%s", out)
	}
	waitFor(t, heal, func() error {
		out, err := lab.Vtysh("gw-a1", "show running-config")
		if err != nil {
			return err
		}
		if containsAll(out, extra) {
			return fmt.Errorf("stale peer %s still configured", extra)
		}
		return nil
	})
	waitFor(t, heal, func() error { return opendci("gw-a1", "status") })
	waitFor(t, heal, func() error { return lab.Ping("m-a", mB.v4, 0) })
}
