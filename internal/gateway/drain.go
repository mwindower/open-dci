package gateway

import (
	"errors"
	"os"
	"path/filepath"
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
