package gateway

import (
	"encoding/json"
	"testing"

	"github.com/mwindower/open-dci/internal/frr"
)

func TestEVPNReady(t *testing.T) {
	evpn := map[string]json.RawMessage{"l2VpnEvpn": nil, "ipv4Vpn": nil}
	vpnOnly := map[string]json.RawMessage{"ipv4Vpn": nil}
	nb := func(state string, upMsec int64, afi map[string]json.RawMessage, eor bool) neighborState {
		n := neighborState{State: state, UpMsec: upMsec, AFI: afi}
		n.GR.EndOfRibRecv = map[string]bool{"l2VpnEvpn": eor}
		return n
	}
	for _, c := range []struct {
		name string
		nbs  map[string]neighborState
		want bool
	}{
		{"no neighbors", nil, false},
		{"EVPN sessions down", map[string]neighborState{"uplink0": nb("Active", 0, evpn, false)}, false},
		{"just up, no End-of-RIB", map[string]neighborState{"uplink0": nb("Established", 800, evpn, false)}, false},
		{"End-of-RIB from one exit", map[string]neighborState{
			"uplink0": nb("Established", 900, evpn, true), "uplink1": nb("Connect", 0, evpn, false)}, true},
		{"no End-of-RIB, up long enough", map[string]neighborState{"uplink0": nb("Established", 31000, evpn, false)}, true},
		{"only a VPN session", map[string]neighborState{"2001:db8::1": nb("Established", 60000, vpnOnly, true)}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := evpnReady(c.nbs)
			if r.Ready != c.want {
				t.Fatalf("ready = %v (%s), want %v", r.Ready, r.Reason, c.want)
			}
			if !r.Ready && r.Reason == "" {
				t.Fatal("not ready without a reason")
			}
		})
	}
}

// Activating the VPN families on the base session makes FRR reset it; a new
// VPN-only peer or anything else doesn't touch the EVPN sessions.
func TestResetsSessions(t *testing.T) {
	evpn := map[string]bool{"uplink0": true, "uplink1": true}
	line := func(af, text string) frr.Line {
		return frr.Line{Context: []string{"router bgp 65000", af}, Text: text, Leaf: true}
	}
	for _, c := range []struct {
		name    string
		missing []frr.Line
		want    bool
	}{
		{"in sync", nil, false},
		{"VPN on the base session", []frr.Line{line("address-family ipv4 vpn", "neighbor uplink1 route-map DCI-PEER-IN in"), line("address-family ipv4 vpn", "neighbor uplink1 activate")}, true},
		{"only a route-map", []frr.Line{line("address-family ipv4 vpn", "neighbor uplink1 route-map DCI-PEER-IN in")}, false},
		{"new multihop peer", []frr.Line{line("address-family ipv6 vpn", "neighbor 2001:db8::1 activate")}, false},
		{"deactivation", []frr.Line{line("address-family ipv4 unicast", "no neighbor uplink0 activate")}, false},
		{"VRF instance", []frr.Line{{Context: []string{"router bgp 65000 vrf vrf1", "address-family ipv4 unicast"}, Text: "neighbor uplink0 activate", Leaf: true}}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := resetsSessions(c.missing, evpn); got != c.want {
				t.Fatalf("resetsSessions = %v, want %v", got, c.want)
			}
		})
	}
}

// A prefix with valid paths but no best path is installed nowhere: status
// must count it (seen after an frr-reload re-created a VRF's BGP instance).
func TestTallyRoutes(t *testing.T) {
	local, remote, noBest := tallyRoutes(map[string][]vrfPath{
		"10.0.16.10/32": {{Valid: true, Best: true}, {Valid: true}},
		"10.0.32.10/32": {{Valid: true, Best: true, NHVrfName: "default"}, {Valid: true, NHVrfName: "default"}},
		"10.0.17.10/32": {{Valid: true}, {Valid: true}},
		"10.0.99.0/24":  {{Valid: false}},
	})
	if local != 1 || remote != 1 || noBest != 1 {
		t.Fatalf("local %d remote %d noBest %d, want 1 1 1", local, remote, noBest)
	}
}

func TestDrainMarker(t *testing.T) {
	g := &Gateway{StateFile: t.TempDir() + "/applied.conf"}
	if g.Drained() {
		t.Fatal("drained without marker")
	}
	if err := g.SetDrained(true); err != nil || !g.Drained() {
		t.Fatalf("drain: %v", err)
	}
	if err := g.SetDrained(false); err != nil || g.Drained() {
		t.Fatalf("undrain: %v", err)
	}
	if err := g.SetDrained(false); err != nil {
		t.Fatalf("undrain twice: %v", err)
	}
}
