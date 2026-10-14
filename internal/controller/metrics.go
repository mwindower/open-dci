package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Handler serves /metrics (Prometheus) and /healthz for the controller.
func Handler(c *Controller, version string) http.Handler {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}), &collector{c: c, version: version})
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeHealth(w, c.State(), time.Now(), c.cfg.Interval.Duration)
	})
	return mux
}

// Health is the body of /healthz.
type Health struct {
	Healthy  bool     `json:"healthy"`
	Problems []string `json:"problems,omitempty"`
	// Warnings don't make the controller unhealthy: a gateway that can't
	// be reached, groups that can't be stitched.
	Warnings []string  `json:"warnings,omitempty"`
	Synced   time.Time `json:"synced,omitzero"`
}

// writeHealth answers 200 while metal-apiserver answers (within three
// intervals), 503 otherwise.
func writeHealth(w http.ResponseWriter, s State, now time.Time, interval time.Duration) {
	h := Health{Synced: s.Synced}
	if s.Stale(now, interval) {
		h.Problems = append(h.Problems, fmt.Sprintf("metal-apiserver: no answer for %s: %s", 3*interval, s.LastError))
	}
	for name, g := range s.Gateways {
		if g.LastError != "" {
			h.Warnings = append(h.Warnings, fmt.Sprintf("gateway %s: %s", name, g.LastError))
		}
		for _, sk := range g.Skipped {
			h.Warnings = append(h.Warnings, fmt.Sprintf("gateway %s: %s (%s) not applied: %s", name, sk.VRF, sk.Group, sk.Reason))
		}
	}
	for _, sk := range s.Skipped {
		h.Warnings = append(h.Warnings, fmt.Sprintf("%s not stitched: %s", sk.Group, sk.Reason))
	}
	for _, u := range s.Unserved {
		h.Warnings = append(h.Warnings, "no gateways for "+u)
	}
	h.Healthy = len(h.Problems) == 0
	w.Header().Set("Content-Type", "application/json")
	if !h.Healthy {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(h)
}

func desc(name, help string, labels ...string) *prometheus.Desc {
	return prometheus.NewDesc("dci_controller_"+name, help, labels, nil)
}

var (
	infoDesc      = desc("info", "dci-controller version.", "version")
	healthyDesc   = desc("healthy", "1 if metal-apiserver answered within three intervals.")
	syncDesc      = desc("sync_timestamp_seconds", "When metal-apiserver last answered (0: never).")
	roundsDesc    = desc("rounds_total", "Reconcile rounds since start.")
	errorsDesc    = desc("errors_total", "Rounds in which metal-apiserver failed.")
	groupsDesc    = desc("groups", "Stitched networks (groups) found.")
	skippedDesc   = desc("skipped", "1 for each group that can't be stitched.", "group", "reason")
	unservedDesc  = desc("unserved", "1 for each network of a stitched group in a partition without gateways.", "group", "partition")
	gwNetsDesc    = desc("gateway_networks", "Networks set on a gateway by the last successful push.", "gateway", "partition")
	gwErrDesc     = desc("gateway_push_errors_total", "Failed pushes to a gateway.", "gateway")
	gwPushDesc    = desc("gateway_last_push_timestamp_seconds", "When a gateway was last pushed successfully (0: never).", "gateway")
	gwUpDesc      = desc("gateway_up", "1 if the last push to a gateway succeeded.", "gateway")
	gwSkippedDesc = desc("gateway_skipped", "1 for each network a gateway didn't apply.", "gateway", "vrf", "reason")
)

type collector struct {
	c       *Controller
	version string
}

func (k *collector) Describe(ch chan<- *prometheus.Desc) { prometheus.DescribeByCollect(k, ch) }

func (k *collector) Collect(ch chan<- prometheus.Metric) {
	s := k.c.State()
	gauge := func(d *prometheus.Desc, v float64, l ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, l...)
	}
	counter := func(d *prometheus.Desc, v float64, l ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.CounterValue, v, l...)
	}
	b := func(v bool) float64 {
		if v {
			return 1
		}
		return 0
	}
	ts := func(t time.Time) float64 {
		if t.IsZero() {
			return 0
		}
		return float64(t.Unix())
	}
	gauge(infoDesc, 1, k.version)
	gauge(healthyDesc, b(!s.Stale(time.Now(), k.c.cfg.Interval.Duration)))
	gauge(syncDesc, ts(s.Synced))
	counter(roundsDesc, float64(s.Rounds))
	counter(errorsDesc, float64(s.Errors))
	gauge(groupsDesc, float64(s.Groups))
	for _, sk := range s.Skipped {
		gauge(skippedDesc, 1, sk.Group, sk.Reason)
	}
	for _, u := range s.Unserved {
		group, partition, _ := strings.Cut(u, " in ")
		gauge(unservedDesc, 1, group, partition)
	}
	for name, g := range s.Gateways {
		gauge(gwNetsDesc, float64(g.Networks), name, g.Partition)
		counter(gwErrDesc, float64(g.PushErrors), name)
		gauge(gwPushDesc, ts(g.LastPush), name)
		gauge(gwUpDesc, b(!g.LastPush.IsZero() && g.LastError == ""), name)
		for _, sk := range g.Skipped {
			gauge(gwSkippedDesc, 1, name, sk.VRF, sk.Reason)
		}
	}
}
