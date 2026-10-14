//go:build e2e

package lab

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/mwindower/open-dci/lab/internal/labtest"

	"github.com/mwindower/open-dci/internal/metalstack/fakeapi"
)

// Tenant 2 comes through the gateways' API: dci-controller on the core reads
// the fake metal-apiserver (configs/core/metal-api.yaml), where project
// tenant-2's private networks are labelled open-dci.io/stitch=main, one per
// partition, and sets every gateway its partition's network. All other tests
// use tenant 2 like a static network; these check the controller and the API.
// Tests change metal-apiserver's answer with an override file and simulate
// an outage with a "down" file (lab/cmd/fakeapi).

const (
	apiOverride = "/run/fakeapi/networks.yaml"
	apiDown     = "/run/fakeapi/down"
	stitchLabel = "open-dci.io/stitch"
	tenant2RT   = "65535:26782482" // derived from "tenant-2/main"
)

// tenant2 returns tenant 2's networks as in configs/core/metal-api.yaml,
// labelled only in the given partitions.
func tenant2(partitions ...string) []fakeapi.Network {
	var out []fakeapi.Network
	for _, n := range []struct {
		partition string
		vrf       uint32
		v4, v6    string
	}{{"a", 3982, "10.0.17.0/24", "2001:db8:17::/48"}, {"b", 4012, "10.0.33.0/24", "2001:db8:33::/48"}, {"c", 5012, "10.0.49.0/24", "2001:db8:49::/48"}} {
		net := fakeapi.Network{ID: "tenant-2-" + n.partition, Project: "tenant-2", Partition: n.partition, VRF: n.vrf, Prefixes: []string{n.v4, n.v6}}
		for _, p := range partitions {
			if p == n.partition {
				net.Labels = map[string]string{stitchLabel: "main"}
			}
		}
		out = append(out, net)
	}
	return out
}

// setAPI makes the fake metal-apiserver answer with nets.
func setAPI(t *testing.T, nets ...fakeapi.Network) {
	t.Helper()
	raw, err := yaml.Marshal(fakeapi.Fixture{Networks: nets})
	if err != nil {
		t.Fatal(err)
	}
	cmd := "mkdir -p /run/fakeapi && cat > " + apiOverride + ".tmp <<'EOF'\n" + string(raw) + "EOF\nmv " + apiOverride + ".tmp " + apiOverride
	if _, err := lab.Exec("core", "sh", "-c", cmd); err != nil {
		t.Fatal(err)
	}
}

// resetAPI makes it answer with the lab's networks again.
func resetAPI() { lab.Exec("core", "rm", "-f", apiOverride) }

// sourceStatus is the network source part of "open-dci status".
func sourceStatus(gw string) (networks map[string]string, err error) {
	out, err := lab.Exec(gw, "sh", "-c", "OPEN_DCI_OUTPUT=json /usr/local/bin/open-dci status -c /etc/open-dci/config.yaml || true")
	if err != nil {
		return nil, err
	}
	var st struct {
		Networks []struct{ VRF, Group, RouteTarget string }
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return nil, fmt.Errorf("%s: %v\n%s", gw, err, out)
	}
	networks = map[string]string{}
	for _, n := range st.Networks {
		if n.Group != "" {
			networks[n.VRF] = n.Group + " " + n.RouteTarget
		}
	}
	return networks, nil
}

// provisioned checks a source network's VRF on a gateway: kernel VRF and its
// devices, and the SID.
func provisioned(gw, vrf string, want bool) func() error {
	return func() error {
		var missing []string
		for _, dev := range []string{vrf, "dcibr" + vrf[3:], "dcivx" + vrf[3:]} {
			if _, err := lab.Exec(gw, "ip", "link", "show", dev); err != nil {
				missing = append(missing, dev)
			}
		}
		sids, _ := lab.Vtysh(gw, "show segment-routing srv6 sid")
		has := len(missing) == 0 && strings.Contains(sids, "'"+vrf+"'")
		gone := len(missing) == 3 && !strings.Contains(sids, "'"+vrf+"'")
		if (want && !has) || (!want && !gone) {
			return fmt.Errorf("%s: %s provisioned: want %v (missing devices %v)\n%s", gw, vrf, want, missing, sids)
		}
		return nil
	}
}

