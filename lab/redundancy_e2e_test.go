//go:build e2e

package lab

import (
	"fmt"
	"strings"
	"testing"
)

// Each partition has a redundant pair of gateways sharing the locator
// (anycast) and pinned SIDs. When one of them fails, remote gateways keep
// sending to the same SIDs; the transport delivers them to the partner.
func TestFailover(t *testing.T) {
	for _, gw := range []string{"gw-a1", "gw-b2"} {
		t.Run(gw, func(t *testing.T) {
			if _, err := lab.Exec(gw, "ip", "link", "set", "uplink0", "down"); err != nil {
				t.Fatal(err)
			}
			restored := false
			restore := func() {
				if !restored {
					lab.Exec(gw, "ip", "link", "set", "uplink0", "up")
					restored = true
				}
			}
			defer restore()

			// the partner carries everything, in both directions
			for _, f := range flows {
				waitFor(t, converge, func() error { return lab.Ping(f[0].name, f[1].v4, 0) })
				waitFor(t, converge, func() error { return lab.Ping(f[0].name, f[1].v6, 0) })
			}
			restore()
			waitFor(t, converge, func() error { return opendci(gw, "status") })
			for _, f := range flows {
				waitFor(t, converge, func() error { return lab.Ping(f[0].name, f[1].v4, 0) })
			}
		})
	}
}

// Remote gateways learn every prefix from both gateways of a pair (own RD
// each), both with the pair's anycast SID.
func TestBothPathsSameSID(t *testing.T) {
	for _, c := range []struct {
		gw, loc string // loc: the remote pair's locator
		dst     machine
		rds     []string
	}{
		{"gw-a1", "fd00:dc1:b::", mB, []string{"10.0.1.16:1001", "10.0.1.17:1001"}},
		{"gw-b2", "fd00:dc1:a::", mA2, []string{"10.0.0.16:1002", "10.0.0.17:1002"}},
	} {
		for _, rd := range c.rds {
			t.Run(c.gw+"/"+rd, func(t *testing.T) {
				waitFor(t, converge, func() error {
					out, err := lab.Vtysh(c.gw, "show bgp ipv4 vpn rd "+rd+" "+c.dst.v4+"/32")
					if err != nil {
						return err
					}
					// FRR shows the SID as locator + label (transposition)
					if !strings.Contains(out, "Remote SID: "+c.loc+",") {
						return fmt.Errorf("no path via rd %s with SID in %s:\n%s", rd, c.loc, out)
					}
					return nil
				})
			})
		}
	}
}

// No full mesh: each gateway's only VPN sessions are the ones to its two
// exits; the exits relay the routes between the partitions, with SID and RD
// intact.
func TestGatewaysPeerWithTheirExit(t *testing.T) {
	for _, g := range gateways {
		t.Run(g.name, func(t *testing.T) {
			var sum struct {
				Peers map[string]struct {
					State string `json:"state"`
				} `json:"peers"`
			}
			if err := lab.VtyshJSON(g.name, "show bgp ipv4 vpn summary", &sum); err != nil {
				t.Fatal(err)
			}
			if len(sum.Peers) != 2 || sum.Peers["uplink0"].State != "Established" || sum.Peers["uplink1"].State != "Established" {
				t.Fatalf("want exactly the two exit sessions (uplink0/1, Established), got %+v", sum.Peers)
			}
		})
	}
	// the exits hold the routes of every gateway (relay), but import none
	for _, c := range []struct{ exit, rd, prefix string }{
		{"exit-a1", "10.0.1.16:1001", mB.v4 + "/32"}, {"exit-a2", "10.0.0.17:1002", mA2.v4 + "/32"},
		{"exit-b1", "10.0.0.16:1001", mA.v4 + "/32"}, {"exit-b2", "10.0.1.17:1002", mB2.v4 + "/32"},
	} {
		t.Run(c.exit+"/"+c.rd, func(t *testing.T) {
			waitFor(t, converge, func() error {
				out, err := lab.Vtysh(c.exit, "show bgp ipv4 vpn rd "+c.rd+" "+c.prefix)
				if err != nil || !strings.Contains(out, "Remote SID") {
					return fmt.Errorf("%s doesn't relay %s %s: %v\n%s", c.exit, c.rd, c.prefix, err, out)
				}
				return nil
			})
		})
	}
}

