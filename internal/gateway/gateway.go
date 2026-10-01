// Package gateway turns an FRR box attached to EVPN fabrics (a dedicated
// gateway, e.g. at the exit) into an open-dci gateway: it provisions the tenant
// VRFs as L3VNIs, applies the kernel part, adds the FRR configuration and keeps
// both in place when the base system rewrites its own config.
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

	"github.com/mwindower/open-dci/internal/config"
	"github.com/mwindower/open-dci/internal/frr"
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

	lastReadiness Readiness
	// gate is the debounced readiness Run renders with; nil outside Run
	gate       *Readiness
	lastLogged bool
}

// Result describes what a reconcile did.
type Result struct {
	Identity frr.Identity
	Missing  []frr.Line // desired lines that were not in the running config
	Removed  string     // commands applied to remove stale lines
	// RemovedL3VNIs releases the L3VNIs of dropped provisioned VRFs; it is
	// applied before Removed
	RemovedL3VNIs string
	Applied       bool
	// Readiness decides whether locator and loopback are announced
	Readiness Readiness
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
	res.Readiness = g.readiness()
	if g.gate != nil {
		res.Readiness = *g.gate
	}
	id.Withhold = !res.Readiness.Ready
	res.Identity = id
	desired, err = frr.Render(g.Config, id)
	if err != nil {
		return "", res, err
	}
	want := frr.Parse(desired)
	res.Missing = frr.Missing(want, frr.Parse(running))
	if prev, err := os.ReadFile(g.StateFile); err == nil {
		res.RemovedL3VNIs = frr.L3VNIRemovals(frr.Parse(string(prev)), want)
		res.Removed = frr.Removals(frr.Parse(string(prev)), want)
	}
	return desired, res, nil
}

// Reconcile brings kernel and FRR to the desired state once.
//
// Order: the kernel first (FRR needs the VRFs), then stale FRR lines are
// removed, then devices of provisioned networks that are gone (FRR must have
// released their L3VNI), then their FRR VRFs, and finally missing lines are
// applied.
func (g *Gateway) Reconcile() (Result, error) {
	desired, res, err := g.Plan()
	if err != nil {
		return res, err
	}
	if !g.SkipKernel {
		if err := ensureKernel(g.Config, res.Identity); err != nil {
			return res, fmt.Errorf("kernel: %w", err)
		}
	}
	if res.RemovedL3VNIs != "" {
		g.Log.Info("releasing L3VNIs", "commands", res.RemovedL3VNIs)
		if err := g.FRR.Apply(res.RemovedL3VNIs); err != nil {
			return res, fmt.Errorf("release L3VNIs: %w", err)
		}
	}
	if res.Removed != "" {
		g.Log.Info("removing stale configuration", "commands", res.Removed)
		// zebra tells bgpd asynchronously that an L3VNI is gone; until then
		// bgpd refuses to delete the VRF's instance
		err := g.FRR.Apply(res.Removed)
		for i := 0; err != nil && res.RemovedL3VNIs != "" && i < 10; i++ {
			time.Sleep(500 * time.Millisecond)
			err = g.FRR.Apply(res.Removed)
		}
		if err != nil {
			return res, fmt.Errorf("remove stale configuration: %w", err)
		}
	}
	if !g.SkipKernel {
		vrfs, err := removeStaleL3VNIs(g.Config, res.Identity)
		if err != nil {
			return res, fmt.Errorf("kernel: %w", err)
		}
		for _, vrf := range vrfs {
			g.Log.Info("removed provisioned vrf", "vrf", vrf)
			// best effort: an inactive, empty FRR VRF is harmless
			if err := g.FRR.Apply(frr.RemoveVRF(vrf)); err != nil {
				g.Log.Warn("remove FRR vrf", "vrf", vrf, "err", err)
			}
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
// Between reconciles it checks readiness every readyPoll. A change that holds
// for readyDebounce polls triggers a reconcile at once: the locator is
// withdrawn when the EVPN sessions go down and announced again once they have
// converged. The debounce keeps a transient reading (e.g. while frr-reload
// rewrites the config) from reconciling in the middle of someone else's
// change.
func (g *Gateway) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	poll := time.NewTicker(readyPoll)
	defer poll.Stop()
	initial := g.readiness()
	g.gate = &initial
	defer func() { g.gate = nil }()
	logged := false
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
		if err == nil && (!logged || res.Readiness.Ready != g.lastLogged) {
			logged, g.lastLogged = true, res.Readiness.Ready
			if res.Readiness.Ready {
				g.Log.Info("locator announced")
			} else {
				g.Log.Info("locator withheld", "reason", res.Readiness.Reason)
			}
		}
		changed := 0
	wait:
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				break wait
			case <-poll.C:
				r := g.readiness()
				if r.Ready == g.gate.Ready {
					changed = 0
					continue
				}
				if changed++; changed >= readyDebounce {
					g.gate = &r
					break wait
				}
			}
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
	if tv := g.Config.Transport.VRF; tv != "" && !base.VRFInstances[tv] {
		return id, fmt.Errorf("no BGP instance for the transport vrf %s (router bgp %d vrf %s) in the running configuration", tv, id.ASN, tv)
	}
	if !base.AdvertiseAllVNI {
		return id, fmt.Errorf("the provisioned L3VNIs need \"advertise-all-vni\" in the default BGP instance's l2vpn evpn address family")
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