// ifindex returns a device's ifindex: unchanged means never rebuilt.
func ifindex(t *testing.T, gw, dev string) string {
	t.Helper()
	out, err := lab.Exec(gw, "cat", "/sys/class/net/"+dev+"/ifindex")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

// sidOf returns the SID FRR shows for a VRF.
func sidOf(gw, vrf string) string {
	out, _ := lab.Vtysh(gw, "show segment-routing srv6 sid")
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) > 2 && strings.Contains(l, "'"+vrf+"'") {
			return f[0]
		}
	}
	return ""
}

// metricValue returns a gateway's metric (a whole series name).
func metricValue(t *testing.T, gw, series string) float64 {
	t.Helper()
	out, err := scrape(gw)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(l, series+" "); ok {
			f, _ := strconv.ParseFloat(v, 64)
			return f
		}
	}
	t.Fatalf("%s: no %s", gw, series)
	return 0
}

// pinger pings continuously in batches until stopped and reports the loss.
type pinger struct {
	from, to       string
	stop           chan struct{}
	wg             sync.WaitGroup
	sent, received int
}

var pingStats = regexp.MustCompile(`(\d+) packets transmitted, (\d+) (?:packets )?received`)

func startPinger(from, to string) *pinger {
	p := &pinger{from: from, to: to, stop: make(chan struct{})}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			select {
			case <-p.stop:
				return
			default:
			}
			out, _ := lab.Exec(from, "ping", "-c", "25", "-i", "0.2", "-W", "1", to)
			if m := pingStats.FindStringSubmatch(out); m != nil {
				s, _ := strconv.Atoi(m[1])
				r, _ := strconv.Atoi(m[2])
				p.sent, p.received = p.sent+s, p.received+r
			}
		}
	}()
	return p
}

func (p *pinger) check(t *testing.T) {
	t.Helper()
	close(p.stop)
	p.wg.Wait()
	if p.sent == 0 || p.received != p.sent {
		t.Errorf("%s -> %s: %d of %d pings lost", p.from, p.to, p.sent-p.received, p.sent)
	} else {
		t.Logf("%s -> %s: %d pings, none lost", p.from, p.to, p.sent)
	}
}

// controllerMetrics returns dci-controller's /metrics (on the core).
func controllerMetrics() (string, error) {
	return lab.Exec("core", "wget", "-q", "-O-", "http://127.0.0.1:9471/metrics")
}

// controllerHealthz returns dci-controller's /healthz status code.
func controllerHealthz() (int, error) {
	out, err := lab.Exec("core", "sh", "-c", `printf 'GET /healthz HTTP/1.0\r\n\r\n' | nc 127.0.0.1 9471`)
	if err != nil {
		return 0, err
	}
	var code int
	if _, err := fmt.Sscanf(out, "HTTP/1.0 %d", &code); err != nil {
		return 0, fmt.Errorf("controller /healthz: %q", out)
	}
	return code, nil
}

// labReady waits until the lab has converged: every gateway healthy and
// both tenants reachable between all partitions. These tests run first
// (file order), right after the deploy, so they can't rely on the data-plane
// tests having waited.
func labReady(t *testing.T) {
	t.Helper()
	fns := map[string]func() error{}
	for _, gw := range allGateways() {
		fns[gw] = func() error { return opendci(gw, "status") }
	}
	waitAll(t, fns)
	pingFlows(t, "v4")
}

func allGateways() []string { return slicesConcat(slicesConcat(pairA, pairB), pairC) }

// waitAll waits for several conditions in parallel.
func waitAll(t *testing.T, fns map[string]func() error) {
	t.Helper()
	labtest.EventuallyAll(t, converge, fns)
}

// allProvisioned waits until every listed VRF is (or isn't) provisioned on
// its gateway, all in parallel.
func allProvisioned(t *testing.T, vrfs map[string][]string, want bool) {
	t.Helper()
	fns := map[string]func() error{}
	for gw, list := range vrfs {
		for _, vrf := range list {
			fns[gw+"/"+vrf] = provisioned(gw, vrf, want)
		}
	}
	waitAll(t, fns)
}

