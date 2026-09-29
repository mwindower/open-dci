package config

import (
	"strings"
	"testing"
)

const valid = `
gateway:
  locator: fd00:dc1:a::/48
  locatorBlock: fd00:dc1::/32
dciNetwork:
  vrf: vrf104100
peers:
  - {address: "fd00:dc1:b::1", asn: 4200000022}
networks:
  - {vrf: vrf3981, routeTarget: "65535:1001"}
`

func TestDefaultsAndDerived(t *testing.T) {
	c, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if c.DCINetwork.MTU != DefaultMTU || c.DCINetwork.TenantMTU != DefaultTenantMTU || c.Gateway.NodeLength != 16 {
		t.Fatalf("defaults not applied: %+v", c)
	}
	if c.DCINetwork.Veth != "dci0" || c.DCINetwork.VethPeer != "dci1" {
		t.Fatalf("veth defaults: %+v", c.DCINetwork)
	}
	if got := c.Gateway.Loopback().String(); got != "fd00:dc1:a::1" {
		t.Fatalf("loopback: %s", got)
	}
	if got := c.RDFor(c.Networks[0], "10.0.0.12"); got != "10.0.0.12:1001" {
		t.Fatalf("rd: %s", got)
	}
}

func TestValidate(t *testing.T) {
	for _, c := range []struct {
		name, from, to, wantErr string
	}{
		{"locator outside block", "locator: fd00:dc1:a::/48", "locator: fd00:dc2:a::/48", "not inside gateway.locatorBlock"},
		{"node length mismatch", "locator: fd00:dc1:a::/48", "locator: fd00:dc1:a::/56", "nodeLength"},
		{"locator host bits", "locator: fd00:dc1:a::/48", "locator: fd00:dc1:a::1/48", "host bits"},
		{"peer in own locator", `"fd00:dc1:b::1"`, `"fd00:dc1:a::5"`, "inside the own locator"},
		{"peer outside block", `"fd00:dc1:b::1"`, `"2001:db8::1"`, "outside gateway.locatorBlock"},
		{"peer without asn", "asn: 4200000022", "asn: 0", "peers[0].asn"},
		{"bad route target", `"65535:1001"`, `"65535"`, "routeTarget"},
		{"tenant vrf is dci vrf", "vrf: vrf3981", "vrf: vrf104100", "used twice"},
		{"mtu too small", "vrf: vrf104100\n", "vrf: vrf104100\n  mtu: 9000\n", "dciNetwork.mtu"},
		{"unknown field", "vrf: vrf104100\n", "vrf: vrf104100\n  typo: 1\n", "unknown field"},
	} {
		t.Run(c.name, func(t *testing.T) {
			raw := strings.Replace(valid, c.from, c.to, 1)
			if raw == valid {
				t.Fatalf("test case does not modify the config")
			}
			_, err := Parse([]byte(raw))
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

func TestLabConfigsValid(t *testing.T) {
	for _, fw := range []string{"fw-a", "fw-b"} {
		if _, err := Load("../../lab/configs/" + fw + "/srv6-dci.yaml"); err != nil {
			t.Errorf("%s: %v", fw, err)
		}
	}
}
