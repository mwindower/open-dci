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
