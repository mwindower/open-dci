package fakeapi_test

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	adminv2 "github.com/metal-stack/api/go/metalstack/admin/v2"

	"github.com/metal-stack/api/go/metalstack/admin/v2/adminv2connect"
	"github.com/mwindower/open-dci/internal/metalstack"
	"github.com/mwindower/open-dci/internal/metalstack/fakeapi"
)

// The lab's fixture is valid, and the server checks the token and goes down.
func TestServer(t *testing.T) {
	const fixture = "../../../lab/configs/core/metal-api.yaml"
	want, err := fakeapi.Load(fixture)
	if err != nil || len(want) == 0 || want[0].GetMeta().GetLabels().GetLabels()["open-dci.io/stitch"] == "" {
		t.Fatalf("%s: %v %v", fixture, want, err)
	}
	down := filepath.Join(t.TempDir(), "down")
	srv := httptest.NewServer((&fakeapi.Server{File: fixture, Down: down, Token: "secret"}).Handler())
	defer srv.Close()
	list := func(token string) (*adminv2.NetworkServiceListResponse, error) {
		return adminv2connect.NewNetworkServiceClient(srv.Client(), srv.URL, connect.WithInterceptors(metalstack.Bearer(token))).
			List(context.Background(), &adminv2.NetworkServiceListRequest{})
	}
	if resp, err := list("secret"); err != nil || len(resp.GetNetworks()) != len(want) {
		t.Fatalf("list: %v %v", resp, err)
	}
	if _, err := list("wrong"); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("wrong token: %v", err)
	}
	os.WriteFile(down, nil, 0o644)
	if _, err := list("secret"); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("down: %v", err)
	}
}
