//go:build e2e

package lab

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The safety net: only prefixes in a network's allowlist leave or enter its
// VRF, and a peer only delivers routes with configured route targets, up to
// maxPrefixes. Each test breaks one side on purpose and checks that the other
// side holds.

// vtyshConf runs configuration commands on node (inside "configure").
func vtyshConf(node string, cmds ...string) error {
	args := []string{"vtysh", "-c", "configure"}
	for _, c := range cmds {
		args = append(args, "-c", c)
	}
	_, err := lab.Exec(node, args...)
	return err
}

// hasRoute reports whether node's FRR knows prefix in the given BGP table,
// e.g. "vrf vrf3981 ipv4 unicast" or "ipv4 vpn".
func hasRoute(node, table, prefix string) bool {
	out, err := lab.Vtysh(node, "show bgp "+table+" "+prefix)
	return err == nil && !strings.Contains(out, "Network not in table") && strings.Contains(out, "Paths:")
}

func absent(node, table, prefix string) func() error {
	return func() error {
		if hasRoute(node, table, prefix) {
			return fmt.Errorf("%s: %s is in %q", node, prefix, table)
		}
		return nil
	}
}

func present(node, table, prefix string) func() error {
	return func() error {
		if !hasRoute(node, table, prefix) {
			return fmt.Errorf("%s: %s is not in %q", node, prefix, table)
		}
		return nil
	}
}

// A tenant machine announces a prefix outside the allowlist and a default
// route. gw-a learns both in the tenant VRF, but exports neither.
func TestExportFilter(t *testing.T) {
	const stray = "10.99.2.1"
	if _, err := lab.Exec("m-a", "ip", "addr", "add", stray+"/32", "dev", "lo"); err != nil {
		t.Fatal(err)
	}
	defer lab.Exec("m-a", "ip", "addr", "del", stray+"/32", "dev", "lo")
	if err := vtyshConf("m-a", "ip route 0.0.0.0/0 blackhole", "router bgp 4200000015", "address-family ipv4 unicast", "network 0.0.0.0/0"); err != nil {
		t.Fatal(err)
	}
	defer vtyshConf("m-a", "router bgp 4200000015", "address-family ipv4 unicast", "no network 0.0.0.0/0", "exit", "exit", "no ip route 0.0.0.0/0 blackhole")

	for _, p := range []string{stray + "/32", "0.0.0.0/0"} {
		waitFor(t, converge, present("gw-a", "vrf vrf3981 ipv4 unicast", p)) // announced and learned
	}
	for _, p := range []string{stray + "/32", "0.0.0.0/0"} {
		for _, c := range []struct{ node, table string }{
			{"gw-a", "ipv4 vpn"}, // not exported
			{"gw-b", "ipv4 vpn"},
			{"gw-b", "vrf vrf4011 ipv4 unicast"},
			{"m-b", "ipv4 unicast"},
		} {
			if hasRoute(c.node, c.table, p) {
				t.Errorf("%s leaked to %s %q", p, c.node, c.table)
			}
		}
	}
	// the allowed prefixes still flow
	waitFor(t, converge, func() error { return lab.Ping("m-a", mB.v4, 0) })
}

// gw-b exports a prefix that gw-a's allowlist doesn't cover (its own export
// list is widened by hand). gw-a receives it as VPN route, but doesn't import
// it into the tenant VRF.
func TestImportFilter(t *testing.T) {
	const stray = "10.99.1.1"
	const extra = "ip prefix-list DCI-vrf4011-v4 seq 1000 permit 10.99.0.0/16 le 32"
	// a line open-dci never applied: it neither removes nor needs it
	if err := vtyshConf("gw-b", extra); err != nil {
		t.Fatal(err)
	}
	defer vtyshConf("gw-b", "no "+extra)
	if _, err := lab.Exec("m-b", "ip", "addr", "add", stray+"/32", "dev", "lo"); err != nil {
		t.Fatal(err)
	}
	defer lab.Exec("m-b", "ip", "addr", "del", stray+"/32", "dev", "lo")

	waitFor(t, converge, present("gw-a", "ipv4 vpn", stray+"/32")) // gw-b exported it
	for _, c := range []struct{ node, table string }{
		{"gw-a", "vrf vrf3981 ipv4 unicast"},
		{"leaf-a", "vrf vrf3981 ipv4 unicast"},
		{"m-a", "ipv4 unicast"},
	} {
		if hasRoute(c.node, c.table, stray+"/32") {
			t.Errorf("%s was imported: %s %q", stray, c.node, c.table)
		}
	}
}

