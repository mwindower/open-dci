// Package controller is dci-controller: it reconciles metal-apiserver's
// private networks with the open-dci gateways. Every round it lists the
// networks once, derives each partition's stitched networks (labelled
// networks, grouped by project and label value; see internal/metalstack) and
// sets them on every gateway of that partition through the gateway API
// (SetNetworks, as owner "dci-controller").
//
// The gateways keep what they have when the controller or metal-apiserver
// is unavailable: a failed round changes nothing.
package controller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	apiv2 "github.com/metal-stack/api/go/metalstack/api/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	opendciv1 "github.com/mwindower/open-dci/api/opendci/v1"
	"github.com/mwindower/open-dci/api/opendci/v1/opendciv1connect"
	"github.com/mwindower/open-dci/internal/config"
	"github.com/mwindower/open-dci/internal/metalstack"
)

// Networks lists metal-apiserver's private networks.
type Networks interface {
	Networks(ctx context.Context) ([]*apiv2.Network, error)
}

// Controller reconciles the gateways with metal-apiserver.
type Controller struct {
	cfg      *Config
	metal    Networks
	gateways map[string]opendciv1connect.GatewayServiceClient
	log      *slog.Logger

	mu    sync.Mutex
	state State
}

// State is what the last rounds did, for metrics, health and logs.
type State struct {
	Rounds, Errors uint64
	// Synced: the last time metal-apiserver answered (zero: never)
	Synced    time.Time
	LastError string
	Groups    int
	// Skipped: groups that can't be stitched at all, with the reason
	Skipped []config.Skipped
	// Unserved: stitched groups with networks in partitions without
	// gateways ("<group> in <partition>"); those networks aren't reached
	Unserved []string
	Gateways map[string]GatewayState
}

// GatewayState is a gateway's state as the controller sees it.
type GatewayState struct {
	Partition  string
	Networks   int // set by the last successful push
	PushErrors uint64
	LastPush   time.Time
	LastError  string
	// Skipped: networks the gateway didn't apply (e.g. a conflict with its
	// config file)
	Skipped []config.Skipped
}

// New returns a controller for cfg; metal lists the networks (nil:
// metal-apiserver as configured).
func New(cfg *Config, metal Networks, log *slog.Logger) (*Controller, error) {
	if metal == nil {
		metal = &metalstack.Client{URL: cfg.MetalStack.URL, TokenFile: cfg.MetalStack.TokenFile}
	}
	c := &Controller{cfg: cfg, metal: metal, gateways: map[string]opendciv1connect.GatewayServiceClient{}, log: log,
		state: State{Gateways: map[string]GatewayState{}}}
	for _, g := range cfg.Gateways {
		hc, err := httpClient(g)
		if err != nil {
			return nil, fmt.Errorf("gateway %s: %w", g.Name, err)
		}
		c.gateways[g.Name] = opendciv1connect.NewGatewayServiceClient(hc, g.Endpoint,
			connect.WithInterceptors(tokenFrom(cfg.GatewayTokenFile)))
		c.state.Gateways[g.Name] = GatewayState{Partition: g.Partition}
	}
	return c, nil
}

func httpClient(g Gateway) (*http.Client, error) {
	hc := &http.Client{Timeout: 20 * time.Second}
	if g.CAFile == "" {
		return hc, nil
	}
	pem, err := os.ReadFile(g.CAFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%s: no certificates", g.CAFile)
	}
	hc.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	return hc, nil
}

// tokenFrom adds the gateways' bearer token, read from file on every call
// (so it can be rotated).
func tokenFrom(file string) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			raw, err := os.ReadFile(file)
			if err != nil {
				return nil, err
			}
			req.Header().Set("Authorization", "Bearer "+strings.TrimSpace(string(raw)))
			return next(ctx, req)
		}
	}
}

// State returns a copy of the controller's state.
func (c *Controller) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.state
	s.Gateways = maps.Clone(c.state.Gateways)
	s.Skipped = slices.Clone(c.state.Skipped)
	s.Unserved = slices.Clone(c.state.Unserved)
	return s
}

