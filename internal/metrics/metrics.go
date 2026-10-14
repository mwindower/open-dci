// Package metrics serves what "open-dci run" publishes (gateway.Monitor) over
// HTTP: Prometheus metrics on /metrics and the gateway's health on /healthz.
// Both read the last published snapshot, so a scrape never calls into FRR.
package metrics

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/mwindower/open-dci/internal/gateway"
)

// Handler serves /metrics and /healthz for mon.
func Handler(mon *gateway.Monitor, version string) http.Handler {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		&collector{mon: mon, version: version, now: time.Now},
	)
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeHealth(w, mon.Snapshot(), time.Now())
	})
	return mux
}

// Health is the body of /healthz.
type Health struct {
	Healthy bool `json:"healthy"`
	// Locator: announced, withheld, withdrawn or drained; Reason why not
	// announced
	Locator  string   `json:"locator,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	Problems []string `json:"problems,omitempty"`
	// Warnings don't make the gateway unhealthy (e.g. an API network that
	// can't be applied).
	Warnings []string `json:"warnings,omitempty"`
	// Updated is when the status was collected.
	Updated time.Time `json:"updated,omitzero"`
}

// writeHealth answers 200 if the gateway is healthy (a drained one is), 503
// otherwise, with the reasons in the body.
func writeHealth(w http.ResponseWriter, s gateway.Snapshot, now time.Time) {
	h := Health{
		Healthy:  s.Healthy(now),
		Locator:  s.Locator,
		Reason:   s.Reason,
		Problems: s.Problems(now),
		Warnings: s.Warnings(now),
		Updated:  s.StatusTime,
	}
	w.Header().Set("Content-Type", "application/json")
	if !h.Healthy {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(h)
}

func desc(name, help string, labels ...string) *prometheus.Desc {
	return prometheus.NewDesc("open_dci_"+name, help, labels, nil)
}

var (
	infoDesc       = desc("info", "Gateway identity and open-dci version.", "version", "asn", "router_id", "loopback", "locator")
	healthyDesc    = desc("healthy", "1 if everything open-dci is responsible for is in place (as open-dci status / healthz).")
	statusTSDesc   = desc("status_timestamp_seconds", "When the status was last collected.")
	locatorDesc    = desc("locator_state", "1 for the locator's current state: announced, withheld (waiting for the fabric), withdrawn (unhealthy) or drained.", "state")
	healthOKDesc   = desc("dataplane_healthy", "1 if the data-plane checks pass; open-dci run withdraws the gateway otherwise.")
	missingDesc    = desc("frr_missing_lines", "Rendered FRR lines missing from the running configuration (drift).")
	reconcileDesc  = desc("reconciles_total", "Reconciles since start.")
	reconErrDesc   = desc("reconcile_errors_total", "Failed reconciles since start.")
	appliesDesc    = desc("frr_applies_total", "Reconciles that applied missing FRR lines (drift repaired).")
	removalsDesc   = desc("frr_removals_total", "Reconciles that removed stale FRR lines.")
	withdrawDesc   = desc("withdrawals_total", "Times the health checks withdrew the gateway.")
	lastReconDesc  = desc("last_reconcile_timestamp_seconds", "When the last successful reconcile finished.")
	peerUpDesc     = desc("peer_established", "1 if the VPN session is established.", "peer")
	peerPfxDesc    = desc("peer_vpn_prefixes", "VPN prefixes accepted from or sent to a peer.", "peer", "family", "direction")
	l3vniDesc      = desc("network_l3vni_up", "1 if the network's L3VNI is up.", "vrf", "vni")
	sidDesc        = desc("network_sid", "1 if the network has its End.DT46 SID.", "vrf", "sid")
	routesDesc     = desc("network_prefixes", "Best paths in the tenant VRF: local (own partition) or remote (VPN).", "vrf", "family", "origin")
	noBestDesc     = desc("network_no_best_path", "Prefixes with valid paths but none selected (a silent black hole).", "vrf")
	strictDesc     = desc("kernel_vrf_strict_mode", "1 if net.vrf.strict_mode=1 (required for End.DT46).")
	filterDesc     = desc("filter_dropped_packets_total", "Packets dropped by a rule of the SRv6 ingress filter.", "rule")
	filterRulesDsc = desc("filter_rules", "Rules installed in the SRv6 ingress filter.")
	filterReqDesc  = desc("filter_rules_required", "Rules the SRv6 ingress filter should have.")
	vethDesc       = desc("transport_veth_up", "1 if the veth to the transport VRF is up (DCI network mode).")
	pathMTUDesc    = desc("transport_path_mtu_bytes", "Smallest MTU on the transport VRF's devices (DCI network mode).")
	reqMTUDesc     = desc("transport_required_mtu_bytes", "MTU the transport needs (tenant MTU + SRv6).")
	ruleDesc       = desc("transport_local_rule_last", "1 if the local ip rule is behind l3mdev (DCI network mode).")
	apiNetsDesc    = desc("api_networks", "Networks applied from the API.")
	apiChangeDesc  = desc("api_last_change_timestamp_seconds", "When the API networks last changed (0: never).")
	apiSkipDesc    = desc("api_skipped", "1 for each API network that isn't applied.", "vrf", "group", "reason")
)

type collector struct {
	mon     *gateway.Monitor
	version string
	now     func() time.Time
}

func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	prometheus.DescribeByCollect(c, ch)
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func (c *collector) Collect(ch chan<- prometheus.Metric) {
	s := c.mon.Snapshot()
	gauge := func(d *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
	}
	counter := func(d *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.CounterValue, v, labels...)
	}
	now := c.now()

	gauge(healthyDesc, b2f(s.Healthy(now)))
	counter(reconcileDesc, float64(s.Reconciles))
	counter(reconErrDesc, float64(s.ReconcileErrors))
	counter(appliesDesc, float64(s.Applies))
	counter(removalsDesc, float64(s.Removals))
	counter(withdrawDesc, float64(s.Withdrawals))
	if !s.LastReconcile.IsZero() {
		gauge(lastReconDesc, float64(s.LastReconcile.Unix()))
	}
	if s.Locator != "" {
		for _, state := range gateway.LocatorStates {
			gauge(locatorDesc, b2f(state == s.Locator), state)
		}
	}
	st := s.Status
	if st == nil {
		gauge(infoDesc, 1, c.version, "", "", "", "")
		return
	}
	gauge(infoDesc, 1, c.version, strconv.FormatUint(uint64(st.ASN), 10), st.RouterID, st.Loopback, st.Locator)
	gauge(statusTSDesc, float64(s.StatusTime.Unix()))
	gauge(healthOKDesc, b2f(st.Health.OK))
	gauge(missingDesc, float64(st.MissingLines))

	for _, p := range st.Peers {
		gauge(peerUpDesc, b2f(p.State == "Established"), p.Address)
		for _, v := range []struct {
			family, dir string
			n           int
		}{{"ipv4", "accepted", p.V4Accepted}, {"ipv4", "sent", p.V4Sent}, {"ipv6", "accepted", p.V6Accepted}, {"ipv6", "sent", p.V6Sent}} {
			gauge(peerPfxDesc, float64(v.n), p.Address, v.family, v.dir)
		}
	}
	for _, n := range st.Networks {
		gauge(l3vniDesc, b2f(n.L3VNIState == "Up"), n.VRF, strconv.FormatUint(uint64(n.VNI), 10))
		gauge(sidDesc, b2f(n.SID != ""), n.VRF, n.SID)
		gauge(routesDesc, float64(n.LocalV4), n.VRF, "ipv4", "local")
		gauge(routesDesc, float64(n.LocalV6), n.VRF, "ipv6", "local")
		gauge(routesDesc, float64(n.RemoteV4), n.VRF, "ipv4", "remote")
		gauge(routesDesc, float64(n.RemoteV6), n.VRF, "ipv6", "remote")
		gauge(noBestDesc, float64(n.NoBestPath), n.VRF)
	}

	if api := st.API; api != nil {
		gauge(apiNetsDesc, float64(api.Networks))
		var changed float64
		if !api.LastChange.IsZero() {
			changed = float64(api.LastChange.Unix())
		}
		gauge(apiChangeDesc, changed)
		for _, sk := range api.Skipped {
			gauge(apiSkipDesc, 1, sk.VRF, sk.Group, sk.Reason)
		}
	}

	k := st.Kernel
	gauge(strictDesc, b2f(k.StrictMode == "1"))
	for _, f := range k.Filter {
		counter(filterDesc, float64(f.Packets), f.Name)
	}
	gauge(filterRulesDsc, float64(len(k.Filter)))
	gauge(filterReqDesc, float64(k.FilterRequired))
	if k.TransportVRF != "" {
		gauge(vethDesc, b2f(k.VethUp))
		gauge(pathMTUDesc, float64(k.DCIPathMTU))
		gauge(reqMTUDesc, float64(k.RequiredMTU))
		gauge(ruleDesc, b2f(k.LocalRuleLast))
	}
}
