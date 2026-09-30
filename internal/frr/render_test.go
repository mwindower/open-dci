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
	gwA = Identity{ASN: 4200000016, RouterID: "10.0.0.16"} // transport in a DCI network
	gwB = Identity{ASN: 4200000026, RouterID: "10.0.1.16"} // transport in the default VRF
)

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
	for node, id := range map[string]Identity{"gw-a": gwA, "gw-b": gwB} {
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
	got := renderLab(t, "gw-b", gwB)
	for _, s := range []string{"address-family ipv6 unicast\n  network fd00:dc1:b::/48", "ipv6 route fd00:dc1:b::/48 blackhole"} {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q", s)
		}
	}
	for _, s := range []string{"dci0", "dci1", "redistribute static", "\n ipv6 route"} {
		if strings.Contains(got, s) {
			t.Errorf("default-VRF transport must not contain %q", s)
		}
	}
}

// Every rendered line must appear verbatim in FRR's running-config (captured
// from the lab), otherwise drift detection would re-apply forever.
func TestRenderMatchesRunningConfig(t *testing.T) {
	for node, id := range map[string]Identity{"gw-a": gwA} {
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
	cfg, err := config.Load("../../lab/configs/gw-a/open-dci.yaml")
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
	running, err := os.ReadFile("testdata/gw-a.running.conf")
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
		"router bgp 4200000016 vrf vrf3982\n bgp router-id 10.0.0.16\n sid vpn per-vrf export auto\n",
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