// Run reconciles every interval until ctx is done.
func (c *Controller) Run(ctx context.Context) {
	t := time.NewTicker(c.cfg.Interval.Duration)
	defer t.Stop()
	for {
		if err := c.Round(ctx); err != nil {
			c.log.Error("round", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Round reconciles once: it lists the networks and sets every gateway's.
// If metal-apiserver doesn't answer, nothing is pushed: the gateways keep
// their networks. A failing gateway doesn't stop the others.
func (c *Controller) Round(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Interval.Duration)
	defer cancel()
	nets, err := c.metal.Networks(ctx)
	c.mu.Lock()
	c.state.Rounds++
	if err != nil {
		c.state.Errors++
		c.state.LastError = err.Error()
		c.mu.Unlock()
		return fmt.Errorf("metal-apiserver %s: %w (the gateways keep their networks)", c.cfg.MetalStack.URL, err)
	}
	c.state.Synced, c.state.LastError = time.Now(), ""
	c.mu.Unlock()

	opts := metalstack.Options{Label: c.cfg.MetalStack.Label, DefaultRouteLabel: c.cfg.MetalStack.DefaultRouteLabel, RouteTargetASN: c.cfg.MetalStack.RouteTargetASN}
	served := map[string]bool{}
	for _, g := range c.cfg.Gateways {
		served[g.Partition] = true
	}
	desired := map[string][]metalstack.Network{}
	skipped := map[string]config.Skipped{}
	groups := map[string]bool{}
	var unserved []string
	partitions := map[string]bool{}
	for _, n := range nets {
		partitions[n.GetPartition()] = true
	}
	for p := range served {
		partitions[p] = true
	}
	for _, p := range slices.Sorted(maps.Keys(partitions)) {
		mapped, sk := metalstack.Map(nets, opts, p)
		for _, s := range sk {
			skipped[s.Group] = s
		}
		for _, n := range mapped {
			if served[p] {
				groups[n.Group] = true
			} else {
				unserved = append(unserved, n.Group+" in "+p)
			}
		}
		desired[p] = mapped
	}

	for _, g := range c.cfg.Gateways {
		c.push(ctx, g, desired[g.Partition])
	}

	c.mu.Lock()
	c.state.Groups = len(groups)
	slices.Sort(unserved)
	c.state.Unserved = unserved
	c.state.Skipped = nil
	for _, name := range slices.Sorted(maps.Keys(skipped)) {
		c.state.Skipped = append(c.state.Skipped, skipped[name])
	}
	c.mu.Unlock()
	return nil
}

// push sets a gateway's networks.
func (c *Controller) push(ctx context.Context, g Gateway, nets []metalstack.Network) {
	req := &opendciv1.SetNetworksRequest{Owner: c.cfg.Owner}
	for _, n := range nets {
		req.Networks = append(req.Networks, &opendciv1.Network{
			Vrf: n.VRF, Vni: n.VNI, RouteTarget: n.RouteTarget,
			Aggregates: n.Aggregates, Prefixes: n.Prefixes, DefaultRoute: n.DefaultRoute,
			Group: n.Group, Created: created(n.Created),
		})
	}
	resp, err := c.gateways[g.Name].SetNetworks(ctx, req)
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.state.Gateways[g.Name]
	if err != nil {
		s.PushErrors++
		s.LastError = err.Error()
		c.state.Gateways[g.Name] = s
		c.log.Error("gateway", "gateway", g.Name, "err", err)
		return
	}
	s.Networks, s.LastPush, s.LastError = len(nets), time.Now(), ""
	s.Skipped = nil
	for _, sk := range resp.GetSkipped() {
		s.Skipped = append(s.Skipped, config.Skipped{VRF: sk.GetVrf(), Group: sk.GetGroup(), Reason: sk.GetReason()})
	}
	c.state.Gateways[g.Name] = s
	if resp.GetChanged() {
		c.log.Info("gateway updated", "gateway", g.Name, "networks", len(nets), "skipped", len(s.Skipped))
	}
}

func created(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

// Stale: metal-apiserver hasn't answered for three intervals.
func (s State) Stale(now time.Time, interval time.Duration) bool {
	return s.Synced.IsZero() || now.Sub(s.Synced) > 3*interval
}
