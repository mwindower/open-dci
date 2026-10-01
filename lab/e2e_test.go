//go:build e2e

// End-to-end assertions for the lab: open-dci on redundant pairs of dedicated
// gateways at the exits. Pair A (gw-a1/gw-a2) runs the SRv6 transport in a DCI
// network (an EVPN VRF of its base config), pair B (gw-b1/gw-b2) in the default
// VRF (fabric underlay), so the tests also cover mixed operation. All gateways
// provision the VRFs of two tenants.
// Run against a deployed lab: make lab-check
package lab

import (
	"strings"
	"testing"
	"time"

	"github.com/mwindower/open-dci/lab/internal/labtest"
)

var lab = labtest.Lab{Prefix: "clab-open-dci"}

const converge = 120 * time.Second

type gateway struct {
	name, transportVRF string // transportVRF "" = default VRF
	remoteLocator      string // the other partition's (shared) locator
}

// machine is a tenant machine, its gateway pair and the VRF the gateways
// provision for it; sid is the pinned End.DT46 SID (function = VNI).
type machine struct {
	name     string
	gws      []string
	vrf, vni string
	sid      string
	v4, v6   string // announced by the machine itself
}

var (
	pairA = []string{"gw-a1", "gw-a2"}
	pairB = []string{"gw-b1", "gw-b2"}

	gateways = []gateway{
		{"gw-a1", "vrf104100", "fd00:dc1:b::/48"}, {"gw-a2", "vrf104100", "fd00:dc1:b::/48"},
		{"gw-b1", "", "fd00:dc1:a::/48"}, {"gw-b2", "", "fd00:dc1:a::/48"},
	}

	mA  = machine{"m-a", pairA, "vrf3981", "3981", "fd00:dc1:a:f8d::", "10.0.16.10", "2001:db8:16::10"}  // tenant 1
	mB  = machine{"m-b", pairB, "vrf4011", "4011", "fd00:dc1:b:fab::", "10.0.32.10", "2001:db8:32::10"}  // tenant 1
	mA2 = machine{"m-a2", pairA, "vrf3982", "3982", "fd00:dc1:a:f8e::", "10.0.17.10", "2001:db8:17::10"} // tenant 2
	mB2 = machine{"m-b2", pairB, "vrf4012", "4012", "fd00:dc1:b:fac::", "10.0.33.10", "2001:db8:33::10"} // tenant 2

	machines = []machine{mA, mB, mA2, mB2}
	// stitched pairs, both directions
	flows = [][2]machine{{mA, mB}, {mB, mA}, {mA2, mB2}, {mB2, mA2}}
)

func TestControlPlane(t *testing.T) {
	for _, n := range []string{"m-a", "m-a2", "leaf-a", "spine-a", "exit-a", "gw-a1", "gw-a2", "core", "gw-b1", "gw-b2", "exit-b", "spine-b", "leaf-b", "m-b", "m-b2"} {
		t.Run("bgp-established/"+n, func(t *testing.T) {
			labtest.Eventually(t, converge, func() error { return lab.BGPEstablished(n) })
		})
	}

	for _, m := range machines {
		for _, gw := range m.gws {
			// the provisioned VRF, bridge and VXLAN device, tagged as open-dci's
			t.Run("provisioned-devices/"+gw+"/"+m.vrf, func(t *testing.T) {
				for _, dev := range []string{m.vrf, "dcibr" + m.vni, "dcivx" + m.vni} {
					labtest.Eventually(t, converge, labtest.Contains(func() (string, error) {
						return lab.Exec(gw, "ip", "link", "show", dev)
					}, "alias open-dci"))
				}
			})
			t.Run("l3vni-up/"+gw+"/"+m.vni, func(t *testing.T) {
				labtest.Eventually(t, converge, labtest.Contains(func() (string, error) {
					return lab.Vtysh(gw, "show evpn vni "+m.vni)
				}, "State: Up"))
			})
			// the pinned SID (function = VNI) into table = VNI, the same on
			// both gateways of the pair (anycast)
			t.Run("end-dt46/"+gw+"/"+m.vrf, func(t *testing.T) {
				labtest.Eventually(t, converge, labtest.Contains(func() (string, error) {
					return lab.Exec(gw, "ip", "-6", "route", "show", m.sid)
				}, "seg6local action End.DT46 vrftable "+m.vni))
			})
		}
	}

	// the remote locator arrives in the transport VRF (pair A: via the DCI
	// network) or in the default VRF (pair B: via the IPv6 underlay)
	for _, g := range gateways {
		t.Run("remote-locator-reachable/"+g.name, func(t *testing.T) {
			labtest.Eventually(t, converge, labtest.Contains(func() (string, error) {
				return lab.KernelRoute(g.name, g.transportVRF, g.remoteLocator)
			}, "proto bgp"))
		})
	}

	for _, f := range flows {
		src, dst := f[0], f[1]
		for _, gw := range src.gws {
			// the leaf's type-5 route of the machine reaches the gateway via
			// the exit and goes out as VPN route with SID
			t.Run("evpn-to-vpn-with-sid/"+gw+"/"+src.v4, func(t *testing.T) {
				labtest.Eventually(t, converge, labtest.Contains(func() (string, error) {
					return lab.Vtysh(gw, "show bgp ipv4 vpn "+src.v4+"/32")
				}, "Remote SID"))
			})
			// remote machines via SRv6 to the remote pair's anycast SID, never
			// via the partner gateway's re-announcement in the fabric
			t.Run("tenant-route-seg6-encap/"+gw+"/"+dst.v6, func(t *testing.T) {
				labtest.Eventually(t, converge, labtest.Contains(func() (string, error) {
					return lab.KernelRoute(gw, src.vrf, dst.v6)
				}, "segs 1 [ "+dst.sid+" ]"))
			})
			// own machines via the fabric (EVPN), never back via SRv6: no loops
			t.Run("local-route-via-fabric/"+gw+"/"+src.v4, func(t *testing.T) {
				labtest.Eventually(t, converge, labtest.Contains(func() (string, error) {
					return lab.KernelRoute(gw, src.vrf, src.v4)
				}, "dev dcibr"+src.vni))
			})
		}
		// the remote machine arrives at the local one as a plain BGP route from its leaf
		t.Run("machine-learns-remote-machine/"+src.name, func(t *testing.T) {
			labtest.Eventually(t, converge, labtest.Contains(func() (string, error) {
				return lab.KernelRoute(src.name, "", dst.v4)
			}, "proto bgp"))
		})
	}

	// tenant prefixes never leave the tenant VRFs: exits and core only see locators
	for _, n := range []string{"exit-a", "core", "exit-b"} {
		t.Run("no-tenant-state/"+n, func(t *testing.T) {
			out, err := lab.Vtysh(n, "show ip route vrf all")
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range machines {
				if strings.Contains(out, m.v4) {
					t.Fatalf("%s knows tenant prefix %s:\n%s", n, m.v4, out)
				}
			}
		})
	}
}

