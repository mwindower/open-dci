package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mwindower/open-dci/internal/config"
)

// fakeSource answers with nets; err makes Sync fail, keeping the last answer.
type fakeSource struct {
	nets   []config.Network
	synced time.Time
	err    error
}

func (f *fakeSource) Interval() time.Duration { return time.Minute }

func (f *fakeSource) Sync(context.Context) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	f.synced = time.Now()
	return true, nil
}

func (f *fakeSource) Networks([]config.Network) ([]config.Network, []config.Skipped, time.Time) {
	return f.nets, []config.Skipped{{Group: "g/crowded", Reason: "two networks in partition a"}}, f.synced
}

func TestSource(t *testing.T) {
	static, err := config.Load("../../lab/configs/gw-a1/open-dci.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{err: errors.New("connection refused")}
	g := &Gateway{Config: static, Source: src, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	g.ApplySource()

	// no answer and no cache: nothing is reconciled (nothing removed)
	if _, err := g.SyncSource(context.Background()); err == nil {
		t.Fatal("sync error swallowed")
	}
	if _, _, err := g.Plan(); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("plan without an answer: %v", err)
	}
	if g.source.Errors != 1 {
		t.Fatalf("errors: %d", g.source.Errors)
	}

	tenant2 := config.Network{Group: "project/tenant-2", VRF: "vrf3982", VNI: 3982, SID: 3982, Table: 3982, RouteTarget: "65535:3473928608", Aggregates: []string{"10.0.17.0/24"}}
	conflict := config.Network{Group: "project/x", VRF: "vrf9", VNI: 3981, SID: 9, Table: 9, RouteTarget: "65535:20000000", Aggregates: []string{"10.9.0.0/24"}}
	src.err, src.nets = nil, []config.Network{tenant2, conflict}
	changed, err := g.SyncSource(context.Background())
	if err != nil || !changed {
		t.Fatalf("sync: changed %v, %v", changed, err)
	}
	if err := g.sourceReady(); err != nil {
		t.Fatal(err)
	}
	var vrfs []string
	for _, n := range g.Config.Networks {
		vrfs = append(vrfs, n.VRF)
	}
	if strings.Join(vrfs, " ") != "vrf3981 vrf3982" || len(g.static.Networks) != 1 {
		t.Fatalf("merged: %v (static %d)", vrfs, len(g.static.Networks))
	}
	if g.source.Networks != 1 || len(g.source.Skipped) != 2 || g.source.Skipped[1].Group != "project/x" {
		t.Fatalf("source status: %+v", g.source)
	}

	// the same answer again: no change; a failure keeps the networks
	if changed, _ := g.SyncSource(context.Background()); changed {
		t.Fatal("unchanged answer reported as change")
	}
	src.err = errors.New("timeout")
	if changed, _ := g.SyncSource(context.Background()); changed || len(g.Config.Networks) != 2 {
		t.Fatalf("failure changed the networks: %v", g.Config.Networks)
	}

	st := Status{Source: &g.source}
	warn := st.Warnings(time.Now().Add(time.Hour))
	if len(warn) != 3 || !strings.Contains(warn[0], "no answer since") || !strings.Contains(warn[0], "timeout") {
		t.Fatalf("warnings: %q", warn)
	}
	if warn := st.Warnings(time.Now()); len(warn) != 2 {
		t.Fatalf("fresh source warns: %q", warn)
	}
}

// A network that leaves the config is forgotten as settled: when it comes
// back, it is provisioned anew and can't withdraw the gateway meanwhile.
func TestSettledPruned(t *testing.T) {
	static, err := config.Load("../../lab/configs/gw-a1/open-dci.yaml")
	if err != nil {
		t.Fatal(err)
	}
	tenant2 := config.Network{Group: "tenant-2/main", VRF: "vrf3982", VNI: 3982, SID: 3982, Table: 3982, RouteTarget: "65535:26782482", Aggregates: []string{"10.0.17.0/24"}}
	src := &fakeSource{nets: []config.Network{tenant2}, synced: time.Now()}
	g := &Gateway{Config: static, Source: src, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	g.ApplySource()
	g.settled = map[string]bool{"vrf3981": true, "vrf3982": true}
	src.nets = nil
	g.ApplySource()
	if !g.settled["vrf3981"] || g.settled["vrf3982"] {
		t.Fatalf("settled: %v", g.settled)
	}
}
