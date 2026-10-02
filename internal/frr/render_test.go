package frr

import (
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/mwindower/open-dci/internal/config"
)

var update = flag.Bool("update", false, "rewrite golden files")

var (
	// each pair's own ranges have hosts (active aggregates), the others' don't
	aggA = active("vrf3981 10.0.16.0/24", "vrf3981 2001:db8:16::/48", "vrf3982 10.0.17.0/24", "vrf3982 2001:db8:17::/48")
	gwA1 = Identity{ASN: 4200000016, RouterID: "10.0.0.16", Aggregates: aggA} // transport in a DCI network
	gwA2 = Identity{ASN: 4200000016, RouterID: "10.0.0.17", Aggregates: aggA} // gw-a1's redundant partner
	gwB1 = Identity{ASN: 4200000026, RouterID: "10.0.1.16",                   // transport in the default VRF
		Aggregates: active("vrf4011 10.0.32.0/24", "vrf4011 2001:db8:32::/48", "vrf4012 10.0.33.0/24", "vrf4012 2001:db8:33::/48")}
)

func active(keys ...string) map[string]bool {
	m := map[string]bool{}
	for _, k := range keys {
		m[k] = true
	}
	return m
}

func renderLab(t *testing.T, node string, id Identity) string {
	t.Helper()
	cfg, err := config.Load("../../lab/configs/" + node + "/open-dci.yaml")
	if err != nil {
		t.Fatal(err)
	}
	out, err := Render(cfg, id)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRenderGolden(t *testing.T) {
	for node, id := range map[string]Identity{"gw-a1": gwA1, "gw-b1": gwB1} {
		t.Run(node, func(t *testing.T) {
			got := renderLab(t, node, id)
			golden := "testdata/" + node + ".golden"
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Fatalf("rendered config differs from %s (run go test ./internal/frr -update):\n%s", golden, got)
			}
		})
	}
}

// Transport in the default VRF: no veth, no transport VRF instance, but the
// locator announced by the default BGP instance.
func TestRenderDefaultVRFTransport(t *testing.T) {
	got := renderLab(t, "gw-b1", gwB1)
	for _, s := range []string{"address-family ipv6 unicast\n  network fd00:dc1:b::/48\n  network fd00:dc1:ff::b1/128\n", "ipv6 route fd00:dc1:b::/48 blackhole"} {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q", s)
		}
	}
	for _, s := range []string{"dci0", "dci1", "redistribute static", "\n ipv6 route fd00:"} { // routes into the block in a VRF
		if strings.Contains(got, s) {
			t.Errorf("default-VRF transport must not contain %q", s)
		}
	}
}

// Every rendered line must appear verbatim in FRR's running-config (captured
// from the lab), otherwise drift detection would re-apply forever.
func TestRenderMatchesRunningConfig(t *testing.T) {
	for node, id := range map[string]Identity{"gw-a1": gwA1} {
		running, err := os.ReadFile("testdata/" + node + ".running.conf")
		if err != nil {
			t.Fatal(err)
		}
		missing := Missing(Parse(renderLab(t, node, id)), Parse(string(running)))
		for _, l := range missing {
			t.Errorf("%s: not in running-config: %s > %s", node, strings.Join(l.Context, " > "), l.Text)
		}
	}
}

func TestRenderNeedsIdentity(t *testing.T) {
	cfg, err := config.Load("../../lab/configs/gw-a1/open-dci.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Render(cfg, Identity{}); err == nil {
		t.Fatal("expected error without ASN/router-id")
	}
}

func TestParseContexts(t *testing.T) {
	lines := Parse(`
router bgp 1
 neighbor x remote-as 2
 !
 address-family ipv4 vpn
  neighbor x activate
 exit-address-family
exit
!
ipv6 route ::/0 blackhole
`)
	want := []struct {
		ctx, text string
		leaf      bool
	}{
		{"", "router bgp 1", false},
		{"router bgp 1", "neighbor x remote-as 2", true},
		{"router bgp 1", "address-family ipv4 vpn", false},
		{"router bgp 1 > address-family ipv4 vpn", "neighbor x activate", true},
		{"", "ipv6 route ::/0 blackhole", true},
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines: %+v", len(lines), lines)
	}
	for i, w := range want {
		l := lines[i]
		if strings.Join(l.Context, " > ") != w.ctx || l.Text != w.text || l.Leaf != w.leaf {
			t.Errorf("line %d: got %+v, want %+v", i, l, w)
		}
	}
}

