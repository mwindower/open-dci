package gateway

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/mwindower/open-dci/internal/config"
)

// The testdata is FRR 10.4.1's output on the lab's gw-a1 (vrf3981, VNI 3981).

func TestEVPNRoutes(t *testing.T) {
	raw, err := os.ReadFile("testdata/gw-a1.evpn-type5.json")
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := decodeEVPN(raw)
	if err != nil {
		t.Fatal(err)
	}
	info := l3vniInfo{RD: "10.0.0.16:5", ImportRTs: []string{"RT:59920:3981"}}
	got := evpnRoutes(tbl, info, 3981)
	sortRoutes(&Routes{EVPN: got})

	type key struct {
		adv          bool
		rd, prefix   string
		nexthop, rts string
		paths        int
	}
	var keys []key
	for _, e := range got {
		rts, _ := json.Marshal(e.RTs)
		keys = append(keys, key{e.Advertised, e.RD, e.Prefix, e.NextHop, string(rts), e.Paths})
		if e.NoBestPath {
			t.Errorf("%s: no best path", e.Prefix)
		}
	}
	// received: the fabric's host routes (auto RT of the exit's ASN, imported
	// by the VNI); advertised: the remote partitions' aggregates. Neither the
	// other tenant's routes nor the DCI network's (vrf104100) belong here.
	want := []key{
		{false, "10.0.0.11:4", "10.0.16.10/32", "10.0.0.11", `["59915:3981"]`, 2},
		{false, "10.0.0.11:4", "2001:db8:16::10/128", "10.0.0.11", `["59915:3981"]`, 2},
		{true, "10.0.0.16:5", "10.0.32.0/24", "10.0.0.16", `["65535:1001","59920:3981"]`, 1},
		{true, "10.0.0.16:5", "10.0.48.0/24", "10.0.0.16", `["65535:1001","59920:3981"]`, 1},
		{true, "10.0.0.16:5", "2001:db8:32::/48", "10.0.0.16", `["65535:1001","59920:3981"]`, 1},
		{true, "10.0.0.16:5", "2001:db8:48::/48", "10.0.0.16", `["65535:1001","59920:3981"]`, 1},
	}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("got\n%v\nwant\n%v", keys, want)
	}
}

func TestSplitRoutes(t *testing.T) {
	var bgp struct {
		Routes vrfTable `json:"routes"`
	}
	var zebra map[string][]zebraRoute
	for f, v := range map[string]any{"testdata/gw-a1.vrf3981-ipv4.json": &bgp, "testdata/gw-a1.vrf3981-zebra-ipv4.json": &zebra} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, v); err != nil {
			t.Fatal(err)
		}
	}
	local, remote := splitRoutes(bgp.Routes, map[string]bool{"10.0.16.0/24": true}, map[string]bool{"10.0.16.0/24": true}, zebra)
	r := &Routes{Local: local, Remote: remote}
	sortRoutes(r)

	wantLocal := []LocalRoute{
		{Prefix: "10.0.16.0/24", Aggregate: true, Exported: true, Installed: true},
		{Prefix: "10.0.16.10/32", NextHop: "10.0.0.11", ASPath: "4200000014 4200000013 4200000011 4200000015", Installed: true},
	}
	wantRemote := []RemoteRoute{
		{Prefix: "10.0.32.0/24", SIDs: []string{"fd00:dc1:b:fab::"}, ASPath: "4200000014 4200000024 4200000026", Paths: 4, Installed: true},
		{Prefix: "10.0.48.0/24", SIDs: []string{"fd00:dc1:c:1393::"}, ASPath: "4200000014 4200000034 4200000036", Paths: 4, Installed: true},
	}
	if !reflect.DeepEqual(r.Local, wantLocal) {
		t.Errorf("local:\n%+v\nwant\n%+v", r.Local, wantLocal)
	}
	if !reflect.DeepEqual(r.Remote, wantRemote) {
		t.Errorf("remote:\n%+v\nwant\n%+v", r.Remote, wantRemote)
	}

	// valid paths but none selected: listed as local, flagged
	stuck := vrfTable{"10.0.16.20/32": {{Valid: true}}}
	if l, _ := splitRoutes(stuck, nil, nil, nil); len(l) != 1 || !l[0].NoBestPath || l[0].Installed {
		t.Errorf("no best path: %+v", l)
	}
}

func TestVTEP(t *testing.T) {
	for in, want := range map[string]string{"::ffff:10.0.0.11": "10.0.0.11", "0.0.0.0": "", "::": "", "10.0.0.11": "10.0.0.11"} {
		if got := vtep(in); got != want {
			t.Errorf("vtep(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSelectNetworks(t *testing.T) {
	c := &config.Config{Networks: []config.Network{{VRF: "vrf3981", VNI: 3981}, {VRF: "vrf3982", VNI: 3982}}}
	for sel, want := range map[string][]string{"": nil, "all": {"vrf3981", "vrf3982"}, "vrf3982": {"vrf3982"}, "3981": {"vrf3981"}} {
		ns, err := SelectNetworks(c, sel)
		var got []string
		for _, n := range ns {
			got = append(got, n.VRF)
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %v %v, want %v", sel, got, err, want)
		}
	}
	if _, err := SelectNetworks(c, "vrf1"); err == nil {
		t.Error("unknown VRF: no error")
	}
}
