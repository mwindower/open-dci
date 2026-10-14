package metalstack

import (
	"context"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	adminv2 "github.com/metal-stack/api/go/metalstack/admin/v2"
	"github.com/metal-stack/api/go/metalstack/admin/v2/adminv2connect"
	apiv2 "github.com/metal-stack/api/go/metalstack/api/v2"
)

// Client lists metal-apiserver's private networks (admin API: labels can't
// be filtered by key on the server, so all of them).
type Client struct {
	URL string
	// TokenFile holds the API token (admin viewer role); re-read on every
	// call, so it can be rotated. Empty: no token.
	TokenFile string
	HTTP      *http.Client
}

// Networks returns the private networks (type child), sorted by ID.
func (c *Client) Networks(ctx context.Context) ([]*apiv2.Network, error) {
	opts := []connect.ClientOption{}
	if c.TokenFile != "" {
		token, err := os.ReadFile(c.TokenFile)
		if err != nil {
			return nil, err
		}
		opts = append(opts, connect.WithInterceptors(Bearer(strings.TrimSpace(string(token)))))
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	child := apiv2.NetworkType_NETWORK_TYPE_CHILD
	resp, err := adminv2connect.NewNetworkServiceClient(hc, c.URL, opts...).
		List(ctx, &adminv2.NetworkServiceListRequest{Query: &apiv2.NetworkQuery{Type: &child}})
	if err != nil {
		return nil, err
	}
	nets := resp.GetNetworks()
	slices.SortFunc(nets, func(a, b *apiv2.Network) int { return strings.Compare(a.GetId(), b.GetId()) })
	return nets, nil
}

// Bearer adds "Authorization: Bearer <token>" to every call.
func Bearer(token string) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, req)
		}
	}
}