func TestRemovals(t *testing.T) {
	prev := Parse(`router bgp 1
 neighbor a remote-as 2
 neighbor a ebgp-multihop 16
 neighbor b remote-as 3
 address-family ipv4 unicast
  no neighbor a activate
 exit-address-family
exit
router bgp 1 vrf t1
 sid vpn per-vrf export auto
exit
`)
	want := Parse(`router bgp 1
 neighbor b remote-as 3
exit
`)
	got := Removals(prev, want)
	for _, s := range []string{" no neighbor a remote-as 2\n", "router bgp 1 vrf t1\n no sid vpn per-vrf export auto\n"} {
		if !strings.Contains(got, s) {
			t.Errorf("removals lack %q:\n%s", s, got)
		}
	}
	// neighbor a is deleted as a whole; its other lines must not be touched
	// (they would fail), and headers are never removed
	for _, s := range []string{"ebgp-multihop", "neighbor a activate", "no router bgp"} {
		if strings.Contains(got, s) {
			t.Errorf("removals must not contain %q:\n%s", s, got)
		}
	}
}

func TestDiscoverBase(t *testing.T) {
	running, err := os.ReadFile("testdata/gw-a1.running.conf")
	if err != nil {
		t.Fatal(err)
	}
	b, err := DiscoverBase(string(running))
	if err != nil {
		t.Fatal(err)
	}
	if b.ASN != 4200000016 || b.RouterID != "10.0.0.16" || !b.AdvertiseAllVNI {
		t.Fatalf("got %+v", b)
	}
	for _, vrf := range []string{"vrf3981", "vrf104100"} {
		if !b.VRFInstances[vrf] {
			t.Errorf("vrf instance %s not discovered", vrf)
		}
	}
	if _, err := DiscoverBase("hostname x\n"); err == nil {
		t.Fatal("expected error without router bgp")
	}
}

