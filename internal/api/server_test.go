package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	opendciv1 "github.com/mwindower/open-dci/api/opendci/v1"
	"github.com/mwindower/open-dci/api/opendci/v1/opendciv1connect"
	"github.com/mwindower/open-dci/internal/config"
	"github.com/mwindower/open-dci/internal/gateway"
)

func testServer(t *testing.T) (*httptest.Server, *gateway.Gateway) {
	t.Helper()
	static, err := config.Load("../../lab/configs/gw-a1/open-dci.yaml")
	if err != nil {
		t.Fatal(err)
	}
	store, err := gateway.OpenAPIStore(filepath.Join(t.TempDir(), "api-networks.json"))
	if err != nil {
		t.Fatal(err)
	}
	gw := &gateway.Gateway{Config: static, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	gw.UseAPI(store)
	mon := &gateway.Monitor{}
	mon.Publish(gateway.Snapshot{
		Status:     &gateway.Status{ASN: 4200000016, RouterID: "10.0.0.16", Announced: true, Health: gateway.Health{OK: true}, Kernel: gateway.KernelStatus{StrictMode: "1"}},
		StatusTime: time.Now(), Interval: 10 * time.Second, Locator: gateway.LocatorAnnounced,
	})
	srv := httptest.NewUnstartedServer(Handler(&Server{Gateway: gw, Monitor: mon, Version: "v1.2.3"}, "secret"))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, gw
}

func bearer(token string) connect.Option {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, req)
		}
	}))
}

func code(err error) connect.Code {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code()
	}
	return 0
}

func TestAPI(t *testing.T) {
	srv, gw := testServer(t)
	ctx := context.Background()
	// real gRPC (HTTP/2), as grpcurl or a gRPC client would talk
	c := opendciv1connect.NewGatewayServiceClient(srv.Client(), srv.URL, connect.WithGRPC(), bearer("secret"))

	if _, err := opendciv1connect.NewGatewayServiceClient(srv.Client(), srv.URL, connect.WithGRPC(), bearer("wrong")).
		GetInfo(ctx, &opendciv1.GetInfoRequest{}); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("wrong token: %v", err)
	}
	if _, err := opendciv1connect.NewGatewayServiceClient(srv.Client(), srv.URL).
		GetInfo(ctx, &opendciv1.GetInfoRequest{}); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("no token: %v", err)
	}

	info, err := c.GetInfo(ctx, &opendciv1.GetInfoRequest{})
	if err != nil || info.GetAsn() != 4200000016 || !info.GetHealthy() || info.GetLocatorState() != "announced" || info.GetVersion() != "v1.2.3" {
		t.Fatalf("info: %+v %v", info, err)
	}

	set, err := c.SetNetworks(ctx, &opendciv1.SetNetworksRequest{Owner: "dci-controller", Networks: []*opendciv1.Network{
		{Vrf: "vrf3982", Vni: 3982, RouteTarget: "65535:26782482", Aggregates: []string{"10.0.17.0/24"}, Group: "tenant-2/main"},
		{Vrf: "vrf9", Vni: 3981, RouteTarget: "65535:9", Aggregates: []string{"10.9.0.0/24"}},
	}})
	if err != nil || !set.GetChanged() || len(set.GetSkipped()) != 1 || set.GetSkipped()[0].GetVrf() != "vrf9" {
		t.Fatalf("set: %+v %v", set, err)
	}

	list, err := c.ListNetworks(ctx, &opendciv1.ListNetworksRequest{})
	if err != nil || len(list.GetNetworks()) != 2 || len(list.GetSkipped()) != 1 {
		t.Fatalf("list: %+v %v", list, err)
	}
	cfgNet, apiNet := list.GetNetworks()[0], list.GetNetworks()[1]
	if cfgNet.GetOrigin() != "config" || apiNet.GetOrigin() != "api" || apiNet.GetOwner() != "dci-controller" ||
		apiNet.GetNetwork().GetSid() != 3982 || apiNet.GetNetwork().GetGroup() != "tenant-2/main" || apiNet.GetNetwork().GetCreated() == nil {
		t.Fatalf("list entries: %+v / %+v", cfgNet, apiNet)
	}

	for _, tc := range []struct {
		name string
		call func() error
		want connect.Code
	}{
		{"conflict", func() error {
			_, err := c.PutNetwork(ctx, &opendciv1.PutNetworkRequest{Owner: "ops", Network: &opendciv1.Network{Vrf: "vrf4000", Vni: 3982, RouteTarget: "65535:4000", Aggregates: []string{"10.4.0.0/24"}}})
			return err
		}, connect.CodeAlreadyExists},
		{"other owner", func() error {
			return deleteErr(c.DeleteNetwork(ctx, &opendciv1.DeleteNetworkRequest{Owner: "ops", Vrf: "vrf3982"}))
		}, connect.CodePermissionDenied},
		{"not found", func() error {
			return deleteErr(c.DeleteNetwork(ctx, &opendciv1.DeleteNetworkRequest{Owner: "ops", Vrf: "vrf7"}))
		}, connect.CodeNotFound},
		{"invalid", func() error {
			_, err := c.PutNetwork(ctx, &opendciv1.PutNetworkRequest{Owner: "ops", Network: &opendciv1.Network{Vrf: "vrf4001", Vni: 4001}})
			return err
		}, connect.CodeInvalidArgument},
		{"no network", func() error {
			_, err := c.PutNetwork(ctx, &opendciv1.PutNetworkRequest{Owner: "ops"})
			return err
		}, connect.CodeInvalidArgument},
	} {
		if err := tc.call(); code(err) != tc.want {
			t.Errorf("%s: want %v, got %v", tc.name, tc.want, err)
		}
	}

	// the Connect protocol works as well
	h1 := opendciv1connect.NewGatewayServiceClient(srv.Client(), srv.URL, bearer("secret"))
	if _, err := h1.PutNetwork(ctx, &opendciv1.PutNetworkRequest{Owner: "ops", Network: &opendciv1.Network{Vrf: "vrf4000", Vni: 4000, RouteTarget: "65535:4000", Aggregates: []string{"10.4.0.0/24"}}}); err != nil {
		t.Fatal(err)
	}
	if nets, _ := gw.Networks(); len(nets) != 3 {
		t.Fatalf("after put: %+v", nets)
	}
	if _, err := h1.DeleteNetwork(ctx, &opendciv1.DeleteNetworkRequest{Owner: "ops", Vrf: "vrf4000"}); err != nil {
		t.Fatal(err)
	}
}

