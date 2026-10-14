package gateway

import (
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mwindower/open-dci/internal/config"
)

func apiNet(vrf string, vni uint32, rt string, prefix string) APINetwork {
	return APINetwork{Group: "g/" + vrf, Network: config.Network{VRF: vrf, VNI: vni, RouteTarget: rt, Aggregates: []string{prefix}}}
}

func testGateway(t *testing.T) (*Gateway, string) {
	t.Helper()
	static, err := config.Load("../../lab/configs/gw-a1/open-dci.yaml") // vrf3981, VNI 3981, RT 65535:1001
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "api-networks.json")
	store, err := OpenAPIStore(path)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Config: static, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	g.UseAPI(store)
	return g, path
}

// vrfs returns the VRFs of the config Run would reconcile next (it takes
// the latest API state over first, as Run does).
func vrfs(g *Gateway) string {
	g.takeAPI()
	var out []string
	for _, n := range g.Config.Networks {
		out = append(out, n.VRF)
	}
	return strings.Join(out, " ")
}

func TestAPIStore(t *testing.T) {
	g, path := testGateway(t)

	// no write yet: the gateway doesn't know its networks
	if err := g.apiReady(); err == nil {
		t.Fatal("reconciling before the first API write")
	}

	// the controller sets two networks, one conflicting with the config file
	changed, skipped, err := g.SetNetworks("dci-controller", []APINetwork{
		apiNet("vrf3982", 3982, "65535:2001", "10.0.17.0/24"),
		apiNet("vrf9", 3981, "65535:2002", "10.9.0.0/24"), // VNI of the static vrf3981
	})
	if err != nil || !changed || len(skipped) != 1 || skipped[0].VRF != "vrf9" || !strings.Contains(skipped[0].Reason, "used twice") {
		t.Fatalf("set: changed %v, skipped %+v, %v", changed, skipped, err)
	}
	if err := g.apiReady(); err != nil {
		t.Fatal(err)
	}
	if got := vrfs(g); got != "vrf3981 vrf3982" {
		t.Fatalf("merged: %s", got)
	}
	if g.Config.Networks[1].SID != 3982 || g.apiStatus.Networks != 1 || len(g.apiStatus.Skipped) != 1 {
		t.Fatalf("SID or status: %+v %+v", g.Config.Networks[1], g.apiStatus)
	}
	select {
	case <-g.wake:
	default:
		t.Fatal("Run wasn't woken")
	}

	// the same again: no change
	if changed, _, _ := g.SetNetworks("dci-controller", []APINetwork{
		apiNet("vrf3982", 3982, "65535:2001", "10.0.17.0/24"),
		apiNet("vrf9", 3981, "65535:2002", "10.9.0.0/24"),
	}); changed {
		t.Fatal("unchanged set reported as change")
	}

	// another owner adds one network; the controller's set doesn't touch it
	if _, err := g.PutNetwork("ops", apiNet("vrf4000", 4000, "65535:3001", "10.4.0.0/24")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := g.SetNetworks("dci-controller", []APINetwork{apiNet("vrf3982", 3982, "65535:2001", "10.0.17.0/24")}); err != nil {
		t.Fatal(err)
	}
	if got := vrfs(g); got != "vrf3981 vrf3982 vrf4000" {
		t.Fatalf("after the controller's set: %s", got)
	}

	// Put is imperative: conflicts and other owners' VRFs are refused
	if _, err := g.PutNetwork("ops", apiNet("vrf4001", 3982, "65535:3002", "10.4.1.0/24")); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting VNI: %v", err)
	}
	if _, err := g.PutNetwork("ops", apiNet("vrf3982", 3999, "65535:3003", "10.4.2.0/24")); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("other owner's VRF: %v", err)
	}
	if _, err := g.PutNetwork("ops", apiNet("vrf4002", 4002, "65535:3004", "10.4.3.0/32")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid network: %v", err)
	}
	if err := g.DeleteNetwork("dci-controller", "vrf4000"); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("delete other owner's: %v", err)
	}
	if err := g.DeleteNetwork("ops", "vrf4999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete unknown: %v", err)
	}
	if _, err := g.PutNetwork("", apiNet("vrf4003", 4003, "65535:3005", "10.4.4.0/24")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no owner: %v", err)
	}

	// persisted: a restart keeps everything
	store, err := OpenAPIStore(path)
	if err != nil {
		t.Fatal(err)
	}
	g2 := &Gateway{Config: g.static, Log: g.Log}
	g2.UseAPI(store)
	if got := vrfs(g2); got != "vrf3981 vrf3982 vrf4000" || g2.apiReady() != nil {
		t.Fatalf("after a restart: %s", got)
	}
	nets, _ := g2.Networks()
	if len(nets) != 3 || nets[0].API || !nets[1].API || nets[1].Owner != "dci-controller" || nets[2].Owner != "ops" {
		t.Fatalf("origins: %+v", nets)
	}

	if err := g.DeleteNetwork("ops", "vrf4000"); err != nil {
		t.Fatal(err)
	}
	if got := vrfs(g); got != "vrf3981 vrf3982" {
		t.Fatalf("after delete: %s", got)
	}
}

// Two owners want the same VNI: the older network wins, on every gateway.
func TestAPIConflictOrder(t *testing.T) {
	g, _ := testGateway(t)
	old := apiNet("vrf5000", 5000, "65535:5000", "10.5.0.0/24")
	old.Created = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	young := apiNet("vrf5001", 5000, "65535:5001", "10.5.1.0/24")
	young.Created = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	if _, _, err := g.SetNetworks("b", []APINetwork{young}); err != nil {
		t.Fatal(err)
	}
	_, skipped, err := g.SetNetworks("a", []APINetwork{old})
	if err != nil {
		t.Fatal(err)
	}
	if got := vrfs(g); got != "vrf3981 vrf5000" || len(skipped) != 0 || len(g.apiStatus.Skipped) != 1 || g.apiStatus.Skipped[0].VRF != "vrf5001" {
		t.Fatalf("older one must win: %s, skipped %+v, status %+v", got, skipped, g.apiStatus.Skipped)
	}
}

// A network that leaves the config is forgotten as settled: when it comes
// back, it is provisioned anew and can't withdraw the gateway meanwhile.
func TestSettledPruned(t *testing.T) {
	g, _ := testGateway(t)
	if _, _, err := g.SetNetworks("c", []APINetwork{apiNet("vrf3982", 3982, "65535:2001", "10.0.17.0/24")}); err != nil {
		t.Fatal(err)
	}
	g.settled = map[string]bool{"vrf3981": true, "vrf3982": true}
	if _, _, err := g.SetNetworks("c", nil); err != nil {
		t.Fatal(err)
	}
	g.takeAPI()
	if !g.settled["vrf3981"] || g.settled["vrf3982"] {
		t.Fatalf("settled: %v", g.settled)
	}
}
