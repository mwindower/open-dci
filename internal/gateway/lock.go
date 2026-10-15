package gateway

import (
	"os"
	"path/filepath"
	"syscall"
)

// Reconciles of several processes on one gateway ("open-dci run" and a
// drain, undrain or apply from the command line) must not overlap: both would
// send the same removals, and vtysh fails the second batch for lines that are
// already gone. Reconcile therefore holds an exclusive lock on a file next to
// the state file; the second one waits and then plans against what the first
// one left.

// LockFile serializes reconciles across processes.
func (g *Gateway) LockFile() string {
	return filepath.Join(filepath.Dir(g.StateFile), "lock")
}

// lock takes the reconcile lock (blocking) and returns its release; a no-op
// without a state file.
func (g *Gateway) lock() (func(), error) {
	if g.StateFile == "" {
		return func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(g.LockFile()), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(g.LockFile(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil // closing releases the lock
}