// counter reads a counter series from a node's metrics (port 9469 on the
// gateways, 9471 for dci-controller on the core).
func counter(t *testing.T, node, port, series string) float64 {
	t.Helper()
	out, err := lab.Exec(node, "wget", "-q", "-O-", "http://127.0.0.1:"+port+"/metrics")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(l, series+" "); ok {
			f, _ := strconv.ParseFloat(v, 64)
			return f
		}
	}
	t.Fatalf("%s: no %s", node, series)
	return 0
}

// waitControllerRounds waits until dci-controller has finished n more
// rounds: n=2 guarantees a whole round after a change of metal-apiserver's
// answer, instead of a fixed sleep.
func waitControllerRounds(t *testing.T, n float64) {
	t.Helper()
	target := counter(t, "core", "9471", "dci_controller_rounds_total") + n
	waitFor(t, converge, func() error {
		if got := counter(t, "core", "9471", "dci_controller_rounds_total"); got < target {
			return fmt.Errorf("controller: %v rounds, waiting for %v", got, target)
		}
		return nil
	})
}

// waitReconciles waits until a gateway has reconciled n more times.
func waitReconciles(t *testing.T, gw string, n float64) {
	t.Helper()
	target := counter(t, gw, "9469", "open_dci_reconciles_total") + n
	waitFor(t, converge, func() error {
		if got := counter(t, gw, "9469", "open_dci_reconciles_total"); got < target {
			return fmt.Errorf("%s: %v reconciles, waiting for %v", gw, got, target)
		}
		return nil
	})
}

func slicesConcat(a, b []string) []string { return append(append([]string{}, a...), b...) }

func TestControllerStitchesTenant2(t *testing.T) {
	for _, m := range []machine{mA2, mB2, mC2} {
		for _, gw := range m.gws {
			t.Run(gw, func(t *testing.T) {
				t.Parallel()
				waitFor(t, converge, func() error {
					nets, err := sourceStatus(gw)
					if err != nil {
						return err
					}
					if got, want := nets[m.vrf], "tenant-2/main "+tenant2RT; got != want || len(nets) != 1 {
						return fmt.Errorf("%s: source networks %v, want %s: %s", gw, nets, m.vrf, want)
					}
					return nil
				})
				waitFor(t, converge, metricsContain(gw, "open_dci_api_networks 1"))
			})
		}
	}
}

