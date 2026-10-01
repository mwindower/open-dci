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