// gw-b sends tenant 1's routes with only a route target gw-a doesn't know
// (set by hand on its VRF). gw-a drops them at the session already, before
// any VRF import. gw-b's sidecar is paused meanwhile, since it would revert
// the change within one interval.
func TestPeerRouteTargetFilter(t *testing.T) {
	if _, err := lab.Exec("gw-b", "pkill", "-STOP", "-f", "open-dci run"); err != nil {
		t.Fatal(err)
	}
	defer lab.Exec("gw-b", "pkill", "-CONT", "-f", "open-dci run")
	vrf := []string{"router bgp 4200000026 vrf vrf4011", "address-family ipv4 unicast"}
	if err := vtyshConf("gw-b", append(vrf, "rt vpn export 65535:1999")...); err != nil {
		t.Fatal(err)
	}
	defer vtyshConf("gw-b", append(vrf, "rt vpn both 65535:1001")...)

	waitFor(t, converge, func() error { // gw-b really sends the foreign RT only
		out, err := lab.Vtysh("gw-b", "show bgp ipv4 vpn "+mB.v4+"/32")
		if err != nil || !strings.Contains(out, "Extended Community: RT:65535:1999\n") {
			return fmt.Errorf("foreign RT not exported yet: %v\n%s", err, out)
		}
		return nil
	})
	waitFor(t, converge, absent("gw-a", "ipv4 vpn", mB.v4+"/32"))
	waitFor(t, converge, absent("m-a", "ipv4 unicast", mB.v4+"/32"))
	// tenant 2 is unaffected
	waitFor(t, converge, func() error { return lab.Ping("m-a2", mB2.v4, 0) })

	if err := vtyshConf("gw-b", append(vrf, "rt vpn both 65535:1001")...); err != nil {
		t.Fatal(err)
	}
	waitFor(t, converge, func() error { return lab.Ping("m-a", mB.v4, 0) })
}

// A peer that sends more VPN prefixes than maxPrefixes loses its session.
// open-dci restores the configured limit; the session comes back after a
// clear, as FRR keeps it down until then.
func TestMaxPrefixes(t *testing.T) {
	const peer = "fd00:dc1:b::1"
	if err := vtyshConf("gw-a", "router bgp 4200000016", "address-family ipv4 vpn", "neighbor "+peer+" maximum-prefix 1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, converge, func() error {
		var nb map[string]struct {
			State  string `json:"bgpState"`
			Reason string `json:"lastResetDueTo"`
		}
		if err := lab.VtyshJSON("gw-a", "show bgp neighbors "+peer, &nb); err != nil {
			return err
		}
		if n := nb[peer]; n.State == "Established" || !strings.Contains(strings.ToLower(n.Reason), "prefix") {
			b, _ := json.Marshal(n)
			return fmt.Errorf("session not torn down by the prefix limit: %s", b)
		}
		return nil
	})
	// open-dci puts the configured limit back (drift); then restart the session
	waitFor(t, heal, func() error {
		out, err := lab.Vtysh("gw-a", "show running-config")
		if err != nil {
			return err
		}
		if strings.Contains(out, "maximum-prefix 1\n") {
			return fmt.Errorf("limit not restored yet")
		}
		return nil
	})
	if _, err := lab.Vtysh("gw-a", "clear bgp "+peer); err != nil {
		t.Fatal(err)
	}
	waitFor(t, converge, func() error { return opendci("gw-a", "status") })
	waitFor(t, converge, func() error { return lab.Ping("m-a", mB.v4, 0) })
}
