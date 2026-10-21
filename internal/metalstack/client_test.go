package metalstack

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/metal-stack/api/go/client"
	apiv2 "github.com/metal-stack/api/go/metalstack/api/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/mwindower/open-dci/internal/metalstack/metalstacktest"
)

func TestClient(t *testing.T) {
	token := filepath.Join(t.TempDir(), "token")
	os.WriteFile(token, []byte("secret\n"), 0o600)
	created := func(n *apiv2.Network, day int) *apiv2.Network {
		n.Meta.CreatedAt = timestamppb.New(time.Date(2026, 1, day, 0, 0, 0, 0, time.UTC))
		return n
	}
	label := map[string]string{"dci/stitch": "web"}
	var auth string
	capture := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			auth = req.Header().Get("Authorization")
			return next(ctx, req)
		}
	})
	c := &Client{URL: "http://metal-api.test", TokenFile: token, Interceptors: []connect.Interceptor{capture,
		client.NewTestInterceptor(t, []client.ClientCall{
			metalstacktest.List(
				created(metalstacktest.Network("b", "p2", "b", 4012, label, "10.0.33.0/24"), 2),
				created(metalstacktest.Network("a", "p2", "a", 3982, label, "10.0.17.0/24"), 1),
			),
			metalstacktest.Unavailable(),
		}),
	}}

	nets, err := c.Networks(context.Background())
	if err != nil || len(nets) != 2 || nets[0].GetId() != "a" {
		t.Fatalf("networks: %v %v", nets, err)
	}
	if auth != "Bearer secret" {
		t.Fatalf("authorization: %q", auth)
	}
	got, _ := Map(nets, Options{Label: "dci/stitch", RouteTargetASN: 65535}, "a")
	if len(got) != 1 || got[0].VRF != "vrf3982" || got[0].Group != "p2/web" || got[0].Created.Day() != 1 {
		t.Fatalf("mapped: %+v", got)
	}
	if _, err := c.Networks(context.Background()); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("outage: %v", err)
	}
}
