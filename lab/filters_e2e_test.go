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
		for _, gw := range pairA {
			waitFor(t, converge, present(gw, "vrf vrf3981 ipv4 unicast", p)) // announced and learned
		}
	}
	for _, p := range []string{stray + "/32", "0.0.0.0/0"} {
		for _, c := range []struct{ node, table string }{
			{"gw-a1", "ipv4 vpn"}, {"gw-a2", "ipv4 vpn"}, // not exported
			{"gw-b1", "ipv4 vpn"}, {"gw-b2", "ipv4 vpn"},
			{"gw-b1", "vrf vrf4011 ipv4 unicast"},
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

// gw-b1 exports a prefix that pair A's allowlist doesn't cover (its own
// export list is widened by hand). Pair A receives it as VPN route, but
// doesn't import it into the tenant VRF.
func TestImportFilter(t *testing.T) {
	const stray = "10.99.1.1"
	const extra = "ip prefix-list DCI-vrf4011-v4 seq 1000 permit 10.99.0.0/16 le 32"
	// a line open-dci never applied: it neither removes nor needs it
	if err := vtyshConf("gw-b1", extra); err != nil {
		t.Fatal(err)
	}
	defer vtyshConf("gw-b1", "no "+extra)
	if _, err := lab.Exec("m-b", "ip", "addr", "add", stray+"/32", "dev", "lo"); err != nil {
		t.Fatal(err)
	}
	defer lab.Exec("m-b", "ip", "addr", "del", stray+"/32", "dev", "lo")

	waitFor(t, converge, present("gw-a1", "ipv4 vpn", stray+"/32")) // gw-b1 exported it
	for _, c := range []struct{ node, table string }{
		{"gw-a1", "vrf vrf3981 ipv4 unicast"},
		{"gw-a2", "vrf vrf3981 ipv4 unicast"},
		{"leaf-a", "vrf vrf3981 ipv4 unicast"},
		{"m-a", "ipv4 unicast"},
	} {
		if hasRoute(c.node, c.table, stray+"/32") {
			t.Errorf("%s was imported: %s %q", stray, c.node, c.table)
		}
	}
}

// gw-b1 sends tenant 1's routes with only a route target pair A doesn't know
// (set by hand on its VRF). Pair A drops them at the session already, before
// any VRF import, and keeps reaching partition B via gw-b2. gw-b1's sidecar is
// paused meanwhile, since it would revert the change within one interval.
func TestPeerRouteTargetFilter(t *testing.T) {
	const fromB1 = "ipv4 vpn rd 10.0.1.16:1001" // gw-b1's paths of tenant 1
	if _, err := lab.Exec("gw-b1", "pkill", "-STOP", "-f", "open-dci run"); err != nil {
		t.Fatal(err)
	}
	defer lab.Exec("gw-b1", "pkill", "-CONT", "-f", "open-dci run")
	vrf := []string{"router bgp 4200000026 vrf vrf4011", "address-family ipv4 unicast"}
	if err := vtyshConf("gw-b1", append(vrf, "rt vpn export 65535:1999")...); err != nil {
		t.Fatal(err)
	}
	defer vtyshConf("gw-b1", append(vrf, "rt vpn both 65535:1001")...)

	waitFor(t, converge, func() error { // gw-b1 really sends the foreign RT only
		out, err := lab.Vtysh("gw-b1", "show bgp "+fromB1+" "+mB.net4)
		if err != nil || !strings.Contains(out, "Extended Community: RT:65535:1999\n") {
			return fmt.Errorf("foreign RT not exported yet: %v\n%s", err, out)
		}
		return nil
	})
	for _, gw := range pairA {
		waitFor(t, converge, absent(gw, fromB1, mB.net4))
		waitFor(t, converge, present(gw, "ipv4 vpn rd 10.0.1.17:1001", mB.net4)) // gw-b2's
	}
	// tenant 1 keeps working via gw-b2, tenant 2 is unaffected
	waitFor(t, converge, func() error { return lab.Ping("m-a", mB.v4, 0) })
	waitFor(t, converge, func() error { return lab.Ping("m-a2", mB2.v4, 0) })

	if err := vtyshConf("gw-b1", append(vrf, "rt vpn both 65535:1001")...); err != nil {
		t.Fatal(err)
	}
	for _, gw := range pairA {
		waitFor(t, converge, present(gw, fromB1, mB.net4))
	}
}

// A peer that sends more VPN prefixes than maxPrefixes loses its session (here
// the exit, so gw-a1 drops out; its partner keeps the partition connected).
// open-dci restores the configured limit; the session comes back after a
// clear, as FRR keeps it down until then.
func TestMaxPrefixes(t *testing.T) {
	const peer = "uplink0" // the session to the exit, carrying all partitions' routes
	if err := vtyshConf("gw-a1", "router bgp 4200000016", "address-family ipv4 vpn", "neighbor "+peer+" maximum-prefix 1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, converge, func() error {
		var nb map[string]struct {
			State  string `json:"bgpState"`
			Reason string `json:"lastResetDueTo"`
			// FRR 10.4 reports the reset as "BGP Notification send" and
			// flags the limit separately
			Exceeded bool `json:"prefixesConfigExceedMax"`
		}
		if err := lab.VtyshJSON("gw-a1", "show bgp neighbors "+peer, &nb); err != nil {
			return err
		}
		if n := nb[peer]; n.State == "Established" || !n.Exceeded && !strings.Contains(strings.ToLower(n.Reason), "prefix") {
			b, _ := json.Marshal(n)
			return fmt.Errorf("session not torn down by the prefix limit: %s", b)
		}
		return nil
	})
	// open-dci puts the configured limit back (drift); then restart the session
	waitFor(t, heal, func() error {
		out, err := lab.Vtysh("gw-a1", "show running-config")
		if err != nil {
			return err
		}
		if strings.Contains(out, "maximum-prefix 1\n") {
			return fmt.Errorf("limit not restored yet")
		}
		return nil
	})
	if _, err := lab.Vtysh("gw-a1", "clear bgp "+peer); err != nil {
		t.Fatal(err)
	}
	waitFor(t, converge, func() error { return opendci("gw-a1", "status") })
	waitFor(t, converge, func() error { return lab.Ping("m-a", mB.v4, 0) })
}

// An aggregate disappears everywhere once its partition has no more specific
// route left in it: from the gateways' VPN tables, from the other partitions'
// fabrics, and as blackhole from the own gateways. Here m-a, tenant 1's only
// machine in partition A, stops announcing itself. FRR 10.6.0 kept the VPN and
// type-5 routes it had leaked from a tenant VRF after their source was
// withdrawn, so a removed prefix stayed routed (into a black hole) in every
// other partition; the lab runs 10.4.1.
func TestWithdrawal(t *testing.T) {
	type where struct{ node, table, prefix string }
	var routed []where
	for _, p := range []struct{ af, net string }{{"ipv4", mA.net4}, {"ipv6", mA.net6}} {
		routed = append(routed,
			where{"gw-a1", p.af + " vpn", p.net}, where{"gw-a2", p.af + " vpn", p.net},
			where{"gw-b1", p.af + " vpn", p.net}, where{"gw-c2", p.af + " vpn", p.net},
			where{"gw-b1", "vrf vrf4011 " + p.af + " unicast", p.net},
			where{"leaf-b", "vrf vrf4011 " + p.af + " unicast", p.net},
			where{"leaf-c", "vrf vrf5011 " + p.af + " unicast", p.net})
	}
	for _, w := range routed {
		waitFor(t, converge, present(w.node, w.table, w.prefix))
	}
	waitFor(t, converge, func() error { return lab.Ping("m-b", mA.v4, 0) })

	session := []string{"router bgp 4200000015", "neighbor lan0 shutdown"}
	if err := vtyshConf("m-a", session...); err != nil {
		t.Fatal(err)
	}
	restored := false
	defer func() {
		if !restored {
			vtyshConf("m-a", "router bgp 4200000015", "no neighbor lan0 shutdown")
		}
	}()
	for _, w := range routed {
		waitFor(t, converge, absent(w.node, w.table, w.prefix))
	}
	for _, gw := range pairA {
		waitFor(t, converge, func() error {
			out, err := lab.KernelRoute(gw, mA.vrf, mA.net4)
			if err != nil || strings.Contains(out, "blackhole") {
				return fmt.Errorf("%s keeps the blackhole for %s: %v %s", gw, mA.net4, err, out)
			}
			return nil
		})
	}
	// and no type-5 route for it is left in partition B's fabric
	waitFor(t, converge, func() error {
		out, err := lab.Vtysh("leaf-b", "show bgp l2vpn evpn route type prefix")
		if err != nil {
			return err
		}
		if strings.Contains(out, "[10.0.16.0]") || strings.Contains(out, "[2001:db8:16::]") {
			return fmt.Errorf("leaf-b still has a type-5 route for %s", mA.net4)
		}
		return nil
	})

	if err := vtyshConf("m-a", "router bgp 4200000015", "no neighbor lan0 shutdown"); err != nil {
		t.Fatal(err)
	}
	restored = true
	for _, w := range routed {
		waitFor(t, converge, present(w.node, w.table, w.prefix))
	}
	waitFor(t, converge, func() error { return lab.Ping("m-b", mA.v4, 0) })
	waitFor(t, converge, func() error { return lab.Ping("m-b", mA.v6, 0) })
}

// Each pair announces its partition's ranges (networks[].aggregates) instead
// of the machines' host routes: the other partitions only learn the aggregate,
// the own fabric doesn't get it back as type-5, and the pair drops traffic to
// unused addresses of its range.
func TestAggregation(t *testing.T) {
	type5 := func(leaf string) (string, error) { return lab.Vtysh(leaf, "show bgp l2vpn evpn route type prefix") }
	for _, c := range []struct {
		m           machine
		leaf, other string // the own partition's leaf, another partition's
		remote      []string
	}{
		{mA, "leaf-a", "leaf-b", append(pairB, pairC...)},
		{mB, "leaf-b", "leaf-c", append(pairA, pairC...)},
		{mC2, "leaf-c", "leaf-a", append(pairA, pairB...)},
	} {
		t.Run(c.m.name, func(t *testing.T) {
			for _, gw := range c.remote {
				waitFor(t, converge, present(gw, "ipv4 vpn", c.m.net4))
				waitFor(t, converge, present(gw, "ipv6 vpn", c.m.net6))
				waitFor(t, converge, absent(gw, "ipv4 vpn", c.m.v4+"/32"))
				waitFor(t, converge, absent(gw, "ipv6 vpn", c.m.v6+"/128"))
			}
			v4, v6 := "["+strings.TrimSuffix(c.m.net4, "/24")+"]", "["+strings.TrimSuffix(c.m.net6, "/48")+"]"
			waitFor(t, converge, func() error { // the other partitions route the range to the gateways
				out, err := type5(c.other)
				if err != nil || !strings.Contains(out, v4) || !strings.Contains(out, v6) {
					return fmt.Errorf("%s lacks type-5 %s/%s: %v", c.other, v4, v6, err)
				}
				return nil
			})
			out, err := type5(c.leaf) // the own partition has the host routes
			if err != nil || strings.Contains(out, v4) || strings.Contains(out, v6) {
				t.Errorf("%s got its own aggregate %s/%s back as type-5: %v\n%s", c.leaf, v4, v6, err, out)
			}
			for _, gw := range c.m.gws {
				for _, net := range []string{c.m.net4, c.m.net6} {
					if out, _ := lab.KernelRoute(gw, c.m.vrf, net); !strings.Contains(out, "blackhole") {
						t.Errorf("%s: no blackhole for %s in %s: %s", gw, net, c.m.vrf, out)
					}
				}
			}
		})
	}
	waitFor(t, converge, func() error { return lab.Ping("m-b", mA.v4, 0) })
	if err := lab.Ping("m-b", "10.0.16.99", 0); err == nil {
		t.Error("an unused address of partition A's range answers")
	}
}
