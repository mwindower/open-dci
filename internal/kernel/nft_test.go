package kernel

import (
	"net/netip"
	"testing"
)

func TestDropRuleString(t *testing.T) {
	r := DropRule{IifPrefix: "dcibr", SaddrNot: netip.MustParsePrefix("fd00:dc1::/32"), Daddr: netip.MustParsePrefix("fd00:dc1:a::/48")}
	if got, want := r.String(), `iifname "dcibr*" ip6 saddr != fd00:dc1::/32 ip6 daddr fd00:dc1:a::/48 drop`; got != want {
		t.Fatalf("got %s", got)
	}
	if m := maskBytes(33); m[3] != 0xff || m[4] != 0x80 || m[5] != 0 {
		t.Fatalf("mask %x", m)
	}
	if filterDigest([]DropRule{r}) == filterDigest([]DropRule{r, r}) {
		t.Fatal("digest must change with the rules")
	}
}
