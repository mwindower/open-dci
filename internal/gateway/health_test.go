package gateway

import (
	"encoding/json"
	"net/netip"
	"testing"
)

func TestUsable(t *testing.T) {
	block := netip.MustParsePrefix("fd00:dc1::/32")
	parse := func(s string) map[string][]routeEntry {
		var r map[string][]routeEntry
		if err := json.Unmarshal([]byte(s), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	for _, c := range []struct {
		name, json string
		want       bool
	}{
		{"bgp via uplink", `{"fd00:dc1:a::/48":[{"prefix":"fd00:dc1:a::/48","selected":true,"nexthops":[{"active":true,"fib":true,"interfaceName":"uplink0"}]}]}`, true},
		{"kernel unreachable", `{"fd00:dc1:a::/48":[{"prefix":"fd00:dc1:a::/48","selected":true,"nexthops":[{"active":true,"fib":true,"unreachable":true}]}]}`, false},
		{"blackhole", `{"fd00:dc1:a::/48":[{"prefix":"fd00:dc1:a::/48","selected":true,"nexthops":[{"active":true,"fib":true,"interfaceName":"lo","blackhole":true}]}]}`, false},
		{"only the default route", `{"::/0":[{"prefix":"::/0","selected":true,"nexthops":[{"active":true,"fib":true,"interfaceName":"eth0"}]}]}`, false},
		{"covering block route only", `{"fd00:dc1::/16":[{"prefix":"fd00::/16","selected":true,"nexthops":[{"active":true,"fib":true,"interfaceName":"eth0"}]}]}`, false},
		{"inactive next hop", `{"fd00:dc1:a::/48":[{"prefix":"fd00:dc1:a::/48","selected":true,"nexthops":[{"active":false,"fib":false,"interfaceName":"uplink0"}]}]}`, false},
		{"no route", `{}`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := usable(parse(c.json), block); got != c.want {
				t.Fatalf("usable = %v, want %v", got, c.want)
			}
		})
	}
}

// Only the loss of every remote SID is the gateway's own fault; a single
// unreachable partition must not make all gateways withdraw.
func TestTransportVerdict(t *testing.T) {
	a, b := netip.MustParseAddr("fd00:dc1:a:f8d::"), netip.MustParseAddr("fd00:dc1:c:1393::")
	for _, c := range []struct {
		name string
		r    map[netip.Addr]bool
		want bool
	}{
		{"no remote routes yet", nil, true},
		{"all reachable", map[netip.Addr]bool{a: true, b: true}, true},
		{"one partition unreachable", map[netip.Addr]bool{a: false, b: true}, true},
		{"none reachable", map[netip.Addr]bool{a: false, b: false}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := transportVerdict(c.r)
			if h.OK != c.want || !h.OK && h.Reason == "" {
				t.Fatalf("%+v, want OK=%v", h, c.want)
			}
		})
	}
}
