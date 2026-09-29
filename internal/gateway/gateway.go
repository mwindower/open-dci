// Package gateway turns an existing EVPN VTEP (a metal-stack firewall) into
// an srv6-dci gateway: it applies the kernel part, adds the FRR configuration
// and keeps both in place when the base system rewrites its own config.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mwindower/srv6-dci/internal/config"
	"github.com/mwindower/srv6-dci/internal/frr"
)

// Gateway reconciles one gateway.
type Gateway struct {
	Config *config.Config
	FRR    frr.Vtysh
	Log    *slog.Logger
	// StateFile remembers the last applied FRR snippet, so that lines
	// dropped from the config (networks, peers) can be removed again.
	StateFile string
	// SkipKernel leaves the kernel alone (render/diff on a non-gateway host).
	SkipKernel bool
}

// Result describes what a reconcile did.
type Result struct {
	Identity frr.Identity
	Missing  []frr.Line // desired lines that were not in the running config
	Removed  string     // commands applied to remove stale lines
	Applied  bool
}

// Plan computes the desired FRR snippet and its difference to the running
// configuration, without changing anything.
func (g *Gateway) Plan() (desired string, res Result, err error) {
	running, err := g.FRR.RunningConfig()
	if err != nil {
		return "", res, fmt.Errorf("read running-config: %w", err)
	}
	id, err := g.identity(running)
	if err != nil {
		return "", res, err
	}
	res.Identity = id
	desired, err = frr.Render(g.Config, id)
	if err != nil {
		return "", res, err
	}
	want := frr.Parse(desired)
	res.Missing = frr.Missing(want, frr.Parse(running))
	if prev, err := os.ReadFile(g.StateFile); err == nil {
		res.Removed = frr.Removals(frr.Parse(string(prev)), want)
	}
	return desired, res, nil
}

// Reconcile brings kernel and FRR to the desired state once.
func (g *Gateway) Reconcile() (Result, error) {
	if !g.SkipKernel {
		if err := ensureKernel(g.Config); err != nil {
			return Result{}, fmt.Errorf("kernel: %w", err)
		}
	}
	desired, res, err := g.Plan()
	if err != nil {
		return res, err
	}
	if res.Removed != "" {
		g.Log.Info("removing stale configuration", "commands", res.Removed)
		if err := g.FRR.Apply(res.Removed); err != nil {
			return res, fmt.Errorf("remove stale configuration: %w", err)
		}
	}
	if len(res.Missing) > 0 {
		for _, l := range res.Missing {
			if l.Leaf { // block headers are implied by their commands
				g.Log.Info("drift", "context", strings.Join(l.Context, " > "), "line", l.Text)
			}
		}
		if err := g.FRR.Apply(desired); err != nil {
			return res, fmt.Errorf("apply: %w", err)
		}
		res.Applied = true
	}
	if res.Removed != "" || res.Applied {
		if err := g.saveState(desired); err != nil {
			return res, err
		}
		// verify: FRR must now show every desired line
		if _, after, err := g.Plan(); err != nil {
			return res, err
		} else if len(after.Missing) > 0 {
			return res, fmt.Errorf("%d line(s) still missing after apply, e.g. %q", len(after.Missing), after.Missing[0].Text)
		}
	} else if _, err := os.Stat(g.StateFile); errors.Is(err, os.ErrNotExist) {
		if err := g.saveState(desired); err != nil {
			return res, err
		}
	}
	return res, nil
}

// Run reconciles until ctx is done. Errors are logged and retried, which also
// covers FRR not being up yet and the base system reloading its config.
func (g *Gateway) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		res, err := g.Reconcile()
		switch {
		case err != nil:
			g.Log.Error("reconcile", "err", err)
		case res.Applied || res.Removed != "":
			g.Log.Info("reconciled", "asn", res.Identity.ASN, "routerID", res.Identity.RouterID, "applied", res.Applied, "missingLines", len(res.Missing))
		default:
			g.Log.Debug("in sync")
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// identity discovers ASN and router-id from FRR, checks them against the
// config (if set) and verifies that every VRF has a BGP instance.
func (g *Gateway) identity(running string) (frr.Identity, error) {
	base, err := frr.DiscoverBase(running)
	if err != nil {
		return frr.Identity{}, err
	}
	id := base.Identity
	gw := g.Config.Gateway
	if gw.ASN != 0 && gw.ASN != id.ASN {
		return id, fmt.Errorf("gateway.asn %d does not match the running BGP instance (AS %d)", gw.ASN, id.ASN)
	}
	if gw.RouterID != "" {
		if id.RouterID != "" && gw.RouterID != id.RouterID {
			return id, fmt.Errorf("gateway.routerID %s does not match the running BGP router-id %s", gw.RouterID, id.RouterID)
		}
		id.RouterID = gw.RouterID
	}
	if id.RouterID == "" {
		return id, fmt.Errorf("the BGP instance has no explicit router-id; set gateway.routerID")
	}
	for _, vrf := range allVRFs(g.Config) {
		if !base.VRFInstances[vrf] {
			return id, fmt.Errorf("no BGP instance for vrf %s (router bgp %d vrf %s) in the running configuration", vrf, id.ASN, vrf)
		}
	}
	return id, nil
}

func (g *Gateway) saveState(desired string) error {
	if g.StateFile == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(g.StateFile), 0o755); err != nil {
		return err
	}
	return os.WriteFile(g.StateFile, []byte(desired), 0o644)
}
