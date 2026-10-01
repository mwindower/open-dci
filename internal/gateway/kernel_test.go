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
		`loopback-from-outside: iifname != "lo" ip6 saddr != fd00:dc1::/32 ip6 saddr != fe80::/10 ip6 daddr fd00:dc1:ff::a1/128 drop`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
}
