package gateway

import (
	"sync"
	"time"
)

// Monitor publishes what "open-dci run" does and sees, for the metrics and
// health endpoint (package metrics). Run updates it from its own goroutine;
// readers get a copy, so they never call into FRR themselves and a scrape
// costs nothing.
type Monitor struct {
	mu   sync.Mutex
	snap Snapshot
}

// Locator states (Snapshot.Locator), as Run logs them.
const (
	LocatorAnnounced = "announced"
	LocatorWithheld  = "withheld"  // waiting for the fabric's EVPN routes
	LocatorWithdrawn = "withdrawn" // unhealthy, see Reason
	LocatorDrained   = "drained"   // open-dci drain
)

// LocatorStates lists the locator states.
var LocatorStates = []string{LocatorAnnounced, LocatorWithheld, LocatorWithdrawn, LocatorDrained}

// Snapshot is the state Run publishes.
type Snapshot struct {
	// Status is the last collected status; nil before the first one. It is
	// never changed after publishing.
	Status     *Status
	StatusErr  string // why the last collection failed
	StatusTime time.Time
	// Locator is the locator state of the last successful reconcile, Reason
	// why it isn't announced.
	Locator, Reason string
	// Counters since start.
	Reconciles, ReconcileErrors uint64
	Applies                     uint64 // reconciles that applied missing lines
	Removals                    uint64 // reconciles that removed stale lines
	Withdrawals                 uint64 // health checks that withdrew the gateway
	BestPathRecoveries          uint64 // prefixes without best path whose selection Run re-ran
	LastReconcile               time.Time
	LastError                   string
	// Interval is Run's reconcile interval: a status older than a few
	// intervals means Run is stuck.
	Interval time.Duration
}

// Snapshot returns a copy of the published state.
func (m *Monitor) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snap
}

// Publish replaces the published state.
func (m *Monitor) Publish(s Snapshot) { m.update(func(old *Snapshot) { *old = s }) }

func (m *Monitor) update(f func(*Snapshot)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	f(&m.snap)
}

// Stale reports whether the status is too old to be trusted: Run collects it
// after every reconcile, at least once per interval.
func (s Snapshot) Stale(now time.Time) bool {
	return s.Status == nil || now.Sub(s.StatusTime) > 3*s.Interval+30*time.Second
}

// Healthy: a fresh status without problems.
func (s Snapshot) Healthy(now time.Time) bool {
	return !s.Stale(now) && s.Status.Healthy()
}

// Problems explains Healthy.
func (s Snapshot) Problems(now time.Time) []string {
	switch {
	case s.Status == nil && s.StatusErr != "":
		return []string{"status: " + s.StatusErr}
	case s.Status == nil:
		return []string{"no status collected yet"}
	case s.Stale(now):
		return []string{"status is stale (collected " + s.StatusTime.UTC().Format(time.RFC3339) + ")"}
	}
	return s.Status.Problems()
}

// Warnings: see Status.Warnings.
func (s Snapshot) Warnings(now time.Time) []string {
	if s.Status == nil {
		return nil
	}
	return s.Status.Warnings(now)
}

// observe records one reconcile and collects the status.
func (g *Gateway) observe(res Result, err error, state, reason string) {
	if g.Monitor == nil {
		return
	}
	now := time.Now()
	g.Monitor.update(func(s *Snapshot) {
		s.Reconciles++
		if err != nil {
			s.ReconcileErrors++
			s.LastError = err.Error()
			return
		}
		s.LastReconcile, s.LastError = now, ""
		s.Locator, s.Reason = state, reason
		if res.Applied {
			s.Applies++
		}
		if res.Removed != "" {
			s.Removals++
		}
	})
	st, serr := g.Status("")
	g.Monitor.update(func(s *Snapshot) {
		if serr != nil {
			s.StatusErr = serr.Error()
			return
		}
		s.Status, s.StatusErr, s.StatusTime = st, "", time.Now()
	})
}
