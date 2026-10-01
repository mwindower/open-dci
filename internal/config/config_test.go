package config

import (
	"strings"
	"testing"
)

const valid = `
gateway:
  locator: fd00:dc1:a::/48
  locatorBlock: fd00:dc1::/32
transport:
  vrf: vrf104100
peers:
  - {address: "fd00:dc1:b::1", asn: 4200000022}
networks:
  - {vrf: vrf3981, vni: 3981, routeTarget: "65535:1001", prefixes: ["10.0.16.0/24 le 32"]}
`

func TestDefaultsAndDerived(t *testing.T) {
	c, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if c.Transport.MTU != DefaultMTU || c.Transport.TenantMTU != DefaultTenantMTU || c.Gateway.NodeLength != 16 {
		t.Fatalf("defaults not applied: %+v", c)
	}
	if c.Transport.Veth != "dci0" || c.Transport.VethPeer != "dci1" {
		t.Fatalf("veth defaults: %+v", c.Transport)
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
		{"mtu too small", "vrf: vrf104100\n", "vrf: vrf104100\n  mtu: 9000\n", "transport.mtu"},
		{"unknown field", "vrf: vrf104100\n", "vrf: vrf104100\n  typo: 1\n", "unknown field"},
		{"vni missing", "vni: 3981, ", "", "networks[0].vni: required"},
		{"vni too large", "vni: 3981", "vni: 16777216", "networks[0].vni"},
		{"reserved table", "vni: 3981", "vni: 254", "reserved"},
		{"prefixes missing", `, prefixes: ["10.0.16.0/24 le 32"]`, "", "prefixes: at least one"},
		{"prefix with host bits", "10.0.16.0/24 le 32", "10.0.16.1/24", "host bits"},
		{"prefix le too small", "10.0.16.0/24 le 32", "10.0.16.0/24 le 24", "between 25 and 32"},
		{"prefix le too large", "10.0.16.0/24 le 32", "10.0.16.0/24 le 33", "between 25 and 32"},
		{"prefix ge above le", "10.0.16.0/24 le 32", "10.0.16.0/24 ge 30 le 28", "ge must not be larger"},
		{"prefix garbage", "10.0.16.0/24 le 32", "10.0.16.0/24 lt 32", "want PREFIX"},
		{"prefix listed twice", `"10.0.16.0/24 le 32"]`, `"10.0.16.0/24 le 32", "10.0.16.0/24  le 32"]`, "listed twice"},
		{"peer with interface and address", `{address: "fd00:dc1:b::1", asn: 4200000022}`, `{interface: uplink0, address: "fd00:dc1:b::1"}`, "either interface"},
		{"peer with bad interface", `{address: "fd00:dc1:b::1", asn: 4200000022}`, `{interface: "bad name"}`, "invalid interface"},
		{"max prefixes negative", "asn: 4200000022}", "asn: 4200000022, maxPrefixes: -1}", "maxPrefixes"},
		{"bad vtep", "locatorBlock: fd00:dc1::/32\n", "locatorBlock: fd00:dc1::/32\n  vtep: fd00::1\n", "gateway.vtep"},
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

func TestDefaultVRFTransport(t *testing.T) {
	c, err := Parse([]byte(strings.Replace(valid, "transport:\n  vrf: vrf104100\n", "", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if c.Transport.InVRF() {
		t.Fatal("transport without vrf must run in the default VRF")
	}
}

func TestLabConfigsValid(t *testing.T) {
	for _, fw := range []string{"gw-a1", "gw-a2", "gw-b1", "gw-b2"} {
		if _, err := Load("../../lab/configs/" + fw + "/open-dci.yaml"); err != nil {
			t.Errorf("%s: %v", fw, err)
		}
	}
}

func TestProvisioned(t *testing.T) {
	c, err := Parse([]byte(valid + `  - {vrf: vrf3983, vni: 3983, table: 2000, routeTarget: "65535:1003", prefixes: ["10.0.18.0/24"]}
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Networks[0].Table != 3981 || c.Networks[1].Table != 2000 {
		t.Fatalf("table defaults to the vni: %+v", c.Networks)
	}
	if c.Networks[0].BridgeName() != "dcibr3981" || c.Networks[0].VxlanName() != "dcivx3981" {
		t.Fatalf("device names: %s %s", c.Networks[0].BridgeName(), c.Networks[0].VxlanName())
	}
	for _, dup := range []string{"vni: 3981, table: 7", "vni: 3984, table: 3981"} {
		raw := valid + "  - {vrf: vrf3984, " + dup + ", routeTarget: \"65535:1004\", prefixes: [10.0.19.0/24]}\n"
		if _, err := Parse([]byte(raw)); err == nil || !strings.Contains(err.Error(), "used twice") {
			t.Errorf("%s: want duplicate error, got %v", dup, err)
		}
	}
}

func TestPrefixRules(t *testing.T) {
	n := Network{Prefixes: []string{"10.0.16.0/24 le 32", "0.0.0.0/0", "2001:db8::/32 ge 48 le 64"}}
	v4, v6 := n.PrefixRules()
	if len(v4) != 2 || len(v6) != 1 {
		t.Fatalf("v4 %v v6 %v", v4, v6)
	}
	for got, want := range map[string]string{
		v4[0].String(): "10.0.16.0/24 le 32",
		v4[1].String(): "0.0.0.0/0", // a bare prefix matches exactly: only the default route
		v6[0].String(): "2001:db8::/32 ge 48 le 64",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestMaxPrefixesDefault(t *testing.T) {
	c, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if c.Peers[0].MaxPrefixes != DefaultMaxPrefixes {
		t.Fatalf("maxPrefixes: %d", c.Peers[0].MaxPrefixes)
	}
}

func TestInterfacePeer(t *testing.T) {
	c, err := Parse([]byte(strings.Replace(valid, `{address: "fd00:dc1:b::1", asn: 4200000022}`, `{interface: uplink0}`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if p := c.Peers[0]; p.Neighbor() != "uplink0" || p.MaxPrefixes != DefaultMaxPrefixes {
		t.Fatalf("peer: %+v", p)
	}
	dup := strings.Replace(valid, `peers:
  - {address: "fd00:dc1:b::1", asn: 4200000022}`, `peers: [{interface: uplink0}, {interface: uplink0}]`, 1)
	if _, err := Parse([]byte(dup)); err == nil || !strings.Contains(err.Error(), "used twice") {
		t.Fatalf("want duplicate interface error, got %v", err)
	}
}