func deleteErr(_ *opendciv1.DeleteNetworkResponse, err error) error { return err }

// Without the token, the gateway answers before it reads the request; with
// it, a request larger than MaxRequestBytes is refused.
func TestAuthBeforeBody(t *testing.T) {
	srv, _ := testServer(t)
	ctx := context.Background()
	huge := &opendciv1.SetNetworksRequest{Owner: "x", Networks: []*opendciv1.Network{{Vrf: strings.Repeat("v", MaxRequestBytes)}}}
	for _, opt := range []connect.ClientOption{connect.WithGRPC(), connect.WithGRPCWeb(), connect.WithProtoJSON()} {
		if _, err := opendciv1connect.NewGatewayServiceClient(srv.Client(), srv.URL, opt).
			SetNetworks(ctx, huge); code(err) != connect.CodeUnauthenticated {
			t.Fatalf("no token: %v", err)
		}
	}
	if _, err := opendciv1connect.NewGatewayServiceClient(srv.Client(), srv.URL, connect.WithGRPC(), bearer("secret")).
		SetNetworks(ctx, huge); code(err) != connect.CodeResourceExhausted {
		t.Fatalf("too large: %v", err)
	}
	// a body that never ends: the answer comes anyway
	pr, pw := io.Pipe()
	defer pw.Close()
	go pw.Write(make([]byte, 1<<20))
	slow, _ := http.NewRequest(http.MethodPost, srv.URL+opendciv1connect.GatewayServiceSetNetworksProcedure, pr)
	slow.Header.Set("Content-Type", "application/grpc")
	done := make(chan *http.Response, 1)
	go func() {
		resp, err := srv.Client().Do(slow)
		if err != nil {
			t.Error(err)
		}
		done <- resp
	}()
	select {
	case resp := <-done:
		if resp != nil {
			resp.Body.Close()
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no answer before the body ended")
	}
	// the reflection service needs the token too
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/grpc.reflection.v1.ServerReflection/ServerReflectionInfo", nil)
	req.Header.Set("Content-Type", "application/grpc")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body) // the trailers follow the body
	resp.Body.Close()
	if got := resp.Header.Get("Grpc-Status") + resp.Trailer.Get("Grpc-Status"); got != "16" { // unauthenticated
		t.Fatalf("reflection without token: grpc-status %q", got)
	}
}
