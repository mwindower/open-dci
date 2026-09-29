package frr

import (
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/mwindower/srv6-dci/internal/config"
)

var update = flag.Bool("update", false, "rewrite golden files")

var (
	fwA = Identity{ASN: 4200000012, RouterID: "10.0.0.12"} // transport in a DCI network
	fwB = Identity{ASN: 4200000022, RouterID: "10.0.1.12"} // transport in the default VRF
)

func renderLab(t *testing.T, node string, id Identity) string {
	t.Helper()
	cfg, err := config.Load("../../lab/configs/" + node + "/srv6-dci.yaml")
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
	for node, id := range map[string]Identity{"fw-a": fwA, "fw-b": fwB} {
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
	got := renderLab(t, "fw-b", fwB)
	for _, s := range []string{"address-family ipv6 unicast\n  network fd00:dc1:b::/48", "ipv6 route fd00:dc1:b::/48 blackhole"} {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q", s)
		}
	}
	for _, s := range []string{"dci0", "dci1", "redistribute static", "\nvrf "} {
		if strings.Contains(got, s) {
			t.Errorf("default-VRF transport must not contain %q", s)
		}
	}
}

// Every rendered line must appear verbatim in FRR's running-config (captured
// from the lab), otherwise drift detection would re-apply forever.
func TestRenderMatchesRunningConfig(t *testing.T) {
	running, err := os.ReadFile("testdata/fw-a.running.conf")
	if err != nil {
		t.Fatal(err)
	}
	missing := Missing(Parse(renderLab(t, "fw-a", fwA)), Parse(string(running)))
	for _, l := range missing {
		t.Errorf("not in running-config: %s > %s", strings.Join(l.Context, " > "), l.Text)
	}
}

func TestRenderNeedsIdentity(t *testing.T) {
	cfg, err := config.Load("../../lab/configs/fw-a/srv6-dci.yaml")
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
	running, err := os.ReadFile("testdata/fw-a.running.conf")
	if err != nil {
		t.Fatal(err)
	}
	b, err := DiscoverBase(string(running))
	if err != nil {
		t.Fatal(err)
	}
	if b.ASN != 4200000012 || b.RouterID != "10.0.0.12" {
		t.Fatalf("got %+v", b.Identity)
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
