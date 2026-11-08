package kernel

import (
	"net/netip"
	"testing"
)

func TestDropRuleString(t *testing.T) {
	r := DropRule{IifPrefix: "dcibr", SaddrNot: []netip.Prefix{netip.MustParsePrefix("fd00:dc1::/32")}, Daddr: netip.MustParsePrefix("fd00:dc1:a::/48")}
	if got, want := r.String(), `iifname "dcibr*" ip6 saddr != fd00:dc1::/32 ip6 daddr fd00:dc1:a::/48 drop`; got != want {
		t.Fatalf("got %s", got)
	}
	if m := maskBytes(33); m[3] != 0xff || m[4] != 0x80 || m[5] != 0 {
		t.Fatalf("mask %x", m)
	}
	v4 := DropRule{ExceptIif: []string{"lo"}, Daddr: netip.MustParsePrefix("10.0.1.16/32"), UDPDport: 4789,
		SaddrNot: []netip.Prefix{netip.MustParsePrefix("10.0.1.0/24"), netip.MustParsePrefix("10.0.2.0/24")}}
	if got, want := v4.String(), `iifname != "lo" ip saddr != 10.0.1.0/24 ip saddr != 10.0.2.0/24 ip daddr 10.0.1.16/32 udp dport 4789 drop`; got != want || !v4.IPv4() || r.IPv4() {
		t.Fatalf("got %s", got)
	}
	if e := v4.exprs(); len(e) != 2+3*3+4+2 {
		t.Fatalf("%d expressions", len(e))
	}
	if filterDigest([]DropRule{r}) == filterDigest([]DropRule{r, r}) {
		t.Fatal("digest must change with the rules")
	}
}
