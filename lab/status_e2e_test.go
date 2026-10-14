//go:build e2e

package lab

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	ogateway "github.com/mwindower/open-dci/internal/gateway"
)

// statusRoutes runs "status --routes <vrf>" as JSON and returns the network's routes.
func statusRoutes(gw, vrf string) (*ogateway.Routes, error) {
	out, err := lab.Exec(gw, "sh", "-c", "OPEN_DCI_OUTPUT=json /usr/local/bin/open-dci status -c /etc/open-dci/config.yaml --routes "+vrf)
	if err != nil {
		return nil, fmt.Errorf("status: %v", err)
	}
	var st ogateway.Status
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return nil, err
	}
	for _, n := range st.Networks {
		if n.VRF == vrf && n.Routes != nil {
			return n.Routes, nil
		}
	}
	return nil, fmt.Errorf("no routes for %s", vrf)
}

// status --routes lists, per tenant VRF, the own partition's prefixes (the
// machine's host route from EVPN, the exported aggregate), the remote
// partitions' aggregates with their SIDs, and the L3VNI's type-5 routes:
// the host route received from the fabric, the remote aggregates advertised
// into it.
func TestStatusRoutes(t *testing.T) {
	for _, m := range machines {
		var remotes []machine
		for _, f := range flows {
			if f[0].name == m.name {
				remotes = append(remotes, f[1])
			}
		}
		for _, gw := range m.gws {
			t.Run(gw+"/"+m.vrf, func(t *testing.T) {
				t.Parallel()
				waitFor(t, converge, func() error {
					r, err := statusRoutes(gw, m.vrf)
					if err != nil {
						return err
					}
					if !slices.ContainsFunc(r.Local, func(l ogateway.LocalRoute) bool {
						return l.Prefix == m.v4+"/32" && l.NextHop != "" && !l.Exported && l.Installed
					}) {
						return fmt.Errorf("local: no installed, unexported host route %s/32 via a VTEP: %+v", m.v4, r.Local)
					}
					if !slices.ContainsFunc(r.Local, func(l ogateway.LocalRoute) bool {
						return l.Prefix == m.net4 && l.Aggregate && l.Exported
					}) {
						return fmt.Errorf("local: aggregate %s not exported: %+v", m.net4, r.Local)
					}
					if !slices.ContainsFunc(r.EVPN, func(e ogateway.EVPNRoute) bool { return !e.Advertised && e.Prefix == m.v4+"/32" }) {
						return fmt.Errorf("evpn: %s/32 not received: %+v", m.v4, r.EVPN)
					}
					for _, o := range remotes {
						if !slices.ContainsFunc(r.Remote, func(rm ogateway.RemoteRoute) bool {
							return rm.Prefix == o.net4 && rm.Installed && slices.Contains(rm.SIDs, o.sid)
						}) {
							return fmt.Errorf("remote: %s not installed via SID %s: %+v", o.net4, o.sid, r.Remote)
						}
						if !slices.ContainsFunc(r.EVPN, func(e ogateway.EVPNRoute) bool { return e.Advertised && e.Prefix == o.net4 }) {
							return fmt.Errorf("evpn: %s not advertised: %+v", o.net4, r.EVPN)
						}
					}
					return nil
				})
			})
		}
	}
}
