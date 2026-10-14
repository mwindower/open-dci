package gateway

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/mwindower/open-dci/internal/config"
)

// NetworkSource provides networks in addition to the config file's (e.g.
// internal/source/metalstack). It keeps its last good answer: a failed Sync
// changes nothing.
type NetworkSource interface {
	// Interval between syncs.
	Interval() time.Duration
	// Sync fetches the networks once; changed: they differ from before.
	Sync(ctx context.Context) (changed bool, err error)
	// Networks returns the networks of the last good answer (SIDs assigned
	// around the static ones), the groups that can't be stitched and when
	// the answer came; zero: nothing known yet.
	Networks(static []config.Network) ([]config.Network, []config.Skipped, time.Time)
}

// SourceStatus is the state of the network source.
type SourceStatus struct {
	// Synced: the last good answer (zero: none, the gateway waits)
	Synced   time.Time
	Interval time.Duration
	// Networks the source adds (after merging with the static ones)
	Networks int
	// Skipped groups, by the source or because they conflict
	Skipped   []config.Skipped `json:",omitempty"`
	Errors    uint64
	LastError string `json:",omitempty"`
}

// Stale: no good answer for three intervals; the gateway keeps the last one.
func (s SourceStatus) Stale(now time.Time) bool {
	return s.Synced.IsZero() || now.Sub(s.Synced) > 3*s.Interval
}

// ApplySource merges the source's networks into the config. The config the
// gateway was created with stays the static part.
func (g *Gateway) ApplySource() {
	if g.Source == nil {
		return
	}
	if g.static == nil {
		g.static = g.Config
	}
	nets, skipped, synced := g.Source.Networks(g.static.Networks)
	cfg, conflicts := g.static.WithNetworks(nets)
	skipped = append(skipped, conflicts...)
	if !slices.Equal(skipped, g.source.Skipped) {
		for _, s := range skipped {
			g.Log.Warn("source: not stitched", "group", s.Group, "reason", s.Reason)
		}
	}
	g.Config = cfg
	g.pruneSettled()
	g.source.Synced, g.source.Interval = synced, g.Source.Interval()
	g.source.Networks = len(cfg.Networks) - len(g.static.Networks)
	g.source.Skipped = skipped
}

// SyncSource polls the source once and merges its networks; changed: the
// networks differ from before.
func (g *Gateway) SyncSource(ctx context.Context) (changed bool, err error) {
	if g.Source == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	changed, err = g.Source.Sync(ctx)
	if err != nil {
		g.source.Errors++
		g.source.LastError = err.Error()
	} else {
		g.source.LastError = ""
	}
	before := g.Config.Networks
	g.ApplySource()
	return changed && !slices.EqualFunc(before, g.Config.Networks, sameNetwork), err
}

func sameNetwork(a, b config.Network) bool {
	return fmt.Sprintf("%+v", a) == fmt.Sprintf("%+v", b)
}

// sourceReady: without any answer (and no cache), the gateway doesn't know
// its networks and must not remove anything.
func (g *Gateway) sourceReady() error {
	if g.Source != nil && g.source.Synced.IsZero() {
		msg := "no answer yet and no cache"
		if g.source.LastError != "" {
			msg = g.source.LastError
		}
		return fmt.Errorf("source: networks unknown, not reconciling: %s", msg)
	}
	return nil
}