// Labels appear and disappear without interrupting anything else: a network
// leaving or joining tenant 2's group only changes that network's partition,
// the remaining members' VRFs are never rebuilt (same ifindex), other groups
// coming and going don't remove a single line of the others, and traffic
// between the untouched members never loses a packet.
func TestLabelChanges(t *testing.T) {
	labReady(t)
	defer resetAPI()
	idxA, idxB := ifindex(t, "gw-a1", mA2.vrf), ifindex(t, "gw-b1", mB2.vrf)
	tenant2Ping, tenant1Ping := startPinger("m-a2", mB2.v4), startPinger("m-a", mB.v4)

	// tenant 2 leaves partition C, and comes back
	setAPI(t, tenant2("a", "b")...)
	allProvisioned(t, map[string][]string{"gw-c1": {mC2.vrf}, "gw-c2": {mC2.vrf}}, false)
	waitFor(t, converge, func() error {
		out, _ := lab.Vtysh("gw-a1", "show ip prefix-list DCI-"+mA2.vrf+"-v4")
		if strings.Contains(out, mC2.net4) {
			return fmt.Errorf("gw-a1 still allows partition C's %s:\n%s", mC2.net4, out)
		}
		return nil
	})
	setAPI(t, tenant2("a", "b", "c")...)
	allProvisioned(t, map[string][]string{"gw-c1": {mC2.vrf}, "gw-c2": {mC2.vrf}}, true)
	waitFor(t, converge, func() error { return lab.Ping("m-a2", mC2.v4, 0) })

	// other groups come and go: not a single line of the others is removed
	removals := metricValue(t, "gw-a1", "open_dci_frr_removals_total")
	label := func(v string) map[string]string { return map[string]string{stitchLabel: v} }
	extra := []fakeapi.Network{
		{ID: "t3-a", Project: "tenant-3", Partition: "a", VRF: 3990, Prefixes: []string{"10.3.16.0/24"}, Labels: label("web")},
		{ID: "t3-b", Project: "tenant-3", Partition: "b", VRF: 4020, Prefixes: []string{"10.3.32.0/24"}, Labels: label("web")},
		// the same value in another project: a group of its own, alone in C
		{ID: "t7-c", Project: "tenant-7", Partition: "c", VRF: 5030, Prefixes: []string{"10.7.48.0/24"}, Labels: label("web")},
		// two networks with the same value in partition C: skipped everywhere
		{ID: "t4-a", Project: "tenant-4", Partition: "a", VRF: 3991, Prefixes: []string{"10.4.16.0/24"}, Labels: label("dup")},
		{ID: "t4-c1", Project: "tenant-4", Partition: "c", VRF: 5021, Prefixes: []string{"10.4.48.0/24"}, Labels: label("dup")},
		{ID: "t4-c2", Project: "tenant-4", Partition: "c", VRF: 5022, Prefixes: []string{"10.4.49.0/24"}, Labels: label("dup")},
		// VRF IDs beyond 16 bits: hashed SIDs, the same on both gateways of a pair
		{ID: "t5-a", Project: "tenant-5", Partition: "a", VRF: 100000, Prefixes: []string{"10.5.16.0/24"}, Labels: label("big")},
		{ID: "t5-c", Project: "tenant-5", Partition: "c", VRF: 100001, Prefixes: []string{"10.5.48.0/24"}, Labels: label("big")},
		// unlabelled: never stitched
		{ID: "t6-a", Project: "tenant-6", Partition: "a", VRF: 3993, Prefixes: []string{"10.6.16.0/24"}},
		{ID: "t6-b", Project: "tenant-6", Partition: "b", VRF: 4023, Prefixes: []string{"10.6.32.0/24"}},
	}
	setAPI(t, append(tenant2("a", "b", "c"), extra...)...)
	want := map[string][]string{
		"gw-a1": {"vrf3990", "vrf100000"}, "gw-a2": {"vrf3990", "vrf100000"},
		"gw-b1": {"vrf4020"}, "gw-b2": {"vrf4020"},
		"gw-c1": {"vrf5030", "vrf100001"}, "gw-c2": {"vrf5030", "vrf100001"},
	}
	allProvisioned(t, want, true)
	for gw := range want {
		for _, vrf := range []string{"vrf3991", "vrf5021", "vrf5022", "vrf3993", "vrf4023"} {
			if err := provisioned(gw, vrf, false)(); err != nil {
				t.Error(err)
			}
		}
	}
	for _, pair := range [][]string{pairA, pairC} {
		vrf := map[string]string{"gw-a1": "vrf100000", "gw-c1": "vrf100001"}[pair[0]]
		if s1, s2 := sidOf(pair[0], vrf), sidOf(pair[1], vrf); s1 == "" || s1 != s2 {
			t.Errorf("%s: SID %q on %s, %q on %s", vrf, s1, pair[0], s2, pair[1])
		}
	}
	waitFor(t, converge, func() error {
		out, err := controllerMetrics()
		if err != nil {
			return err
		}
		if !strings.Contains(out, `dci_controller_skipped{group="tenant-4/dup",reason="2 networks in partition c`) {
			return fmt.Errorf("controller: tenant-4/dup not reported as skipped")
		}
		return nil
	})
	if got := metricValue(t, "gw-a1", "open_dci_frr_removals_total"); got != removals {
		t.Errorf("gw-a1 removed lines while groups were only added (removals %v -> %v)", removals, got)
	}
	setAPI(t, tenant2("a", "b", "c")...)
	allProvisioned(t, want, false)

	tenant2Ping.check(t)
	tenant1Ping.check(t)
	if a, b := ifindex(t, "gw-a1", mA2.vrf), ifindex(t, "gw-b1", mB2.vrf); a != idxA || b != idxB {
		t.Errorf("tenant 2's VRFs were rebuilt: gw-a1 %s -> %s, gw-b1 %s -> %s", idxA, a, idxB, b)
	}

	// down to a lone member in A: it stays (same VRF), and B and C come back
	setAPI(t, tenant2("a")...)
	allProvisioned(t, map[string][]string{"gw-b1": {mB2.vrf}, "gw-b2": {mB2.vrf}, "gw-c1": {mC2.vrf}, "gw-c2": {mC2.vrf}}, false)
	waitControllerRounds(t, 2)    // the change has been pushed everywhere ...
	waitReconciles(t, "gw-a1", 1) // ... and reconciled on gw-a1
	if err := provisioned("gw-a1", mA2.vrf, true)(); err != nil {
		t.Error(err)
	}
	if a := ifindex(t, "gw-a1", mA2.vrf); a != idxA {
		t.Errorf("lone member rebuilt: gw-a1 %s %s -> %s", mA2.vrf, idxA, a)
	}
	resetAPI()
	waitFor(t, converge, func() error { return lab.Ping("m-a2", mB2.v4, 0) })
	waitFor(t, converge, func() error { return lab.Ping("m-a2", mC2.v4, 0) })
}

