package gateway

import (
	"strings"
	"testing"

	"github.com/mwindower/open-dci/internal/config"
)

func TestFilterRules(t *testing.T) {
	cfg, err := config.Load("../../lab/configs/gw-a1/open-dci.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range FilterRules(cfg) {
		got = append(got, r.Name+": "+r.String())
	}
	want := []string{
		`tenant-to-transport: iifname "dcibr*" ip6 daddr fd00:dc1::/32 drop`,
		`locator-from-outside: iifname != "lo" ip6 saddr != fd00:dc1::/32 ip6 saddr != fe80::/10 ip6 daddr fd00:dc1:a::/48 drop`,
		`own-locator-spoofed: iifname != "lo" iifname != "dci1" iifname != "vrf104100" ip6 saddr fd00:dc1:a::/48 drop`,
		`loopback-from-outside: iifname != "lo" ip6 saddr != fd00:dc1::/32 ip6 saddr != fe80::/10 ip6 daddr fd00:dc1:ff::a1/128 drop`,
		`own-loopback-spoofed: iifname != "lo" iifname != "dci1" iifname != "vrf104100" ip6 saddr fd00:dc1:ff::a1/128 drop`,
		`vxlan-from-outside: iifname != "lo" ip saddr != 10.0.0.0/24 udp dport 4789 drop`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
}

// In default-VRF mode, the own packets never come back in on an interface:
// only lo is exempt.
func TestFilterRulesDefaultVRF(t *testing.T) {
	cfg, err := config.Load("../../lab/configs/gw-b1/open-dci.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range FilterRules(cfg) {
		if r.Name == "own-loopback-spoofed" {
			if got, want := r.String(), `iifname != "lo" ip6 saddr fd00:dc1:ff::b1/128 drop`; got != want {
				t.Fatalf("got %s", got)
			}
			return
		}
	}
	t.Fatal("no own-loopback-spoofed rule")
}
