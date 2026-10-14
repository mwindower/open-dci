package metalstack

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	adminv2 "github.com/metal-stack/api/go/metalstack/admin/v2"
	"github.com/metal-stack/api/go/metalstack/admin/v2/adminv2connect"
	apiv2 "github.com/metal-stack/api/go/metalstack/api/v2"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/mwindower/open-dci/internal/config"
)

// Source polls metal-apiserver for private networks. It keeps the last good
// answer, also in a cache file: a failed poll or a restart without the API
// never removes a network, only an answer without it does.
type Source struct {
	cfg   *config.MetalStackSource
	cache string
	log   *slog.Logger
	http  *http.Client

	mu     sync.Mutex
	nets   []*apiv2.Network
	synced time.Time // of nets; zero: nothing known yet
}

// New returns a source for cfg and loads the cache file (if any).
func New(cfg *config.MetalStackSource, cache string, log *slog.Logger) *Source {
	s := &Source{cfg: cfg, cache: cache, log: log, http: &http.Client{Timeout: 20 * time.Second}}
	if err := s.load(); err != nil && !os.IsNotExist(err) {
		log.Warn("metal-stack source: ignoring cache", "file", cache, "err", err)
	}
	return s
}

// Interval is the configured poll interval.
func (s *Source) Interval() time.Duration { return s.cfg.Interval.Duration }

// Networks returns the gateway's networks from the last good answer, with
// SIDs assigned around the static networks, and the groups it skips.
func (s *Source) Networks(static []config.Network) ([]config.Network, []config.Skipped, time.Time) {
	s.mu.Lock()
	nets, synced := s.nets, s.synced
	s.mu.Unlock()
	out, skipped := Map(nets, s.cfg, static)
	return out, skipped, synced
}

// Sync polls metal-apiserver once. On error the last good answer stays.
// changed reports whether the answer differs from the last one.
func (s *Source) Sync(ctx context.Context) (changed bool, err error) {
	nets, err := s.fetch(ctx)
	if err != nil {
		return false, fmt.Errorf("metal-apiserver %s: %w", s.cfg.URL, err)
	}
	slices.SortFunc(nets, func(a, b *apiv2.Network) int { return strings.Compare(a.GetId(), b.GetId()) })
	raw, err := marshal(nets)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	changed = s.synced.IsZero() || !slices.EqualFunc(s.nets, nets, func(a, b *apiv2.Network) bool { return proto.Equal(a, b) })
	s.nets, s.synced = nets, time.Now()
	s.mu.Unlock()
	if err := writeAtomic(s.cache, raw); err != nil {
		s.log.Warn("metal-stack source: writing cache", "file", s.cache, "err", err)
	}
	return changed, nil
}

// fetch lists the private networks of all projects (admin API): labels
// can't be filtered by key on the server.
func (s *Source) fetch(ctx context.Context) ([]*apiv2.Network, error) {
	opts := []connect.ClientOption{}
	if s.cfg.TokenFile != "" {
		token, err := os.ReadFile(s.cfg.TokenFile)
		if err != nil {
			return nil, err
		}
		opts = append(opts, connect.WithInterceptors(bearer(strings.TrimSpace(string(token)))))
	}
	child := apiv2.NetworkType_NETWORK_TYPE_CHILD
	c := adminv2connect.NewNetworkServiceClient(s.http, s.cfg.URL, opts...)
	resp, err := c.List(ctx, &adminv2.NetworkServiceListRequest{Query: &apiv2.NetworkQuery{Type: &child}})
	if err != nil {
		return nil, err
	}
	return resp.GetNetworks(), nil
}

func bearer(token string) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, req)
		}
	}
}

// The cache file holds the last good answer as an admin list response.
func marshal(nets []*apiv2.Network) ([]byte, error) {
	return protojson.MarshalOptions{Multiline: true}.Marshal(&adminv2.NetworkServiceListResponse{Networks: nets})
}

func (s *Source) load() error {
	raw, err := os.ReadFile(s.cache)
	if err != nil {
		return err
	}
	fi, err := os.Stat(s.cache)
	if err != nil {
		return err
	}
	var resp adminv2.NetworkServiceListResponse
	if err := protojson.Unmarshal(raw, &resp); err != nil {
		return err
	}
	s.nets, s.synced = resp.GetNetworks(), fi.ModTime()
	return nil
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
