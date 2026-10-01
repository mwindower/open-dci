//go:build e2e

package lab

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A gateway announces its (anycast) locator only once its tenant VRFs hold the
// fabric's routes: after its links come back, the exit must not send it
// traffic it would decapsulate into an empty VRF. Both transport modes: gw-b2
// announces in the default VRF (exit-b1 learns it via swp4), gw-a2 via the DCI
// network (exit-a1 learns it as type-5 from VTEP 10.0.0.17).
func TestLocatorWithheldUntilReady(t *testing.T) {
	for _, c := range []struct {
		gw, exit   string
		exitRoute  []string // the exit's route to the locator
		viaGateway string   // marks the gateway's next hop in it
		tenant     machine  // must be reachable in the gateway's tenant VRF before
	}{
		{"gw-b2", "exit-b1", []string{"ip", "-6", "route", "show", "fd00:dc1:b::/48"}, "dev swp4", mB},
		{"gw-a2", "exit-a1", []string{"ip", "-6", "route", "show", "vrf", "vrf104100", "fd00:dc1:a::/48"}, "10.0.0.17", mA},
	} {
		t.Run(c.gw, func(t *testing.T) {
			links := func(state string) {
				lab.Exec(c.gw, "sh", "-c", "ip link set uplink0 "+state+"; ip link set uplink1 "+state)
			}
			links("down")
			restored := false
			defer func() {
				if !restored {
					links("up")
				}
			}()
			waitFor(t, converge, func() error {
				if announced(t, c.gw) {
					return fmt.Errorf("%s still announces its locator", c.gw)
				}
				return nil
			})

			links("up")
			restored = true
			// until the gateway announces, the exit has no path via it; once it
			// has one, the gateway's tenant VRF already knows the local machine
			deadline := time.Now().Add(converge)
			for {
				out, err := lab.Exec(c.exit, c.exitRoute...)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(out, c.viaGateway) {
					route, _ := lab.Exec(c.gw, "ip", "route", "show", "vrf", c.tenant.vrf, c.tenant.v4)
					if !strings.Contains(route, c.tenant.v4) {
						t.Fatalf("%s sends to %s before its %s has a route to %s:\n%s", c.exit, c.gw, c.tenant.vrf, c.tenant.v4, out)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("%s never learned the locator via %s again:\n%s", c.exit, c.gw, out)
				}
				time.Sleep(50 * time.Millisecond)
			}
			waitFor(t, converge, func() error { return opendci(c.gw, "status") })
		})
	}
}

// announced reads the gateway's status: is its locator announced?
func announced(t *testing.T, gw string) bool {
	t.Helper()
	out, _ := lab.Exec(gw, "sh", "-c", "OPEN_DCI_OUTPUT=json /usr/local/bin/open-dci status -c /etc/open-dci/config.yaml")
	var st struct{ Announced bool }
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("%s: status: %v\n%s", gw, err, out)
	}
	return st.Announced
}

// Planned maintenance: a drained gateway withdraws its locator and its
// type-5 routes, so neither the exits nor the leaves send it anything; the
// partner carries all flows, and the gateway can then be stopped. undrain
// brings it back.
func TestDrain(t *testing.T) {
	const gw = "gw-b2"
	viaExit := func() (string, error) { return lab.Exec("exit-b1", "ip", "-6", "route", "show", "fd00:dc1:b::/48") }
	viaLeaf := func() (string, error) { return lab.Exec("leaf-b", "ip", "route", "show", "vrf", mB.vrf, mA.v4) }
	usesGW := func(want bool) func() error {
		return func() error {
			e, err := viaExit()
			if err != nil {
				return err
			}
			l, err := viaLeaf()
			if err != nil {
				return err
			}
			if strings.Contains(e, "dev swp4") != want || strings.Contains(l, "10.0.1.17") != want {
				return fmt.Errorf("%s in use: want %v\nexit-b1: %s\nleaf-b: %s", gw, want, e, l)
			}
			return nil
		}
	}
	waitFor(t, converge, usesGW(true))
	if err := opendci(gw, "drain"); err != nil {
		t.Fatal(err)
	}
	defer opendci(gw, "undrain")
	waitFor(t, converge, usesGW(false))
	// still healthy: drained on purpose
	if err := opendci(gw, "status"); err != nil {
		t.Errorf("drained gateway not healthy: %v", err)
	}
	// the sidecar keeps it drained across reconciles
	time.Sleep(12 * time.Second)
	if err := usesGW(false)(); err != nil {
		t.Fatalf("drain reverted: %v", err)
	}
	for _, f := range flows {
		waitFor(t, converge, func() error { return lab.Ping(f[0].name, f[1].v4, 0) })
	}

	if err := opendci(gw, "undrain"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, converge, usesGW(true))
	waitFor(t, converge, func() error { return opendci(gw, "status") })
}