// Each gateway is attached to both exits of its partition. When a whole exit
// fails, the fabric, the transport and the VPN routes continue via the other.
func TestExitFailover(t *testing.T) {
	for _, c := range []struct {
		exit string
		ifs  []string
	}{
		{"exit-a1", []string{"swp1", "swp2", "swp3", "swp4", "swp5"}},
		{"exit-b2", []string{"swp1", "swp2", "swp3", "swp4"}},
	} {
		t.Run(c.exit, func(t *testing.T) {
			set := func(state string) {
				for _, i := range c.ifs {
					lab.Exec(c.exit, "ip", "link", "set", i, state)
				}
			}
			set("down")
			restored := false
			restore := func() {
				if !restored {
					set("up")
					restored = true
				}
			}
			defer restore()

			for _, f := range flows {
				waitFor(t, converge, func() error { return lab.Ping(f[0].name, f[1].v4, 0) })
				waitFor(t, converge, func() error { return lab.Ping(f[0].name, f[1].v6, 0) })
			}
			restore()
			for _, gw := range append(append([]string{}, pairA...), pairB...) {
				waitFor(t, converge, func() error { return opendci(gw, "status") })
			}
		})
	}
}

// Dual-attached gateways must not become transit routers between their two
// exits (only-self-out in their base config): the exits reach each other via
// the spine or the core, never through a gateway port (swp3/swp4).
func TestGatewaysAreNotTransit(t *testing.T) {
	for _, c := range []struct{ exit, dst string }{
		{"exit-a1", "10.0.0.18"}, {"exit-a2", "10.0.0.14"}, // underlay loopbacks
		{"exit-b1", "2001:db8:e::b2"}, {"exit-b2", "2001:db8:e::b1"}, // IPv6 loopbacks
	} {
		t.Run(c.exit+"/"+c.dst, func(t *testing.T) {
			waitFor(t, converge, func() error {
				out, err := lab.KernelRoute(c.exit, "", c.dst)
				if err != nil || !strings.Contains(out, c.dst) {
					return fmt.Errorf("no route: %v\n%s", err, out)
				}
				if strings.Contains(out, "swp3") || strings.Contains(out, "swp4") {
					return fmt.Errorf("%s reaches %s through a gateway:\n%s", c.exit, c.dst, out)
				}
				return nil
			})
		})
	}
}

// The transport stays out of the fabric: no leaf or spine has a route into the
// locator block, in any table. In default-VRF mode (partition B) this depends
// on the exits announcing locators only to gateways and core; in DCI-network
// mode (partition A) the transport VRF doesn't exist on them at all.
func TestFabricHasNoTransportRoutes(t *testing.T) {
	for _, n := range []string{"leaf-a", "spine-a", "leaf-b", "spine-b"} {
		t.Run(n, func(t *testing.T) {
			out, err := lab.Exec(n, "ip", "-6", "route", "show", "table", "all")
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range strings.Split(out, "\n") {
				if strings.HasPrefix(l, "fd00:dc1:") {
					t.Errorf("%s has a route into the locator block: %s", n, l)
				}
			}
		})
	}
}

// The exits relay the VPN routes in a ladder: each exit peers with its two
// gateways and with both exits of the neighbouring partition, never with the
// other exit of its own partition.
func TestExitLadder(t *testing.T) {
	for _, c := range []struct {
		exit    string
		want    []string
		partner string
	}{
		{"exit-a1", []string{"swp3", "swp4", "2001:db8:e::b1", "2001:db8:e::b2"}, "2001:db8:e::a2"},
		{"exit-a2", []string{"swp3", "swp4", "2001:db8:e::b1", "2001:db8:e::b2"}, "2001:db8:e::a1"},
		{"exit-b1", []string{"swp3", "swp4", "2001:db8:e::a1", "2001:db8:e::a2"}, "2001:db8:e::b2"},
		{"exit-b2", []string{"swp3", "swp4", "2001:db8:e::a1", "2001:db8:e::a2"}, "2001:db8:e::b1"},
	} {
		t.Run(c.exit, func(t *testing.T) {
			waitFor(t, converge, func() error {
				var sum struct {
					Peers map[string]struct {
						State string `json:"state"`
					} `json:"peers"`
				}
				if err := lab.VtyshJSON(c.exit, "show bgp ipv4 vpn summary", &sum); err != nil {
					return err
				}
				if len(sum.Peers) != len(c.want) {
					return fmt.Errorf("want VPN sessions %v, got %+v", c.want, sum.Peers)
				}
				for _, p := range c.want {
					if sum.Peers[p].State != "Established" {
						return fmt.Errorf("session %s is %q", p, sum.Peers[p].State)
					}
				}
				if _, ok := sum.Peers[c.partner]; ok {
					return fmt.Errorf("%s peers with its partner exit %s", c.exit, c.partner)
				}
				return nil
			})
		})
	}
}
