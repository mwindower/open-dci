package controller

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/metal-stack/api/go/client"
	apiv2 "github.com/metal-stack/api/go/metalstack/api/v2"

	"github.com/mwindower/open-dci/internal/api"
	"github.com/mwindower/open-dci/internal/config"
	"github.com/mwindower/open-dci/internal/gateway"
	"github.com/mwindower/open-dci/internal/metalstack"
	"github.com/mwindower/open-dci/internal/metalstack/metalstacktest"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// testGateway runs a gateway's API in-process (store and merge, no FRR).
func testGateway(t *testing.T, cfgFile string) (*gateway.Gateway, *httptest.Server) {
	t.Helper()
	static, err := config.Load(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	store, err := gateway.OpenAPIStore(filepath.Join(t.TempDir(), "api-networks.json"))
	if err != nil {
		t.Fatal(err)
	}
	gw := &gateway.Gateway{Config: static, Log: quiet}
	gw.UseAPI(store)
	srv := httptest.NewServer(api.Handler(&api.Server{Gateway: gw, Monitor: &gateway.Monitor{}}, "gw-secret"))
	t.Cleanup(srv.Close)
	return gw, srv
}

func label(v string) map[string]string { return map[string]string{"open-dci.io/stitch": v} }

func vrfs(gw *gateway.Gateway) string {
	nets, _ := gw.Networks()
	var out []string
	for _, n := range nets {
		out = append(out, n.Network.VRF)
	}
	return strings.Join(out, " ")
}

func TestController(t *testing.T) {
	token := filepath.Join(t.TempDir(), "gw-token")
	os.WriteFile(token, []byte("gw-secret\n"), 0o600)
	net := metalstacktest.Network
	tenant2 := []*apiv2.Network{
		net("t2-a", "tenant-2", "a", 3982, label("main"), "10.0.17.0/24"),
		net("t2-b", "tenant-2", "b", 4012, label("main"), "10.0.33.0/24"),
		net("t2-c", "tenant-2", "c", 5012, label("main"), "10.0.49.0/24"), // no gateway in c
	}
	dup := []*apiv2.Network{
		net("d1", "tenant-4", "b", 4021, label("dup"), "10.4.32.0/24"),
		net("d2", "tenant-4", "b", 4022, label("dup"), "10.4.33.0/24"),
	}
	unlabelled := net("t2-b", "tenant-2", "b", 4012, nil, "10.0.33.0/24")
	// one answer per round, in order
	metal := &metalstack.Client{URL: "http://metal-api.test", Interceptors: []connect.Interceptor{
		client.NewTestInterceptor(t, []client.ClientCall{
			metalstacktest.List(append(tenant2, dup...)...),
			metalstacktest.Unavailable(),
			metalstacktest.List(tenant2[0], unlabelled, tenant2[2]),
		}),
	}}

	gwA, srvA := testGateway(t, "../../lab/configs/gw-a1/open-dci.yaml")
	gwB, srvB := testGateway(t, "../../lab/configs/gw-b1/open-dci.yaml")
	cfg := &Config{
		MetalStack:       MetalStack{URL: metal.URL, Label: "open-dci.io/stitch", RouteTargetASN: 65535},
		Interval:         config.Duration{Duration: time.Minute},
		Owner:            DefaultOwner,
		GatewayTokenFile: token,
		Gateways: []Gateway{
			{Name: "gw-a1", Partition: "a", Endpoint: srvA.URL},
			{Name: "gw-b1", Partition: "b", Endpoint: srvB.URL},
			{Name: "gw-x", Partition: "b", Endpoint: "http://127.0.0.1:1"}, // unreachable
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	c, err := New(cfg, metal, quiet)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := c.Round(ctx); err != nil {
		t.Fatal(err)
	}
	if got := vrfs(gwA); got != "vrf3981 vrf3982" {
		t.Fatalf("gw-a1: %s", got)
	}
	if got := vrfs(gwB); got != "vrf4011 vrf4012" {
		t.Fatalf("gw-b1: %s", got)
	}
	nets, _ := gwA.Networks()
	if n := nets[1]; !n.API || n.Owner != DefaultOwner || n.Network.Group != "tenant-2/main" || n.Network.RouteTarget != "65535:26782482" ||
		len(n.Network.Aggregates) != 3 || n.Network.SID != 3982 {
		t.Fatalf("gw-a1's tenant 2: %+v", n)
	}
	st := c.State()
	if st.Groups != 1 || st.Gateways["gw-x"].PushErrors != 1 || st.Gateways["gw-a1"].Networks != 1 || st.Gateways["gw-b1"].LastError != "" {
		t.Fatalf("state: %+v", st)
	}
	reasons := map[string]string{}
	for _, s := range st.Skipped {
		reasons[s.Group] = s.Reason
	}
	if !strings.Contains(reasons["tenant-4/dup"], "2 networks in partition b") || len(reasons) != 1 {
		t.Fatalf("skipped: %+v", st.Skipped)
	}
	if strings.Join(st.Unserved, ",") != "tenant-2/main in c" {
		t.Fatalf("unserved: %v", st.Unserved)
	}

	// metal-apiserver down: nothing is pushed, the gateways keep their networks
	if err := c.Round(ctx); err == nil {
		t.Fatal("round without metal-apiserver succeeded")
	}
	if got := vrfs(gwA); got != "vrf3981 vrf3982" {
		t.Fatalf("gw-a1 changed during the outage: %s", got)
	}

	// a label removed: only that partition loses the network
	if err := c.Round(ctx); err != nil {
		t.Fatal(err)
	}
	if a, b := vrfs(gwA), vrfs(gwB); a != "vrf3981 vrf3982" || b != "vrf4011" {
		t.Fatalf("after the label change: gw-a1 %s, gw-b1 %s", a, b)
	}

	// metrics and health
	h := Handler(c, "v1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(rec.Result().Body)
	for _, s := range []string{
		"dci_controller_healthy 1",
		"dci_controller_errors_total 1",
		`dci_controller_gateway_networks{gateway="gw-a1",partition="a"} 1`,
		`dci_controller_gateway_up{gateway="gw-x"} 0`,
		`dci_controller_gateway_push_errors_total{gateway="gw-x"} 2`,
		`dci_controller_unserved{group="tenant-2/main",partition="c"} 1`,
	} {
		if !strings.Contains(string(body), s+"\n") {
			t.Errorf("metrics: missing %q", s)
		}
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	var health Health
	if err := json.NewDecoder(rec.Result().Body).Decode(&health); err != nil || rec.Code != 200 || !health.Healthy || len(health.Warnings) == 0 {
		t.Fatalf("/healthz %d %+v %v", rec.Code, health, err)
	}
}

func TestConfig(t *testing.T) {
	for _, f := range []string{"../../deploy/dci-controller.yaml", "../../lab/configs/core/dci-controller.yaml"} {
		if _, err := Load(f); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	path := filepath.Join(t.TempDir(), "c.yaml")
	valid := `
metalStack: {url: "http://api:8080", label: open-dci.io/stitch, routeTargetASN: 65535}
gatewayTokenFile: /etc/token
gateways:
  - {name: gw-a1, partition: a, endpoint: "http://gw-a1:9470"}
`
	os.WriteFile(path, []byte(valid), 0o644)
	c, err := Load(path)
	if err != nil || c.Interval.Duration != DefaultInterval || c.Owner != DefaultOwner {
		t.Fatalf("valid: %+v %v", c, err)
	}
	for _, tc := range []struct{ from, to, want string }{
		{"routeTargetASN: 65535", "routeTargetASN: 4200000016", "2-byte"},
		{"label: open-dci.io/stitch, ", "", "metalStack.label"},
		{`endpoint: "http://gw-a1:9470"`, `endpoint: "gw-a1:9470"`, "endpoint"},
		{"gatewayTokenFile: /etc/token\n", "", "gatewayTokenFile"},
		{"partition: a, ", "", "partition"},
	} {
		os.WriteFile(path, []byte(strings.Replace(valid, tc.from, tc.to, 1)), 0o644)
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want error with %q, got %v", tc.want, tc.want, err)
		}
	}
}
