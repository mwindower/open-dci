package gateway

import (
	"encoding/json"
	"testing"
)

func TestHasHosts(t *testing.T) {
	// as FRR 10.4.1 shows them in "show bgp vrf X ipv4 unicast P longer-prefixes json"
	const (
		own      = `"10.0.16.0/24": [{"weight":32768,"peerId":"(unspec)","path":""}]`
		host     = `"10.0.16.10/32": [{"suppressed":true,"weight":0,"peerId":"fe80::1","path":"4200000014 4200000015"}]`
		remote   = `"10.0.16.0/24": [{"weight":0,"peerId":"(unspec)","path":"4200000018 4200000026","nhVrfName":"default"}]`
		remote32 = `"10.0.16.20/32": [{"weight":0,"peerId":"(unspec)","path":"4200000018 4200000026","nhVrfName":"default"}]`
	)
	for _, c := range []struct {
		name, routes string
		hosts        bool
	}{
		{"host from the fabric", "{" + own + "," + host + "}", true},
		{"host, not yet announced", "{" + host + "}", true},
		{"only the own aggregate left", "{" + own + "}", false},
		{"remote partition's aggregate", "{" + remote + "}", false},
		{"remote host route only", "{" + remote32 + "}", false},
		{"nothing", `{}`, false},
	} {
		var routes map[string][]aggregatePath
		if err := json.Unmarshal([]byte(c.routes), &routes); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := hasHosts(routes, "10.0.16.0/24"); got != c.hosts {
			t.Errorf("%s: hosts %v, want %v", c.name, got, c.hosts)
		}
	}
}
