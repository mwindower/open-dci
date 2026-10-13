package metalstack

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/mwindower/open-dci/internal/config"
	"github.com/mwindower/open-dci/internal/source/metalstack/fakeapi"
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

func TestSource(t *testing.T) {
	dir := t.TempDir()
	fixture, token, cache := filepath.Join(dir, "networks.yaml"), filepath.Join(dir, "token"), filepath.Join(dir, "state", "cache.json")
	label := map[string]string{"dci/stitch": "web"}
	a := fakeapi.Network{ID: "a", Project: "p2", Partition: "a", VRF: 3982, Prefixes: []string{"10.0.17.0/24"}, Labels: label}
	b := fakeapi.Network{ID: "b", Project: "p2", Partition: "b", VRF: 4012, Prefixes: []string{"10.0.33.0/24"}, Labels: label}
	writeFixture(t, fixture, a, b)
	os.WriteFile(token, []byte("secret\n"), 0o600)
	srv := httptest.NewServer((&fakeapi.Server{File: fixture, Token: "secret"}).Handler())
	defer srv.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	for _, mode := range []struct {
		projects []string
		label    string
		group    string
	}{
		{[]string{"p2"}, "", "project/p2"},               // project API
		{[]string{config.AllProjects}, "", "project/p2"}, // admin API
		{nil, "dci/stitch", "label/p2/web"},              // admin API
	} {
		os.Remove(cache)
		cfg := &config.MetalStackSource{URL: srv.URL, TokenFile: token, Partition: "a", RouteTargetASN: 65535, Projects: mode.projects, Label: mode.label}
		s := New(cfg, cache, log)
		if changed, err := s.Sync(context.Background()); err != nil || !changed {
			t.Fatalf("%v: sync: changed %v, %v", mode, changed, err)
		}
		nets, _, synced := s.Networks(nil)
		if len(nets) != 1 || nets[0].VRF != "vrf3982" || nets[0].SID != 3982 || nets[0].Group != mode.group || synced.IsZero() {
			t.Fatalf("%v: %+v", mode, nets)
		}
		if changed, err := s.Sync(context.Background()); err != nil || changed {
			t.Fatalf("%v: second sync: changed %v, %v", mode, changed, err)
		}
	}

	cfg := &config.MetalStackSource{URL: srv.URL, TokenFile: token, Partition: "a", RouteTargetASN: 65535, Projects: []string{"p2"}}
	s := New(cfg, cache, log)
	// the cache: known before the first sync
	if nets, _, synced := s.Networks(nil); len(nets) != 1 || synced.IsZero() {
		t.Fatalf("from cache: %+v", nets)
	}
	// a failure keeps the last answer
	os.WriteFile(token, []byte("wrong"), 0o600)
	if _, err := s.Sync(context.Background()); err == nil {
		t.Fatal("wrong token accepted")
	}
	if nets, _, _ := s.Networks(nil); len(nets) != 1 {
		t.Fatalf("after a failure: %+v", nets)
	}
	// only an answer without the network removes it
	os.WriteFile(token, []byte("secret"), 0o600)
	writeFixture(t, fixture, a)
	if changed, err := s.Sync(context.Background()); err != nil || !changed {
		t.Fatalf("sync: changed %v, %v", changed, err)
	}
	if nets, skipped, _ := s.Networks(nil); len(nets) != 0 || len(skipped) != 1 {
		t.Fatalf("after removal: %+v %+v", nets, skipped)
	}
	// a broken cache is ignored
	os.WriteFile(cache, []byte("{"), 0o644)
	if nets, _, synced := New(cfg, cache, log).Networks(nil); len(nets) != 0 || !synced.IsZero() {
		t.Fatalf("broken cache: %+v", nets)
	}
}
