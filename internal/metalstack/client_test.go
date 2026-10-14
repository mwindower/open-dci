package metalstack

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/mwindower/open-dci/internal/metalstack/fakeapi"
)

func writeFixture(t *testing.T, path string, nets ...fakeapi.Network) {
	t.Helper()
	raw, err := yaml.Marshal(fakeapi.Fixture{Networks: nets})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestClient(t *testing.T) {
	dir := t.TempDir()
	fixture, token := filepath.Join(dir, "networks.yaml"), filepath.Join(dir, "token")
	label := map[string]string{"dci/stitch": "web"}
	writeFixture(t, fixture,
		fakeapi.Network{ID: "b", Project: "p2", Partition: "b", VRF: 4012, Prefixes: []string{"10.0.33.0/24"}, Labels: label, Created: "2026-01-02T00:00:00Z"},
		fakeapi.Network{ID: "a", Project: "p2", Partition: "a", VRF: 3982, Prefixes: []string{"10.0.17.0/24"}, Labels: label, Created: "2026-01-01T00:00:00Z"},
	)
	os.WriteFile(token, []byte("secret\n"), 0o600)
	srv := httptest.NewServer((&fakeapi.Server{File: fixture, Token: "secret"}).Handler())
	defer srv.Close()

	c := &Client{URL: srv.URL, TokenFile: token}
	nets, err := c.Networks(context.Background())
	if err != nil || len(nets) != 2 || nets[0].GetId() != "a" {
		t.Fatalf("networks: %v %v", nets, err)
	}
	got, _ := Map(nets, Options{Label: "dci/stitch", RouteTargetASN: 65535}, "a")
	if len(got) != 1 || got[0].VRF != "vrf3982" || got[0].Group != "p2/web" || got[0].Created.Day() != 1 {
		t.Fatalf("mapped: %+v", got)
	}
	// a wrong token is refused
	os.WriteFile(token, []byte("wrong"), 0o600)
	if _, err := c.Networks(context.Background()); err == nil {
		t.Fatal("wrong token accepted")
	}
}
