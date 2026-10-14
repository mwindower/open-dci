package metrics

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mwindower/open-dci/internal/config"
	"github.com/mwindower/open-dci/internal/gateway"
	"github.com/mwindower/open-dci/internal/kernel"
)

func healthyStatus() *gateway.Status {
	return &gateway.Status{
		ASN: 4200000016, RouterID: "10.0.0.16", Loopback: "fd00:dc1:ff::a1", Locator: "fd00:dc1:a::/48",
		Announced: true, Health: gateway.Health{OK: true},
		Kernel: gateway.KernelStatus{
			TransportVRF: "vrf104100", VethUp: true, DCIPathMTU: 9166, RequiredMTU: 9166, LocalRuleLast: true, StrictMode: "1",
			Filter: []kernel.FilterCounter{{Name: "tenant-to-block", Packets: 7}}, FilterRequired: 1,
		},
		Peers: []gateway.PeerStatus{{Address: "uplink0", State: "Established", V4Accepted: 6, V4Sent: 2}},
		Networks: []gateway.NetworkStatus{{
			VRF: "vrf3981", VNI: 3981, L3VNIState: "Up", SID: "fd00:dc1:a:f8d::", LocalV4: 2, RemoteV4: 3,
		}},
	}
}

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	body, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, string(body)
}

func TestMetrics(t *testing.T) {
	mon := &gateway.Monitor{}
	now := time.Now()
	mon.Publish(gateway.Snapshot{
		Status: healthyStatus(), StatusTime: now, Interval: 10 * time.Second,
		Locator: gateway.LocatorAnnounced, Reconciles: 5, Applies: 1, Withdrawals: 2, LastReconcile: now,
	})
	code, body := get(t, Handler(mon, "v1.2.3"), "/metrics")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	for _, s := range []string{
		`open_dci_info{asn="4200000016",locator="fd00:dc1:a::/48",loopback="fd00:dc1:ff::a1",router_id="10.0.0.16",version="v1.2.3"} 1`,
		"open_dci_healthy 1",
		`open_dci_locator_state{state="announced"} 1`,
		`open_dci_locator_state{state="withdrawn"} 0`,
		"open_dci_reconciles_total 5",
		"open_dci_frr_applies_total 1",
		"open_dci_withdrawals_total 2",
		"open_dci_frr_missing_lines 0",
		`open_dci_peer_established{peer="uplink0"} 1`,
		`open_dci_peer_vpn_prefixes{direction="accepted",family="ipv4",peer="uplink0"} 6`,
		`open_dci_network_l3vni_up{vni="3981",vrf="vrf3981"} 1`,
		`open_dci_network_prefixes{family="ipv4",origin="remote",vrf="vrf3981"} 3`,
		`open_dci_filter_dropped_packets_total{rule="tenant-to-block"} 7`,
		"open_dci_transport_path_mtu_bytes 9166",
		"go_goroutines",
	} {
		if !strings.Contains(body, s+"\n") && !strings.Contains(body, s+" ") {
			t.Errorf("missing %q", s)
		}
	}
	if t.Failed() {
		t.Log(body)
	}
}

func TestHealthz(t *testing.T) {
	now := time.Now()
	unhealthy := healthyStatus()
	unhealthy.Peers[0].State = "Active"
	for _, c := range []struct {
		name     string
		snap     gateway.Snapshot
		code     int
		problems string
	}{
		{"healthy", gateway.Snapshot{Status: healthyStatus(), StatusTime: now, Interval: 10 * time.Second, Locator: "announced"}, 200, ""},
		{"peer down", gateway.Snapshot{Status: unhealthy, StatusTime: now, Interval: 10 * time.Second}, 503, "peer uplink0: Active"},
		{"stale", gateway.Snapshot{Status: healthyStatus(), StatusTime: now.Add(-time.Hour), Interval: 10 * time.Second}, 503, "stale"},
		{"no status yet", gateway.Snapshot{}, 503, "no status collected yet"},
		{"status failed", gateway.Snapshot{StatusErr: "vtysh: exit 1"}, 503, "vtysh: exit 1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			mon := &gateway.Monitor{}
			mon.Publish(c.snap)
			code, body := get(t, Handler(mon, "dev"), "/healthz")
			var h Health
			if err := json.Unmarshal([]byte(body), &h); err != nil {
				t.Fatal(err)
			}
			if code != c.code || h.Healthy != (c.code == 200) || !strings.Contains(strings.Join(h.Problems, "; "), c.problems) {
				t.Errorf("got %d %s, want %d with %q", code, body, c.code, c.problems)
			}
		})
	}
}

func TestAPIMetrics(t *testing.T) {
	mon := &gateway.Monitor{}
	now := time.Now()
	st := healthyStatus()
	st.API = &gateway.APIStatus{
		Known: true, Networks: 3, LastChange: now.Add(-time.Hour),
		Skipped: []config.Skipped{{VRF: "vrf9", Group: "p1/dup", Reason: "vni: 3981 used twice"}},
	}
	mon.Publish(gateway.Snapshot{Status: st, StatusTime: now, Interval: 10 * time.Second, Locator: gateway.LocatorAnnounced})
	h := Handler(mon, "v1")
	_, body := get(t, h, "/metrics")
	for _, s := range []string{
		"open_dci_api_networks 3",
		`open_dci_api_skipped{group="p1/dup",reason="vni: 3981 used twice",vrf="vrf9"} 1`,
		"open_dci_healthy 1", // a skipped API network doesn't make the gateway unhealthy
	} {
		if !strings.Contains(body, s+"\n") {
			t.Errorf("missing %q", s)
		}
	}
	code, body := get(t, h, "/healthz")
	var health Health
	if err := json.Unmarshal([]byte(body), &health); err != nil {
		t.Fatal(err)
	}
	if code != http.StatusOK || len(health.Warnings) != 1 || !strings.Contains(health.Warnings[0], "vrf9") {
		t.Fatalf("/healthz %d %+v", code, health)
	}
}
