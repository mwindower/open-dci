package controller

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/mwindower/open-dci/internal/config"
)

// Config is the content of dci-controller's config file.
type Config struct {
	// MetalStack: where the networks come from, and how they are stitched.
	MetalStack MetalStack `json:"metalStack"`
	// Interval between rounds (default 30s).
	Interval config.Duration `json:"interval,omitempty"`
	// Owner: the name the controller writes the gateways' networks under
	// (default "dci-controller"); networks of other owners stay untouched.
	Owner string `json:"owner,omitempty"`
	// Gateways: every gateway, with its partition and API endpoint.
	Gateways []Gateway `json:"gateways"`
	// GatewayTokenFile: the bearer token of the gateways' API (the same on
	// all of them), re-read every round.
	GatewayTokenFile string `json:"gatewayTokenFile"`
	// Listen: address for /metrics and /healthz, e.g. :9471 (optional).
	Listen string `json:"listen,omitempty"`
}

// MetalStack configures the access to metal-apiserver and the stitching.
type MetalStack struct {
	// URL of metal-apiserver (API v2).
	URL string `json:"url"`
	// TokenFile: an API token with the admin viewer role.
	TokenFile string `json:"tokenFile,omitempty"`
	// Label: the network label key tenants set; a project's networks with
	// the same value are stitched together.
	Label string `json:"label"`
	// DefaultRouteLabel: a label key whose value (export or import) sets the
	// network's defaultRoute.
	DefaultRouteLabel string `json:"defaultRouteLabel,omitempty"`
	// RouteTargetASN: the 2-byte namespace of the derived route targets.
	RouteTargetASN uint32 `json:"routeTargetASN"`
}

// Gateway is a gateway the controller manages.
type Gateway struct {
	Name      string `json:"name"`
	Partition string `json:"partition"`
	// Endpoint of its API (open-dci run --api-listen), e.g.
	// http://gw-a1:9470 or https://... with TLS.
	Endpoint string `json:"endpoint"`
	// CAFile: CA for an https endpoint (default: the system's).
	CAFile string `json:"caFile,omitempty"`
}

const (
	DefaultInterval = 30 * time.Second
	DefaultOwner    = "dci-controller"
)

// Load reads, defaults and validates a config file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.UnmarshalStrict(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if c.Interval.Duration == 0 {
		c.Interval.Duration = DefaultInterval
	}
	if c.Owner == "" {
		c.Owner = DefaultOwner
	}
	return &c, c.Validate()
}

// Validate checks the config.
func (c *Config) Validate() error {
	var errs []string
	fail := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }
	httpURL := func(s string) bool {
		u, err := url.Parse(s)
		return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
	}
	m := c.MetalStack
	if !httpURL(m.URL) {
		fail("metalStack.url: want http(s)://host: %q", m.URL)
	}
	if m.Label == "" {
		fail("metalStack.label: required (the network label tenants set)")
	}
	if m.RouteTargetASN == 0 || m.RouteTargetASN > 1<<16-1 {
		fail("metalStack.routeTargetASN: %d, want a 2-byte value (1-65535): it is only the route targets' namespace, "+
			"and after a 4-byte ASN the local part has 16 bits, too few for hashed group names", m.RouteTargetASN)
	}
	if c.Interval.Duration < time.Second {
		fail("interval: %s is too short", c.Interval)
	}
	if strings.ContainsAny(c.Owner, " \t\n") {
		fail("owner: %q must not contain spaces", c.Owner)
	}
	if len(c.Gateways) == 0 {
		fail("gateways: at least one required")
	}
	if c.GatewayTokenFile == "" {
		fail("gatewayTokenFile: required (the gateways' API token)")
	}
	seen := map[string]bool{}
	for i, g := range c.Gateways {
		switch {
		case g.Name == "" || seen[g.Name]:
			fail("gateways[%d].name: missing or twice: %q", i, g.Name)
		case g.Partition == "":
			fail("gateways[%d].partition: required", i)
		case !httpURL(g.Endpoint):
			fail("gateways[%d].endpoint: want http(s)://host:port: %q", i, g.Endpoint)
		}
		seen[g.Name] = true
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid config:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}