// A gray failure: BGP is fine, but the gateway can't forward (here: kernel
// routes make every remote locator unreachable, so it can't encapsulate).
// open-dci notices it, withdraws the gateway like a drain, and announces it
// again once it has recovered. Both transport modes.
func TestWithdrawOnGrayFailure(t *testing.T) {
	for _, c := range []struct {
		gw, exit, leaf, vrf string
		blackhole           string // ip -6 route args to break the transport
		exitRoute           []string
		viaGW, viaGWVTEP    string
		peer                machine // a remote machine to ping
		local               machine
	}{
		{"gw-b2", "exit-b1", "leaf-b", "", "", []string{"ip", "-6", "route", "show", "fd00:dc1:b::/48"}, "dev swp4", "10.0.1.17", mA, mB},
		{"gw-a2", "exit-a1", "leaf-a", "vrf104100", "vrf vrf104100", []string{"ip", "-6", "route", "show", "vrf", "vrf104100", "fd00:dc1:a::/48"}, "10.0.0.17", "10.0.0.17", mB, mA},
	} {
		t.Run(c.gw, func(t *testing.T) {
			routes := func(op string) string {
				var cmds []string
				for _, loc := range []string{"fd00:dc1:a::/48", "fd00:dc1:b::/48", "fd00:dc1:c::/48"} {
					if strings.HasPrefix(c.local.sid, loc[:len(loc)-5]) {
						continue // the own locator
					}
					cmds = append(cmds, fmt.Sprintf("ip -6 route %s unreachable %s %s metric 1", op, loc, c.blackhole))
				}
				return strings.Join(cmds, "; ")
			}
			usesGW := func(want bool) func() error {
				return func() error {
					e, _ := lab.Exec(c.exit, c.exitRoute...)
					l, _ := lab.Exec(c.leaf, "ip", "route", "show", "vrf", c.local.vrf, c.peer.v4)
					if strings.Contains(e, c.viaGW) != want || strings.Contains(l, c.viaGWVTEP) != want {
						return fmt.Errorf("%s in use: want %v\n%s: %s\n%s: %s", c.gw, want, c.exit, e, c.leaf, l)
					}
					return nil
				}
			}
			waitFor(t, converge, usesGW(true))
			if out, err := lab.Exec(c.gw, "sh", "-c", routes("add")); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			restored := false
			defer func() {
				if !restored {
					lab.Exec(c.gw, "sh", "-c", routes("del"))
				}
			}()
			waitFor(t, converge, func() error {
				if err := opendci(c.gw, "status"); err == nil {
					return fmt.Errorf("%s still reports healthy", c.gw)
				}
				return nil
			})
			waitFor(t, converge, usesGW(false))
			waitFor(t, converge, func() error { return lab.Ping(c.local.name, c.peer.v4, 0) })
			waitFor(t, converge, func() error { return lab.Ping(c.peer.name, c.local.v4, 0) })

			lab.Exec(c.gw, "sh", "-c", routes("del"))
			restored = true
			waitFor(t, converge, usesGW(true))
			waitFor(t, converge, func() error { return opendci(c.gw, "status") })
		})
	}
}