// While metal-apiserver is unreachable, the controller pushes nothing: the
// gateways keep their networks and stay healthy, the controller turns
// unhealthy and counts the errors.
func TestMetalAPIOutage(t *testing.T) {
	const gw = "gw-a1"
	defer lab.Exec("core", "rm", "-f", apiDown)
	if _, err := lab.Exec("core", "sh", "-c", "mkdir -p /run/fakeapi && touch "+apiDown); err != nil {
		t.Fatal(err)
	}
	waitFor(t, converge, func() error {
		if code, err := controllerHealthz(); err != nil || code != 503 {
			return fmt.Errorf("controller /healthz %d during the outage (%v)", code, err)
		}
		return nil
	})
	if c, h, err := healthz(gw); err != nil || c != 200 || !h.Healthy {
		t.Errorf("%s: /healthz %d %+v during the outage (%v)", gw, c, h, err)
	}
	if err := provisioned(gw, mA2.vrf, true)(); err != nil {
		t.Error(err)
	}
	if err := lab.Ping("m-a2", mB2.v4, 0); err != nil {
		t.Error(err)
	}
	if out, _ := controllerMetrics(); strings.Contains(out, "dci_controller_errors_total 0\n") {
		t.Error("no controller errors counted")
	}

	lab.Exec("core", "rm", "-f", apiDown)
	waitFor(t, converge, func() error {
		if code, err := controllerHealthz(); err != nil || code != 200 {
			return fmt.Errorf("controller /healthz %d after the outage (%v)", code, err)
		}
		return nil
	})
}

// While the controller is stopped, the gateways keep everything; a label
// change only takes effect once it runs again.
func TestControllerOutage(t *testing.T) {
	defer resetAPI()
	defer lab.Exec("core", "pkill", "-CONT", "-f", "dci-controller")
	if _, err := lab.Exec("core", "pkill", "-STOP", "-f", "dci-controller"); err != nil {
		t.Fatal(err)
	}
	setAPI(t, tenant2("a", "b")...) // tenant 2 leaves partition C
	time.Sleep(4 * time.Second)     // two controller intervals; the rounds counter stands still
	for _, gw := range pairC {
		if err := provisioned(gw, mC2.vrf, true)(); err != nil {
			t.Errorf("changed without the controller: %v", err)
		}
	}
	if err := lab.Ping("m-a2", mC2.v4, 0); err != nil {
		t.Error(err)
	}
	lab.Exec("core", "pkill", "-CONT", "-f", "dci-controller")
	pairCvrf := map[string][]string{"gw-c1": {mC2.vrf}, "gw-c2": {mC2.vrf}}
	allProvisioned(t, pairCvrf, false)
	resetAPI()
	allProvisioned(t, pairCvrf, true)
	waitFor(t, converge, func() error { return lab.Ping("m-a2", mC2.v4, 0) })
}

// apiCall calls a gateway's API from the core (where dci-controller runs),
// over the management network, with the Connect protocol (HTTP + JSON, via
// "labnode post"):
// the HTTP status and the JSON body. gRPC over HTTP/2 is covered by the
// unit tests (internal/api).
func apiCall(t *testing.T, gw, method, token string, req any) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(req)
	out, err := execStdin("core", string(body), "labnode", "post", "http://"+gw+":9470/opendci.v1.GatewayService/"+method,
		"Content-Type: application/json", "Authorization: Bearer "+token)
	if err != nil {
		t.Fatalf("%s %s: %v: %s", gw, method, err, out)
	}
	status, payload, _ := strings.Cut(out, "\n")
	code, err := strconv.Atoi(status)
	if err != nil {
		t.Fatalf("%s %s: %q", gw, method, out)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		t.Fatalf("%s %s: %v: %q", gw, method, err, payload)
	}
	return code, m
}