func TestDataPlane(t *testing.T) {
	for _, f := range flows {
		for _, dst := range []string{f[1].v4, f[1].v6} {
			t.Run(f[0].name+"->"+dst, func(t *testing.T) {
				labtest.Eventually(t, converge, func() error { return lab.Ping(f[0].name, dst, 0) })
			})
		}
	}
}

// Machines use MTU 9000 as in metal-stack. On the way the packet grows to 9048
// (SRv6 incl. SRH) and, in gw-a's DCI network, to 9098 (VXLAN; uplinks 9216).
// With the base config's 9000 on the DCI devices these packets would be
// silently dropped; open-dci raises them to 9166.
func TestMTU(t *testing.T) {
	const machineMTU = 9000
	for _, c := range []struct {
		src, dst string
		size     int
	}{
		{"m-a", mB.v6, machineMTU - 48},
		{"m-b", mA.v6, machineMTU - 48},
		{"m-a", mB.v4, machineMTU - 28},
		{"m-b2", mA2.v6, machineMTU - 48},
	} {
		t.Run(c.src+"->"+c.dst, func(t *testing.T) {
			labtest.Eventually(t, 20*time.Second, func() error { return lab.Ping(c.src, c.dst, c.size) })
		})
	}
}

// The two tenants share leaves, exits, core and gateways, but never see each other.
func TestTenantIsolation(t *testing.T) {
	for _, c := range []struct{ node, dst string }{
		{"m-a", mB2.v4}, {"m-a", mA2.v4}, {"m-a2", mB.v4}, {"m-b2", mA.v4},
	} {
		t.Run(c.node+"->"+c.dst, func(t *testing.T) {
			out, _ := lab.KernelRoute(c.node, "", c.dst)
			if strings.Contains(out, "proto bgp") {
				t.Fatalf("%s has a route to the other tenant's %s: %s", c.node, c.dst, out)
			}
			if err := lab.Ping(c.node, c.dst, 0); err == nil {
				t.Fatalf("%s reaches the other tenant's %s", c.node, c.dst)
			}
		})
	}
}

// A lookup that finds nothing in a tenant VRF must not fall through to the
// main table (unreachable default). Only the locator block does, since SRv6
// encapsulation routes the outer packet in the tenant VRF's table; tenants
// themselves are kept out of the block by the gateway's ingress filter
// (TestGatewayDropsTenantToTransport).
func TestNoFallThrough(t *testing.T) {
	for _, c := range []struct{ node, vrf, dst string }{
		{"gw-a1", mA.vrf, "10.99.9.9"}, {"gw-a2", mA2.vrf, "2001:db8:99::1"},
		{"gw-b1", mB.vrf, "2001:db8:99::1"}, {"gw-b2", mB2.vrf, "10.99.9.9"},
		{"leaf-a", mA.vrf, "10.99.9.9"}, {"leaf-b", mB2.vrf, "2001:db8:99::1"},
	} {
		t.Run(c.node+"/"+c.vrf+"/"+c.dst, func(t *testing.T) {
			args := []string{"ip", "route", "get", c.dst, "vrf", c.vrf}
			if strings.Contains(c.dst, ":") {
				args = []string{"ip", "-6", "route", "get", c.dst, "vrf", c.vrf}
			}
			out, err := lab.Exec(c.node, args...)
			if err == nil || strings.Contains(out, "seg6local") || strings.Contains(out, "eth0") {
				t.Fatalf("lookup of %s in %s resolves: %v\n%s", c.dst, c.vrf, err, out)
			}
		})
	}
}

func waitFor(t *testing.T, d time.Duration, fn func() error) {
	t.Helper()
	labtest.Eventually(t, d, fn)
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
