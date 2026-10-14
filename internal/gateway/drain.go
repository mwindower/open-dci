package gateway

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// A drained gateway announces neither its locator nor its tenant VRFs' routes
// (type-5), so the exits and the fabric send it nothing and its partner
// carries all traffic: for planned maintenance. The state is a marker file
// next to the state file, so that a running "open-dci run" keeps it.

// DrainFile is the marker of a drained gateway.
func (g *Gateway) DrainFile() string {
	return filepath.Join(filepath.Dir(g.StateFile), "drained")
}

// Drained reports whether the gateway is drained.
func (g *Gateway) Drained() bool {
	_, err := os.Stat(g.DrainFile())
	return err == nil
}

// SetDrained drains or undrains the gateway; Reconcile applies it.
func (g *Gateway) SetDrained(drained bool) error {
	if !drained {
		if err := os.Remove(g.DrainFile()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(g.DrainFile()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(g.DrainFile(), []byte("drained by open-dci drain\n"), 0o644)
}

// WithdrawnFile marks a gateway that "open-dci run" withdrew because it is
// unhealthy, with the reason. run keeps the state in memory; the file lets
// the other commands (status, diff) see it, so they don't take the withdrawn
// announcements for drift.
func (g *Gateway) WithdrawnFile() string {
	return filepath.Join(filepath.Dir(g.StateFile), "withdrawn")
}

// withdrawnMarker returns the reason in the marker, if there is one.
func (g *Gateway) withdrawnMarker() (string, bool) {
	raw, err := os.ReadFile(g.WithdrawnFile())
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(raw)), true
}

// markWithdrawn writes (h != nil) or removes the marker; errors are only
// logged: the marker serves the other commands, not run.
func (g *Gateway) markWithdrawn(h *Health) {
	var err error
	if h == nil {
		if err = os.Remove(g.WithdrawnFile()); errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	} else if err = os.MkdirAll(filepath.Dir(g.WithdrawnFile()), 0o755); err == nil {
		err = os.WriteFile(g.WithdrawnFile(), []byte(h.Reason+"\n"), 0o644)
	}
	if err != nil {
		g.Log.Warn("withdrawn marker", "err", err)
	}
}