// execStdin runs a command in a lab node with stdin.
func execStdin(node, stdin string, args ...string) (string, error) {
	cmd := exec.Command("docker", append([]string{"exec", "-i", lab.Prefix + "-" + node}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	return string(out), err
}

// The gateway API: authentication, networks with their origin, and owners
// that don't overwrite each other: an operator's network ("e2e") stays while
// the controller keeps setting its own.
func TestAPI(t *testing.T) {
	labReady(t)
	const gw = "gw-b1"
	raw, err := os.ReadFile("configs/api-token")
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(string(raw))
	call := func(method string, req any) (int, map[string]any) { return apiCall(t, gw, method, token, req) }
	defer call("SetNetworks", map[string]any{"owner": "e2e"})

	if code, m := apiCall(t, gw, "GetInfo", "wrong", map[string]any{}); code != 401 || m["code"] != "unauthenticated" {
		t.Fatalf("wrong token: %d %v", code, m)
	}
	code, info := call("GetInfo", map[string]any{})
	if code != 200 || info["asn"] != float64(4200000026) || info["healthy"] != true || info["locatorState"] != "announced" {
		t.Fatalf("info: %d %v", code, info)
	}
	code, list := call("ListNetworks", map[string]any{})
	if code != 200 {
		t.Fatalf("list: %d %v", code, list)
	}
	origins := map[string]string{}
	for _, e := range list["networks"].([]any) {
		n := e.(map[string]any)
		net := n["network"].(map[string]any)
		owner, _ := n["owner"].(string)
		origins[net["vrf"].(string)] = n["origin"].(string) + "/" + owner
		if net["vrf"] == mB2.vrf && (n["l3vniUp"] != true || n["sidAddress"] == "" || net["routeTarget"] != tenant2RT) {
			t.Errorf("tenant 2: %v", n)
		}
	}
	if origins[mB.vrf] != "config/" || origins[mB2.vrf] != "api/dci-controller" {
		t.Fatalf("origins: %v", origins)
	}

	// an operator adds a network next to the controller's
	extra := map[string]any{"vrf": "vrf4050", "vni": 4050, "routeTarget": "65535:4050", "aggregates": []string{"10.50.32.0/24"}}
	if code, m := call("PutNetwork", map[string]any{"owner": "e2e", "network": extra}); code != 200 {
		t.Fatalf("put: %d %v", code, m)
	}
	waitFor(t, converge, provisioned(gw, "vrf4050", true))
	waitControllerRounds(t, 2) // whole controller rounds: they leave it alone
	waitReconciles(t, gw, 1)
	if err := provisioned(gw, "vrf4050", true)(); err != nil {
		t.Errorf("the controller removed another owner's network: %v", err)
	}
	conflict := map[string]any{"vrf": "vrf4051", "vni": 4012, "routeTarget": "65535:4051", "aggregates": []string{"10.51.32.0/24"}}
	if code, m := call("PutNetwork", map[string]any{"owner": "e2e", "network": conflict}); code != 409 || m["code"] != "already_exists" {
		t.Errorf("conflicting VNI: %d %v", code, m)
	}
	if code, m := call("DeleteNetwork", map[string]any{"owner": "e2e", "vrf": mB2.vrf}); code != 403 || m["code"] != "permission_denied" {
		t.Errorf("deleting the controller's network: %d %v", code, m)
	}
	code, set := call("SetNetworks", map[string]any{"owner": "e2e", "networks": []any{
		extra,
		map[string]any{"vrf": "vrf4052", "vni": 4011, "routeTarget": "65535:4052", "aggregates": []string{"10.52.32.0/24"}}, // tenant 1's VNI
	}})
	skipped, _ := set["skipped"].([]any)
	if code != 200 || len(skipped) != 1 || skipped[0].(map[string]any)["vrf"] != "vrf4052" {
		t.Errorf("set with a conflict: %d %v", code, set)
	}
	if code, m := call("DeleteNetwork", map[string]any{"owner": "e2e", "vrf": "vrf4050"}); code != 200 {
		t.Fatalf("delete: %d %v", code, m)
	}
	waitFor(t, converge, provisioned(gw, "vrf4050", false))
	waitFor(t, converge, func() error { return lab.Ping("m-b2", mA2.v4, 0) })
}
