//go:build e2e

package lab

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mwindower/open-dci/internal/metrics"
)

// The gateways run "open-dci run --listen :9469" (node.yaml): Prometheus
// metrics on /metrics, the health on /healthz.

// scrape returns a gateway's /metrics.
func scrape(gw string) (string, error) {
	return lab.Exec(gw, "wget", "-q", "-O-", "http://127.0.0.1:9469/metrics")
}

// healthz returns a gateway's /healthz: the HTTP status code and the body.
func healthz(gw string) (int, metrics.Health, error) {
	var h metrics.Health
	out, err := lab.Exec(gw, "sh", "-c", `printf 'GET /healthz HTTP/1.0\r\n\r\n' | nc 127.0.0.1 9469`)
	if err != nil {
		return 0, h, err
	}
	var code int
	if _, err := fmt.Sscanf(out, "HTTP/1.0 %d", &code); err != nil {
		return 0, h, fmt.Errorf("%s: %q", gw, out)
	}
	_, body, _ := strings.Cut(out, "\r\n\r\n")
	if err := json.Unmarshal([]byte(body), &h); err != nil {
		return code, h, fmt.Errorf("%s: %v: %q", gw, err, body)
	}
	return code, h, nil
}

// metricsContain returns a func for waitFor that checks a gateway's metrics
// for whole lines.
func metricsContain(gw string, lines ...string) func() error {
	return func() error {
		out, err := scrape(gw)
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for _, l := range strings.Split(out, "\n") {
			have[l] = true
		}
		for _, l := range lines {
			if !have[l] {
				return fmt.Errorf("%s: metric %q missing", gw, l)
			}
		}
		return nil
	}
}

// healthzSays returns a func for waitFor that checks /healthz.
func healthzSays(gw string, code int, locator string) func() error {
	return func() error {
		c, h, err := healthz(gw)
		if err != nil {
			return err
		}
		if c != code || h.Healthy != (code == 200) || h.Locator != locator {
			return fmt.Errorf("%s: /healthz %d %+v, want %d with locator %s", gw, c, h, code, locator)
		}
		return nil
	}
}

// Every gateway is healthy, announces its locator and reports its sessions
// to both exits and its networks.
func TestMetrics(t *testing.T) {
	for _, m := range machines {
		for _, gw := range m.gws {
			t.Run(gw+"/"+m.vrf, func(t *testing.T) {
				waitFor(t, converge, healthzSays(gw, 200, "announced"))
				waitFor(t, converge, metricsContain(gw,
					"open_dci_healthy 1",
					"open_dci_dataplane_healthy 1",
					`open_dci_locator_state{state="announced"} 1`,
					`open_dci_locator_state{state="withdrawn"} 0`,
					"open_dci_frr_missing_lines 0",
					"open_dci_kernel_vrf_strict_mode 1",
					`open_dci_peer_established{peer="uplink0"} 1`,
					`open_dci_peer_established{peer="uplink1"} 1`,
					fmt.Sprintf(`open_dci_network_l3vni_up{vni="%s",vrf="%s"} 1`, m.vni, m.vrf),
					fmt.Sprintf(`open_dci_network_sid{sid="%s",vrf="%s"} 1`, m.sid, m.vrf),
					fmt.Sprintf(`open_dci_network_no_best_path{vrf="%s"} 0`, m.vrf),
				))
				// at least the two remote partitions' aggregates
				waitFor(t, converge, func() error {
					out, err := scrape(gw)
					if err != nil {
						return err
					}
					var n int
					metric := fmt.Sprintf(`open_dci_network_prefixes{family="ipv4",origin="remote",vrf="%s"}`, m.vrf)
					for _, l := range strings.Split(out, "\n") {
						if v, ok := strings.CutPrefix(l, metric+" "); ok {
							fmt.Sscan(v, &n)
						}
					}
					if n < 2 {
						return fmt.Errorf("%s: %s is %d, want at least 2", gw, metric, n)
					}
					return nil
				})
			})
		}
	}
}
