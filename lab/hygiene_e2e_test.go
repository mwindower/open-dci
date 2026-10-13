//go:build e2e

package lab

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// What the gateways announce into their fabric is clean: the type-5 routes
// of remote prefixes carry the partition's L3VNI route target, the gateway's
// router MAC and the partition's site of origin, but none of the extended
// communities the routes brought from the VPN. EVPN imports auto route
// targets by their local part (the VNI), so a leaked DCI RT 65535:N would
// land in any VRF with VNI N.

// partition is a fabric with its leaf and the site of origin of its gateway
// pair (default <asn>:<locator node>).
type partition struct {
	leaf, soo string
	tenants   []machine
}

var partitions = []partition{
	{"leaf-a", "4200000016:10", []machine{mA, mA2}},
	{"leaf-b", "4200000026:11", []machine{mB, mB2}},
	{"leaf-c", "4200000036:12", []machine{mC, mC2}},
}

// type5Communities returns the extended communities of every path a node has
// for a type-5 prefix ("10.0.32.0/24").
func type5Communities(node, prefix string) ([]string, error) {
	addr, plen, _ := strings.Cut(prefix, "/")
	var top map[string]json.RawMessage
	if err := lab.VtyshJSON(node, "show bgp l2vpn evpn route type prefix", &top); err != nil {
		return nil, err
	}
	var out []string
	for _, raw := range top {
		var block map[string]json.RawMessage
		if json.Unmarshal(raw, &block) != nil {
			continue
		}
		var r struct {
			Paths [][]struct {
				Ext struct {
					String string `json:"string"`
				} `json:"extendedCommunity"`
			} `json:"paths"`
		}
		if json.Unmarshal(block["[5]:[0]:["+plen+"]:["+addr+"]"], &r) != nil {
			continue
		}
		for _, ps := range r.Paths {
			for _, p := range ps {
				out = append(out, p.Ext.String)
			}
		}
	}
	return out, nil
}

func TestType5Hygiene(t *testing.T) {
	for _, p := range partitions {
		for _, local := range p.tenants {
			for _, f := range flows {
				if f[0].name != local.name {
					continue
				}
				remote := f[1]
				t.Run(p.leaf+"/"+remote.net4, func(t *testing.T) {
					waitFor(t, converge, func() error {
						comms, err := type5Communities(p.leaf, remote.net4)
						if err != nil {
							return err
						}
						if len(comms) == 0 {
							return fmt.Errorf("%s: no type-5 route for %s", p.leaf, remote.net4)
						}
						for _, c := range comms {
							for _, ec := range strings.Fields(c) {
								rt, isRT := strings.CutPrefix(ec, "RT:")
								switch {
								case strings.HasPrefix(ec, "RT:65535:"):
									return fmt.Errorf("%s: DCI route target in %s: %s", p.leaf, remote.net4, c)
								case isRT && !strings.HasSuffix(rt, ":"+local.vni):
									return fmt.Errorf("%s: foreign route target in %s: %s", p.leaf, remote.net4, c)
								}
							}
							if !strings.Contains(" "+c+" ", " SoO:"+p.soo+" ") {
								return fmt.Errorf("%s: %s lacks SoO:%s: %s", p.leaf, remote.net4, p.soo, c)
							}
						}
						return nil
					})
				})
			}
		}
	}
}

// A route carrying the partition's site of origin never leaves for the VPN
// (e.g. a remote prefix that came back from the own fabric). gw-a1 withdraws
// its own aggregate and announces it again with the mark, set by hand (its
// sidecar paused, it would revert that): the exits never get gw-a1's path,
// gw-a2's carries the traffic. Without the mark, the path comes back.
//
// The route must be new: FRR (10.4.1) doesn't withdraw a VPN copy it exported
// before when an attribute change makes the export filter deny the route.
// That's no gap here: a route coming back from the own fabric was never
// exported (FRR exports no route imported from the VPN).
func TestSiteOfOriginNotExported(t *testing.T) {
	const gw, rd = "gw-a1", "ipv4 vpn rd 10.0.0.16:3981"
	if _, err := lab.Exec(gw, "pkill", "-STOP", "-f", "open-dci run"); err != nil {
		t.Fatal(err)
	}
	defer lab.Exec(gw, "pkill", "-CONT", "-f", "open-dci run")
	network := func(on bool) error {
		cmd := "network " + mA.net4 + " route-map DCI-AGG"
		if !on {
			cmd = "no " + cmd
		}
		return vtyshConf(gw, "router bgp 4200000016 vrf "+mA.vrf, "address-family ipv4 unicast", cmd)
	}
	mark := func(on bool) error {
		cmd := "set extcommunity soo 4200000016:10"
		if !on {
			cmd = "no " + cmd
		}
		return vtyshConf(gw, "route-map DCI-AGG permit 10", cmd)
	}
	defer network(true)
	defer mark(false)
	waitFor(t, converge, present("exit-b1", rd, mA.net4))
	if err := network(false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, converge, absent("exit-b1", rd, mA.net4))
	if err := mark(true); err != nil {
		t.Fatal(err)
	}
	if err := network(true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, converge, func() error { // announced in the VRF, with the mark ...
		out, err := lab.Vtysh(gw, "show bgp vrf "+mA.vrf+" ipv4 unicast "+mA.net4)
		if err != nil || !strings.Contains(out, "SoO:4200000016:10") {
			return fmt.Errorf("%s: %s not marked: %v\n%s", gw, mA.net4, err, out)
		}
		return nil
	})
	time.Sleep(10 * time.Second) // ... and not exported (route-map delay timer: ~5 s)
	if err := absent("exit-b1", rd, mA.net4)(); err != nil {
		t.Error(err)
	}
	waitFor(t, converge, present("exit-b1", "ipv4 vpn rd 10.0.0.17:3981", mA.net4))
	waitFor(t, converge, func() error { return lab.Ping("m-b", mA.v4, 0) })

	if err := mark(false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, converge, present("exit-b1", rd, mA.net4))
}