// Every network gets the FRR VRF with the L3VNI and a full BGP instance with
// EVPN type-5 advertisement.
func TestRenderProvisioned(t *testing.T) {
	cfg, err := config.Parse([]byte(`
gateway: {locator: "fd00:dc1:a2::/48", locatorBlock: "fd00:dc1::/32"}
peers: [{address: "fd00:dc1:b2::1", asn: 4200000026}]
networks:
  - {vrf: vrf3982, vni: 3982, routeTarget: "65535:1002", prefixes: ["10.0.17.0/24 le 32"]}
  - {vrf: vrf3983, vni: 3983, routeTarget: "65535:1003", prefixes: ["2001:db8:17::/48 le 128"]}
`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Render(cfg, Identity{ASN: 4200000016, RouterID: "10.0.0.16"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "vrf vrf3982\n vni 3982\nexit-vrf\n!\nvrf vrf3983\n vni 3983\nexit-vrf\n!\nsegment-routing\n") {
		t.Errorf("the vrf blocks must come first:\n%s", got)
	}
	for _, s := range []string{
		"router bgp 4200000016 vrf vrf3982\n bgp router-id 10.0.0.16\n sid vpn per-vrf export 3982\n",
		" address-family l2vpn evpn\n  advertise ipv4 unicast\n  advertise ipv6 unicast\n exit-address-family\nexit\n!\nrouter bgp 4200000016 vrf vrf3983\n bgp router-id 10.0.0.16\n sid vpn",
	} {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q in:\n%s", s, got)
		}
	}
	if strings.Count(got, "advertise ipv4 unicast") != 2 {
		t.Errorf("every vrf advertises its routes as type-5:\n%s", got)
	}
}

// A provisioned VRF that is no longer wanted is removed as a whole, L3VNI
// before BGP instance; blocks of the base config (e.g. the transport VRF's)
// keep their headers.
func TestRemovalsProvisioned(t *testing.T) {
	prev := Parse(`vrf vrf3982
 vni 3982
exit-vrf
vrf vrf3983
 vni 3983
exit-vrf
router bgp 1 vrf vrf3982
 bgp router-id 10.0.0.1
 address-family l2vpn evpn
  advertise ipv4 unicast
 exit-address-family
exit
router bgp 1 vrf vrf3983
 bgp router-id 10.0.0.1
exit
router bgp 1 vrf t1
 sid vpn per-vrf export auto
exit
`)
	want := Parse(`vrf vrf3983
 vni 3983
exit-vrf
router bgp 1 vrf vrf3983
 bgp router-id 10.0.0.1
exit
`)
	if got := L3VNIRemovals(prev, want); got != "vrf vrf3982\n no vni 3982\nexit-vrf\n" {
		t.Errorf("L3VNI removals: %q", got)
	}
	got := Removals(prev, want)
	if !strings.HasPrefix(got, "no router bgp 1 vrf vrf3982\n") {
		t.Errorf("the provisioned vrf's BGP instance must be removed as a whole:\n%s", got)
	}
	for _, s := range []string{"advertise", "bgp router-id", "3983", "vni"} {
		if strings.Contains(got, s) {
			t.Errorf("removals must not contain %q:\n%s", s, got)
		}
	}
	if !strings.Contains(got, "router bgp 1 vrf t1\n no sid vpn per-vrf export auto\n") || strings.Contains(got, "no router bgp 1 vrf t1") {
		t.Errorf("base-config vrf: only its leaves are removed:\n%s", got)
	}
}

func TestDiscoverAdvertiseAllVNI(t *testing.T) {
	b, err := DiscoverBase("router bgp 1\n address-family l2vpn evpn\n  advertise-all-vni\n exit-address-family\nexit\n")
	if err != nil || !b.AdvertiseAllVNI {
		t.Fatalf("advertise-all-vni not discovered: %+v %v", b, err)
	}
	b, _ = DiscoverBase("router bgp 1\nexit\nrouter bgp 1 vrf x\n address-family l2vpn evpn\n  advertise-all-vni\n exit-address-family\nexit\n")
	if b.AdvertiseAllVNI {
		t.Fatal("advertise-all-vni of a vrf instance must not count")
	}
}

// Only allowlisted prefixes are exported and imported; a family without
// prefixes is denied completely; peers only deliver configured RTs.
func TestRenderFilters(t *testing.T) {
	cfg, err := config.Parse([]byte(`
gateway: {locator: "fd00:dc1:a::/48", locatorBlock: "fd00:dc1::/32"}
peers: [{address: "fd00:dc1:b::1", asn: 2, maxPrefixes: 500}]
networks:
  - {vrf: t1, vni: 1, routeTarget: "65535:1", prefixes: ["10.0.16.0/24 le 32", "10.0.32.0/24 le 32"]}
  - {vrf: t2, vni: 2, routeTarget: "65535:1", prefixes: ["2001:db8::/32 ge 48 le 64"]}
`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Render(cfg, Identity{ASN: 1, RouterID: "10.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"ip prefix-list DCI-t1-v4 seq 5 permit 10.0.16.0/24 le 32\nip prefix-list DCI-t1-v4 seq 10 permit 10.0.32.0/24 le 32\nroute-map DCI-t1-v4 permit 10\n match ip address prefix-list DCI-t1-v4\nexit\n",
		"route-map DCI-t1-v6 deny 10\nexit\n",
		"ipv6 prefix-list DCI-t2-v6 seq 5 permit 2001:db8::/32 ge 48 le 64\nroute-map DCI-t2-v6 permit 10\n match ipv6 address prefix-list DCI-t2-v6\nexit\n",
		"route-map DCI-t2-v4 deny 10\nexit\n",
		" address-family ipv4 unicast\n  rd vpn export 10.0.0.1:1\n  rt vpn both 65535:1\n  route-map vpn import DCI-t1-v4\n  route-map vpn export DCI-t1-v4\n",
		"  neighbor fd00:dc1:b::1 activate\n  neighbor fd00:dc1:b::1 route-map DCI-PEER-IN in\n  neighbor fd00:dc1:b::1 maximum-prefix 500\n",
		"bgp extcommunity-list standard DCI-RT seq 5 permit rt 65535:1\nroute-map DCI-PEER-IN permit 10\n match extcommunity DCI-RT\nexit\n",
	} {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q in:\n%s", s, got)
		}
	}
	if strings.Count(got, "maximum-prefix 500") != 2 || strings.Contains(got, "seq 10 permit rt") {
		t.Errorf("max-prefix per VPN family, and each RT once:\n%s", got)
	}
}

// Active aggregates are announced from a blackhole in the tenant VRF, inactive
// ones not at all; both are matched exactly by the allowlist. One that becomes
// inactive is removed on its own.
func TestRenderAggregates(t *testing.T) {
	cfg, err := config.Parse([]byte(`
gateway: {locator: "fd00:dc1:a2::/48", locatorBlock: "fd00:dc1::/32"}
peers: [{address: "fd00:dc1:b2::1", asn: 4200000026}]
networks:
  - {vrf: t1, vni: 1, routeTarget: "65535:1", aggregates: [10.0.16.0/24, 10.0.32.0/24, "2001:db8:16::/48"], prefixes: ["10.99.0.0/16 le 32"]}
`))
	if err != nil {
		t.Fatal(err)
	}
	id := Identity{ASN: 4200000016, RouterID: "10.0.0.16", Aggregates: active("t1 10.0.16.0/24", "t1 2001:db8:16::/48")}
	got, err := Render(cfg, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"vrf t1\n vni 1\n ip route 10.0.16.0/24 blackhole\n ipv6 route 2001:db8:16::/48 blackhole\nexit-vrf\n",
		" address-family ipv4 unicast\n  network 10.0.16.0/24 route-map DCI-AGG\n  rd vpn export",
		" address-family ipv6 unicast\n  network 2001:db8:16::/48 route-map DCI-AGG\n  rd vpn export",
		"ip prefix-list DCI-t1-v4 seq 5 permit 10.0.16.0/24\nip prefix-list DCI-t1-v4 seq 10 permit 10.0.32.0/24\nip prefix-list DCI-t1-v4 seq 15 permit 10.99.0.0/16 le 32\n",
		"ipv6 prefix-list DCI-t1-v6 seq 5 permit 2001:db8:16::/48\n",
		// the own aggregates stay out of the own partition's type-5 ...
		"  advertise ipv4 unicast route-map DCI-ADV\n  advertise ipv6 unicast route-map DCI-ADV\n",
		"bgp large-community-list standard DCI-AGG seq 5 permit 4200000016:0:1\nroute-map DCI-AGG permit 10\n set large-community 4200000016:0:1\nexit\n",
		"route-map DCI-ADV deny 10\n match large-community DCI-AGG\nexit\n!\nroute-map DCI-ADV permit 20\nexit\n",
		// ... but the mark doesn't leave the gateway
		"route-map DCI-t1-v4 permit 10\n match ip address prefix-list DCI-t1-v4\n set large-comm-list DCI-AGG delete\nexit\n",
	} {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q in:\n%s", s, got)
		}
	}
	if strings.Contains(got, "10.0.32.0/24 blackhole") || strings.Contains(got, "network 10.0.32.0/24") {
		t.Errorf("the inactive aggregate is announced:\n%s", got)
	}

	// the last host of 10.0.16.0/24 is gone
	id.Aggregates = active("t1 2001:db8:16::/48")
	want, err := Render(cfg, id)
	if err != nil {
		t.Fatal(err)
	}
	rm := Removals(Parse(got), Parse(want))
	for _, s := range []string{
		"vrf t1\n no ip route 10.0.16.0/24 blackhole\n",
		"router bgp 4200000016 vrf t1\n address-family ipv4 unicast\n  no network 10.0.16.0/24 route-map DCI-AGG\n",
	} {
		if !strings.Contains(rm, s) {
			t.Errorf("removals lack %q:\n%s", s, rm)
		}
	}
	if strings.Contains(rm, "2001:db8:16::/48") || strings.Contains(rm, "prefix-list") {
		t.Errorf("removals touch the active aggregate or the allowlist:\n%s", rm)
	}

	// without aggregates, nothing of the marking is rendered
	cfg.Networks[0].Aggregates = nil
	plain, err := Render(cfg, Identity{ASN: 4200000016, RouterID: "10.0.0.16"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"route 10.0.16.0/24 blackhole", "DCI-AGG", "DCI-ADV"} {
		if strings.Contains(plain, s) {
			t.Errorf("rendered %q without aggregates:\n%s", s, plain)
		}
	}
}

// Lines that are gone already (the base system reloaded FRR) aren't removed
// again; a provisioned VRF's vni line still decides whether it's dropped.
func TestPresent(t *testing.T) {
	prev := Parse("vrf t1\n vni 1\n ip route 10.0.16.0/24 blackhole\nexit-vrf\nrouter bgp 1 vrf t1\n address-family ipv4 unicast\n  network 10.0.16.0/24 route-map DCI-AGG\n exit-address-family\nexit\n")
	have := Parse("router bgp 1 vrf t1\n address-family ipv4 unicast\n  network 10.0.16.0/24 route-map DCI-AGG\n exit-address-family\nexit\n")
	rm := Removals(Present(prev, have), nil)
	if strings.Contains(rm, "no ip route") {
		t.Errorf("removes a route that is gone:\n%s", rm)
	}
	if !strings.Contains(rm, "no router bgp 1 vrf t1") {
		t.Errorf("the dropped VRF must still go:\n%s", rm)
	}
	rm = Removals(Present(prev, have), Parse("vrf t1\n vni 1\nexit-vrf\n"))
	if !strings.Contains(rm, "no network 10.0.16.0/24 route-map DCI-AGG") || strings.Contains(rm, "no ip route") {
		t.Errorf("want only the present network removed:\n%s", rm)
	}
}

// Filters of a dropped network go completely (route-maps as a whole, list
// entries one by one); a changed prefix only swaps its entry.
func TestRemovalsFilters(t *testing.T) {
	prev := Parse(`ip prefix-list DCI-t1-v4 seq 5 permit 10.0.16.0/24 le 32
route-map DCI-t1-v4 permit 10
 match ip address prefix-list DCI-t1-v4
exit
route-map DCI-t1-v6 deny 10
exit
ip prefix-list DCI-t2-v4 seq 5 permit 10.0.17.0/24 le 32
route-map DCI-t2-v4 permit 10
 match ip address prefix-list DCI-t2-v4
exit
route-map BASE permit 10
 match ip address prefix-list X
exit
`)
	want := Parse(`ip prefix-list DCI-t2-v4 seq 5 permit 10.0.18.0/24 le 32
route-map DCI-t2-v4 permit 10
 match ip address prefix-list DCI-t2-v4
exit
`)
	got := Removals(prev, want)
	for _, s := range []string{
		"no ip prefix-list DCI-t1-v4 seq 5 permit 10.0.16.0/24 le 32\n",
		"no ip prefix-list DCI-t2-v4 seq 5 permit 10.0.17.0/24 le 32\n",
		"no route-map DCI-t1-v4\n",
		"no route-map DCI-t1-v6\n",
		"route-map BASE permit 10\n no match ip address prefix-list X\n",
	} {
		if !strings.Contains(got, s) {
			t.Errorf("removals lack %q:\n%s", s, got)
		}
	}
	for _, s := range []string{"no route-map DCI-t2-v4", "no route-map BASE", "no match ip address prefix-list DCI-t1-v4", "no route-map DCI-t1-v6 deny 10"} {
		if strings.Contains(got, s) {
			t.Errorf("removals must not contain %q:\n%s", s, got)
		}
	}
}

// The two gateways of a redundant pair share the locator and announce the
// same pinned SIDs, but use their own loopback for sessions and encap.
func TestRenderAnycastPair(t *testing.T) {
	a1, a2 := renderLab(t, "gw-a1", gwA1), renderLab(t, "gw-a2", gwA2)
	for _, s := range []string{
		"prefix fd00:dc1:a::/48 block-len 32 node-len 16",
		"sid vpn per-vrf export 3981\n", "sid vpn per-vrf export 3982\n",
		"ipv6 route fd00:dc1:a::/48 fe80::1 dci1\n",
	} {
		if !strings.Contains(a1, s) || !strings.Contains(a2, s) {
			t.Errorf("both gateways must render %q", s)
		}
	}
	for gw, lo := range map[string]string{a1: "fd00:dc1:ff::a1", a2: "fd00:dc1:ff::a2"} {
		for _, s := range []string{"source-address " + lo + "\n", " ipv6 route " + lo + "/128 fe80::1 dci1\n"} {
			if !strings.Contains(gw, s) {
				t.Errorf("missing %q", s)
			}
		}
	}
}

// A gateway that isn't ready withholds its locator and loopback: in the
// default VRF the network statements, in a DCI network the redistribution
// into the transport VRF. Going from announced to withheld removes exactly
// these lines and never the base config's block headers.
func TestRenderWithhold(t *testing.T) {
	for _, c := range []struct {
		node    string
		id      Identity
		gone    []string
		removal string
	}{
		{"gw-b1", gwB1, []string{"network fd00:dc1:b::/48", "network fd00:dc1:ff::b1/128"},
			"router bgp 4200000026\n address-family ipv6 unicast\n  no network fd00:dc1:b::/48\n"},
		{"gw-a1", gwA1, []string{"redistribute static"},
			"router bgp 4200000016 vrf vrf104100\n address-family ipv6 unicast\n  no redistribute static\n"},
	} {
		t.Run(c.node, func(t *testing.T) {
			announced := renderLab(t, c.node, c.id)
			id := c.id
			id.Withhold = true
			withheld := renderLab(t, c.node, id)
			for _, s := range c.gone {
				if !strings.Contains(announced, s) || strings.Contains(withheld, s) {
					t.Errorf("%q must be rendered only when announcing", s)
				}
			}
			// everything else stays: SIDs, VRFs, the blackhole route
			for _, s := range []string{"sid vpn per-vrf export", "blackhole", "locator DCI"} {
				if !strings.Contains(withheld, s) {
					t.Errorf("withheld config lacks %q", s)
				}
			}
			got := Removals(Parse(announced), Parse(withheld))
			if !strings.Contains(got, c.removal) {
				t.Errorf("removals lack %q:\n%s", c.removal, got)
			}
			if strings.Contains(got, "no router bgp") || strings.Contains(got, "no address-family") {
				t.Errorf("removals must not remove block headers:\n%s", got)
			}
		})
	}
}

// A drained gateway also stops announcing its tenant VRFs' routes into the
// fabric; undraining brings back exactly these lines.
func TestRenderDrain(t *testing.T) {
	announced := renderLab(t, "gw-b1", gwB1)
	id := gwB1
	id.Drain = true
	drained := renderLab(t, "gw-b1", id)
	for _, s := range []string{"advertise ipv4 unicast", "advertise ipv6 unicast", "network fd00:dc1:b::/48"} {
		if strings.Contains(drained, s) {
			t.Errorf("drained config still contains %q", s)
		}
	}
	if !strings.Contains(drained, "sid vpn per-vrf export") || !strings.Contains(drained, "import vpn") {
		t.Error("draining must keep the VRFs and their VPN import")
	}
	got := Removals(Parse(announced), Parse(drained))
	for _, s := range []string{"router bgp 4200000026 vrf vrf4011\n address-family l2vpn evpn\n  no advertise ipv4 unicast route-map DCI-ADV\n", "no network fd00:dc1:b::/48"} {
		if !strings.Contains(got, s) {
			t.Errorf("removals lack %q:\n%s", s, got)
		}
	}
	if strings.Contains(got, "no address-family") || strings.Contains(got, "no router bgp") {
		t.Errorf("removals must not remove block headers:\n%s", got)
	}
}
