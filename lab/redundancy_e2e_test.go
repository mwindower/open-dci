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
					out, err := lab.Vtysh(c.gw, "show bgp ipv4 vpn rd "+rd+" "+c.dst.net4)
					if err != nil {
						return err
					}
					// FRR shows the SID as locator + label (transposition);
					// 10.6 appends ", sid structure=...", 10.4 ends the line
					if !strings.Contains(out, "Remote SID: "+c.loc+",") && !strings.Contains(out, "Remote SID: "+c.loc+"\n") {
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
		{"exit-a1", "10.0.1.16:1001", mB.net4}, {"exit-a2", "10.0.0.17:1002", mA2.net4},
		{"exit-b1", "10.0.0.16:1001", mA.net4}, {"exit-b2", "10.0.1.17:1002", mB2.net4},
		{"exit-c1", "10.0.1.16:1001", mB.net4}, {"exit-c2", "10.0.0.17:1002", mA2.net4},
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
			for _, gw := range append(append(append([]string{}, pairA...), pairB...), pairC...) {
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
	for _, n := range []string{"leaf-a", "spine-a", "leaf-b", "spine-b", "leaf-c", "spine-c"} {
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

// The core announces each exit link only what its VRF needs (route-maps in
// configs/core/frr.conf): exit-a1/a2's default VRF gets the exit loopbacks
// for the VPN relay, never the locator block; their DCI VRF gets the block,
// never the exit loopbacks.
func TestCoreAnnouncesPerVRF(t *testing.T) {
	for _, exit := range []string{"exit-a1", "exit-a2"} {
		for _, c := range []struct {
			vrf, want, never string
		}{
			{"", "2001:db8:e::b1", "fd00:dc1:"},
			{"vrf104100", "fd00:dc1:b::/48", "2001:db8:e::"},
		} {
			t.Run(exit+"/"+c.vrf, func(t *testing.T) {
				args := []string{"ip", "-6", "route", "show"}
				if c.vrf != "" {
					args = append(args, "vrf", c.vrf)
				}
				waitFor(t, converge, func() error {
					out, err := lab.Exec(exit, args...)
					if err != nil {
						return err
					}
					if !strings.Contains(out, c.want) {
						return fmt.Errorf("no route to %s:\n%s", c.want, out)
					}
					for _, l := range strings.Split(out, "\n") {
						if strings.HasPrefix(l, c.never) && !strings.Contains(l, "dev lo ") {
							return fmt.Errorf("unexpected route: %s", l)
						}
					}
					return nil
				})
			})
		}
	}
}

// The exits relay the VPN routes in a closed ladder: each exit peers with its
// two gateways and with both exits of the neighbouring partitions (with three
// partitions: both other ones), never with the other exit of its own
// partition.
func TestExitLadder(t *testing.T) {
	all := map[string][]string{
		"a": {"2001:db8:e::a1", "2001:db8:e::a2"},
		"b": {"2001:db8:e::b1", "2001:db8:e::b2"},
		"c": {"2001:db8:e::c1", "2001:db8:e::c2"},
	}
	type exitCase struct {
		exit    string
		want    []string
		partner string
	}
	var cases []exitCase
	for _, p := range []string{"a", "b", "c"} {
		// with three partitions in a ring, both other partitions are neighbours
		var others []string
		for _, q := range []string{"a", "b", "c"} {
			if q != p {
				others = append(others, all[q]...)
			}
		}
		for i, n := range []string{"1", "2"} {
			cases = append(cases, exitCase{"exit-" + p + n, append([]string{"swp3", "swp4"}, others...), all[p][1-i]})
		}
	}
	for _, c := range cases {
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
