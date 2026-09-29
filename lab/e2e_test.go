//go:build e2e

// End-to-end assertions for the lab: srv6-dci on metal-stack firewalls. fw-a
// runs the SRv6 transport in a dedicated DCI network (EVPN VRF), fw-b in the
// default VRF (fabric underlay), so the tests also cover mixed operation.
// Run against a deployed lab: make lab-check
package lab

import (
	"strings"
	"testing"
	"time"

	"github.com/mwindower/srv6-dci/lab/internal/labtest"
)

var lab = labtest.Lab{Prefix: "clab-srv6-dci"}

const converge = 120 * time.Second

type partition struct {
	name, machine, leaf, fw, exit string
	tenantVRF, transportVRF       string // transportVRF "" = default VRF
	machine4, machine6            string // machine IPs (announced by the machine itself)
	sid                           string // End.DT46 SID of the tenant VRF
}

var (
	a = partition{"a", "m-a", "leaf-a", "fw-a", "exit-a", "vrf3981", "vrf104100", "10.0.16.10", "2001:db8:16::10", "fd00:dc1:a:1::"}
	b = partition{"b", "m-b", "leaf-b", "fw-b", "exit-b", "vrf4011", "", "10.0.32.10", "2001:db8:32::10", "fd00:dc1:b:1::"}
)

func TestControlPlane(t *testing.T) {
	for _, n := range []string{"m-a", "leaf-a", "fw-a", "spine-a", "exit-a", "core", "exit-b", "spine-b", "fw-b", "leaf-b", "m-b"} {
		t.Run("bgp-established/"+n, func(t *testing.T) {
			labtest.Eventually(t, converge, func() error { return lab.BGPEstablished(n) })
		})
	}

	for _, p := range []partition{a, b} {
		t.Run("end-dt46-for-tenant-vrf/"+p.fw, func(t *testing.T) {
			labtest.Eventually(t, converge, labtest.Contains(func() (string, error) {
				return lab.Exec(p.fw, "ip", "-6", "route", "show", p.sid)
			}, "seg6local action End.DT46 vrftable 1000"))
		})
		// the remote locator arrives in the transport VRF (fw-a: via the DCI
		// network, which the leaf only passes if the firewall is attached to its
		// VNI) or in the default VRF (fw-b: via the IPv6 underlay)
		t.Run("remote-locator-reachable/"+p.fw, func(t *testing.T) {
			other := map[string]string{"a": "fd00:dc1:b::/48", "b": "fd00:dc1:a::/48"}[p.name]
			labtest.Eventually(t, converge, labtest.Contains(func() (string, error) {
				return lab.KernelRoute(p.fw, p.transportVRF, other)
			}, "proto bgp"))
		})
	}

	// machine route learned by the leaf -> type-5 -> firewall -> VPN with SID
	for _, c := range []struct{ fw, cmd string }{
		{"fw-a", "show bgp ipv4 vpn 10.0.16.10/32"},
		{"fw-b", "show bgp ipv4 vpn 10.0.32.10/32"},
		{"fw-a", "show bgp ipv6 vpn 2001:db8:16::10/128"},
	} {
		t.Run("evpn-to-vpn-with-sid/"+c.fw+"/"+c.cmd, func(t *testing.T) {
			labtest.Eventually(t, converge, labtest.Contains(func() (string, error) { return lab.Vtysh(c.fw, c.cmd) }, "Remote SID"))
		})
	}

	for _, c := range []struct {
		p   partition
		dst string
	}{{a, b.machine4}, {b, a.machine4}, {a, b.machine6}} {
		t.Run("tenant-route-seg6-encap/"+c.p.fw+"/"+c.dst, func(t *testing.T) {
			labtest.Eventually(t, converge, labtest.Contains(func() (string, error) {
				return lab.KernelRoute(c.p.fw, c.p.tenantVRF, c.dst)
			}, "encap seg6"))
		})
	}

	// remote machine arrives at the local machine as a plain BGP route from its leaf
	for _, c := range []struct{ p, remote partition }{{a, b}, {b, a}} {
		t.Run("machine-learns-remote-machine/"+c.p.machine, func(t *testing.T) {
			labtest.Eventually(t, converge, labtest.Contains(func() (string, error) {
				return lab.KernelRoute(c.p.machine, "", c.remote.machine4)
			}, "proto bgp"))
		})
	}

	// tenant prefixes never leave the tenant VRFs: exits and core only see DCI locators
	for _, n := range []string{"exit-a", "core", "exit-b"} {
		t.Run("no-tenant-state/"+n, func(t *testing.T) {
			out, err := lab.Vtysh(n, "show ip route vrf all")
			if err != nil {
				t.Fatal(err)
			}
			for _, pfx := range []string{a.machine4, b.machine4} {
				if strings.Contains(out, pfx) {
					t.Fatalf("%s knows tenant prefix %s:\n%s", n, pfx, out)
				}
			}
		})
	}
}

func TestDataPlane(t *testing.T) {
	for _, c := range []struct{ name, src, dst string }{
		{"v4 a->b", "m-a", b.machine4},
		{"v4 b->a", "m-b", a.machine4},
		{"v6 a->b", "m-a", b.machine6},
		{"v6 b->a", "m-b", a.machine6},
	} {
		t.Run(c.name, func(t *testing.T) {
			labtest.Eventually(t, converge, func() error { return lab.Ping(c.src, c.dst, 0) })
		})
	}
}

// Machines use MTU 9000 as in metal-stack. On the way the packet grows to 9048
// (SRv6 incl. SRH) and to 9098 in the DCI VNI on the fabric (firewall uplinks 9216).
// With metal-stack's default 9000 on the DCI devices these packets are silently
// dropped; the lab sizes them 9166.
func TestMTU(t *testing.T) {
	const machineMTU = 9000
	for _, c := range []struct {
		name, src, dst string
		size           int
	}{
		{"v6 a->b", "m-a", b.machine6, machineMTU - 48},
		{"v6 b->a", "m-b", a.machine6, machineMTU - 48},
		{"v4 a->b", "m-a", b.machine4, machineMTU - 28},
	} {
		t.Run(c.name, func(t *testing.T) {
			labtest.Eventually(t, 20*time.Second, func() error { return lab.Ping(c.src, c.dst, c.size) })
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
