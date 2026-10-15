package gateway

import (
	"path/filepath"
	"testing"
	"time"
)

// A second reconcile (another process: a lock of its own) waits until the
// first one releases the lock.
func TestReconcileLock(t *testing.T) {
	g := &Gateway{StateFile: filepath.Join(t.TempDir(), "applied.conf")}
	unlock, err := g.lock()
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan struct{})
	go func() {
		other := &Gateway{StateFile: g.StateFile}
		release, err := other.lock()
		if err != nil {
			t.Error(err)
		}
		release()
		close(got)
	}()
	select {
	case <-got:
		t.Fatal("second lock taken while the first is held")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("second lock not taken after release")
	}
}
