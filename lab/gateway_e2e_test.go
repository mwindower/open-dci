//go:build e2e

package lab

import (
	"fmt"
	"strings"
	"testing"

	"github.com/mwindower/open-dci/lab/internal/labtest"
)

// Tenant 2 is stitched by dedicated gateways at the exits (gw-a, gw-b), which
// are not the tenant's VTEP: open-dci provisions the tenant VRFs there itself.
var (
	a2 = partition{"a", "m-a2", "leaf-a", "gw-a", "exit-a", "vrf3982", "", "10.0.17.10", "2001:db8:17::10", "fd00:dc1:a2:1::"}
	b2 = partition{"b", "m-b2", "leaf-b", "gw-b", "exit-b", "vrf4012", "", "10.0.33.10", "2001:db8:33::10", "fd00:dc1:b2:1::"}
)

func TestDedicatedGatewayControlPlane(t *testing.T) {
	for _, c := range []struct {
		p   partition
		vni string
	}{{a2, "3982"}, {b2, "4012"}} {
		// the provisioned VRF, bridge and VXLAN device, tagged as open-dci's
		t.Run("provisioned-devices/"+c.p.fw, func(t *testing.T) {
			for _, dev := range []string{c.p.tenantVRF, "dcibr" + c.vni, "dcivx" + c.vni} {
				labtest.Eventually(t, converge, labtest.Contains(func() (string, error) {
					return lab.Exec(c.p.fw, "ip", "link", "show", dev)
				}, "alias open-dci"))
			}
		})
		t.Run("l3vni-up/"+c.p.fw, func(t *testing.T) {
			labtest.Eventually(t, converge, labtest.Contains(func() (string, error) {
				return lab.Vtysh(c.p.fw, "show evpn vni "+c.vni)
			}, "State: Up"))
		})
		// table = VNI (default for provisioned VRFs)
		t.Run("end-dt46-for-tenant-vrf/"+c.p.fw, func(t *testing.T) {
			labtest.Eventually(t, converge, labtest.Contains(func() (string, error) {
				return lab.Exec(c.p.fw, "ip", "-6", "route", "show", c.p.sid)
			}, "seg6local action End.DT46 vrftable "+c.vni))
		})
	}

	for _, c := range []struct{ p, remote partition }{{a2, b2}, {b2, a2}} {
		t.Run("remote-locator-reachable/"+c.p.fw, func(t *testing.T) {
			labtest.Eventually(t, converge, labtest.Contains(func() (string, error) {
				return lab.KernelRoute(c.p.fw, "", strings.TrimSuffix(c.remote.sid, "1::")+":/48")
			}, "proto bgp"))
		})
		// the leaf's type-5 route of the machine reaches the gateway via the
		// exit and goes out as VPN route with SID; the remote one comes back
		t.Run("evpn-to-vpn-with-sid/"+c.p.fw, func(t *testing.T) {
			labtest.Eventually(t, converge, labtest.Contains(func() (string, error) {
				return lab.Vtysh(c.p.fw, "show bgp ipv4 vpn "+c.p.machine4+"/32")
			}, "Remote SID"))
		})
		t.Run("tenant-route-seg6-encap/"+c.p.fw, func(t *testing.T) {
			labtest.Eventually(t, converge, labtest.Contains(func() (string, error) {
				return lab.KernelRoute(c.p.fw, c.p.tenantVRF, c.remote.machine6)
			}, "encap seg6"))
		})
		t.Run("machine-learns-remote-machine/"+c.p.machine, func(t *testing.T) {
			labtest.Eventually(t, converge, labtest.Contains(func() (string, error) {
				return lab.KernelRoute(c.p.machine, "", c.remote.machine4)
			}, "proto bgp"))
		})
	}
}

func TestDedicatedGatewayDataPlane(t *testing.T) {
	for _, c := range []struct {
		name, src, dst string
		size           int
	}{
		{"v4 a->b", "m-a2", b2.machine4, 0},
		{"v4 b->a", "m-b2", a2.machine4, 0},
		{"v6 a->b", "m-a2", b2.machine6, 0},
		{"v6 b->a", "m-b2", a2.machine6, 0},
		{"v6 a->b full size", "m-a2", b2.machine6, 9000 - 48},
		{"v4 b->a full size", "m-b2", a2.machine4, 9000 - 28},
	} {
		t.Run(c.name, func(t *testing.T) {
			labtest.Eventually(t, converge, func() error { return lab.Ping(c.src, c.dst, c.size) })
		})
	}
}

// The two tenants share leaves, exits and core, but never see each other.
func TestTenantIsolation(t *testing.T) {
	for _, c := range []struct{ node, dst string }{
		{"m-a", b2.machine4}, {"m-a", a2.machine4}, {"m-a2", b.machine4}, {"m-b2", a.machine4},
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

// A provisioned network dropped from the config is removed completely: FRR
// VRF, BGP instance and the kernel devices. The extra network is applied once
// by hand; the sidecar, running with the real config and sharing the state
// file, has to clean it up.
func TestRemovesProvisionedNetwork(t *testing.T) {
	const node, vni = "gw-b", "4099"
	if _, err := lab.Exec(node, "sh", "-c",
		`cat /etc/open-dci/config.yaml > /tmp/extra.yaml && echo '  - {vrf: vrf`+vni+`, vni: `+vni+`, routeTarget: "65535:1099"}' >> /tmp/extra.yaml`); err != nil {
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
	// the real network is unaffected
	waitFor(t, heal, func() error { return opendci(node, "status") })
	waitFor(t, heal, func() error { return lab.Ping("m-b2", a2.machine4, 0) })
}

// open-dci never takes over a VRF it did not create: a vni on the firewall's
// existing (metal-networker) tenant VRF is refused before anything changes.
func TestRefusesToProvisionBaseVRF(t *testing.T) {
	if _, err := lab.Exec("fw-a", "sh", "-c",
		`sed 's/^    routeTarget:/    vni: 3981\n    routeTarget:/' /etc/open-dci/config.yaml > /tmp/adopt.yaml`); err != nil {
		t.Fatal(err)
	}
	out, err := lab.Exec("fw-a", "sh", "-c", "/usr/local/bin/open-dci apply -c /tmp/adopt.yaml --state /tmp/adopt.state 2>&1")
	if err == nil || !strings.Contains(out, "not created by open-dci") {
		t.Fatalf("want refusal, got err=%v:\n%s", err, out)
	}
	waitFor(t, heal, func() error { return opendci("fw-a", "status") })
}
