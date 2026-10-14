package gateway

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mwindower/open-dci/internal/config"
)

// APINetwork is a network set through the API (open-dci run --api-listen).
type APINetwork struct {
	// Owner is the writer's name, e.g. "dci-controller"; each owner only
	// changes its own networks.
	Owner string `json:"owner"`
	// Group is the writer's name for the stitched network (informational).
	Group string `json:"group,omitempty"`
	// Created orders the networks by age: the older one wins a conflict,
	// and SIDs are assigned oldest first.
	Created time.Time      `json:"created"`
	Network config.Network `json:"network"`
}

// APIStore holds the API networks, persisted in a file (in the state
// directory), so that a restart keeps them.
type APIStore struct {
	path    string
	nets    []APINetwork
	known   bool      // the file exists: a write was received once
	changed time.Time // of the last change
}

// apiFile is the store's file format.
type apiFile struct {
	Networks []APINetwork `json:"networks"`
}

// OpenAPIStore loads the store from path, if it exists.
func OpenAPIStore(path string) (*APIStore, error) {
	s := &APIStore{path: path}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var f apiFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	fi, _ := os.Stat(path)
	s.nets, s.known, s.changed = f.Networks, true, fi.ModTime()
	return s, nil
}

// save replaces the networks and writes the file atomically.
func (s *APIStore) save(nets []APINetwork) error {
	raw, err := json.MarshalIndent(apiFile{Networks: nets}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.nets, s.known, s.changed = nets, true, time.Now()
	return nil
}

// sortAPI orders API networks by age, then owner and VRF: the order of the
// merge (an earlier network wins a conflict) and of the SID assignment.
func sortAPI(nets []APINetwork) {
	slices.SortStableFunc(nets, func(a, b APINetwork) int {
		if c := a.Created.Compare(b.Created); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Owner, b.Owner); c != 0 {
			return c
		}
		return cmp.Compare(a.Network.VRF, b.Network.VRF)
	})
}

// APIStatus is the state of the API networks.
type APIStatus struct {
	// Known: a write was received once; until then, the gateway doesn't
	// reconcile (it doesn't know its networks)
	Known bool
	// Networks applied (after merging with the config file's)
	Networks   int
	LastChange time.Time `json:",omitzero"`
	// Skipped: API networks that aren't applied, and why
	Skipped []config.Skipped `json:",omitempty"`
}

// Errors of the API operations; the API server maps them to status codes.
var (
	ErrInvalid    = errors.New("invalid")
	ErrConflict   = errors.New("conflict")
	ErrNotFound   = errors.New("not found")
	ErrNotOwner   = errors.New("owned by another owner")
	ErrNotEnabled = errors.New("the API is not enabled")
)

// UseAPI enables API networks: they are merged into the config from now on.
func (g *Gateway) UseAPI(s *APIStore) {
	g.mu.Lock()
	g.api = s
	g.wake = make(chan struct{}, 1)
	g.applyAPI()
	g.mu.Unlock()
	g.takeAPI()
}

// takeAPI adopts the latest merged config (API writes compute it, see
// applyAPI). Only Run (or a command, before it works) calls it: the config
// it reconciles never changes under its feet, and API calls never wait for
// a reconcile.
func (g *Gateway) takeAPI() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.latest == nil || g.latest == g.Config {
		return
	}
	g.Config, g.apiStatus = g.latest, g.latestStatus
	g.pruneSettled()
}

// merge returns the config file's config with the API networks nets added
// (oldest first, SIDs assigned), and the API networks it had to skip.
func (g *Gateway) merge(nets []APINetwork) (*config.Config, []config.Skipped, map[string]APINetwork) {
	sorted := slices.Clone(nets)
	sortAPI(sorted)
	cnets := make([]config.Network, len(sorted))
	ages := make([]time.Time, len(sorted))
	byVRF := map[string]APINetwork{}
	for i, a := range sorted {
		cnets[i], ages[i] = a.Network, a.Created
		cnets[i].Group = a.Group
	}
	config.AssignSIDs(cnets, ages, g.static.Networks)
	cfg, skipped := g.static.WithNetworks(cnets)
	for _, a := range sorted {
		if _, ok := byVRF[a.Network.VRF]; !ok {
			byVRF[a.Network.VRF] = a
		}
	}
	return cfg, skipped, byVRF
}

// applyAPI merges the stored API networks into the latest config, which Run
// takes over (takeAPI). g.mu must be held.
func (g *Gateway) applyAPI() {
	if g.api == nil {
		return
	}
	if g.static == nil {
		g.static = g.Config
	}
	cfg, skipped, _ := g.merge(g.api.nets)
	if !slices.Equal(skipped, g.latestStatus.Skipped) {
		for _, s := range skipped {
			g.Log.Warn("api: network not applied", "vrf", s.VRF, "group", s.Group, "reason", s.Reason)
		}
	}
	g.latest = cfg
	g.latestStatus = APIStatus{
		Known:      g.api.known,
		Networks:   len(cfg.Networks) - len(g.static.Networks),
		LastChange: g.api.changed,
		Skipped:    skipped,
	}
}

// commit stores nets and applies them; changed: they differ from before.
// g.mu must be held.
func (g *Gateway) commit(nets []APINetwork) (bool, error) {
	sortAPI(nets)
	old, _ := json.Marshal(g.api.nets)
	now, _ := json.Marshal(nets)
	changed := string(old) != string(now)
	if !changed && g.api.known {
		return false, nil
	}
	if err := g.api.save(nets); err != nil {
		return false, err
	}
	g.applyAPI()
	if changed {
		select {
		case g.wake <- struct{}{}:
		default:
		}
	}
	return changed, nil
}

func validOwner(owner string) error {
	if owner == "" || strings.ContainsAny(owner, " \t\n") {
		return fmt.Errorf("%w: owner %q: want a non-empty name without spaces", ErrInvalid, owner)
	}
	return nil
}

// SetNetworks replaces all API networks of owner (declarative). Networks
// that can't be applied (conflicts, invalid) are kept as wanted, but skipped
// and reported; they apply once the conflict is gone.
func (g *Gateway) SetNetworks(owner string, nets []APINetwork) (changed bool, skipped []config.Skipped, err error) {
	if err := validOwner(owner); err != nil {
		return false, nil, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.api == nil {
		return false, nil, ErrNotEnabled
	}
	mine := map[string]APINetwork{}
	var next []APINetwork
	for _, a := range g.api.nets {
		if a.Owner == owner {
			mine[a.Network.VRF] = a
		} else {
			next = append(next, a)
		}
	}
	seen := map[string]bool{}
	for _, a := range nets {
		vrf := a.Network.VRF
		if vrf == "" || seen[vrf] {
			return false, nil, fmt.Errorf("%w: network VRF %q missing or twice", ErrInvalid, vrf)
		}
		seen[vrf] = true
		a.Owner = owner
		if a.Created.IsZero() {
			a.Created = time.Now().UTC()
			if old, ok := mine[vrf]; ok {
				a.Created = old.Created
			}
		}
		next = append(next, a)
	}
	_, all, _ := g.merge(next)
	for _, s := range all {
		if seen[s.VRF] {
			skipped = append(skipped, s)
		}
	}
	changed, err = g.commit(next)
	return changed, skipped, err
}

// PutNetwork adds or replaces one API network of owner (imperative): a
// network that conflicts or is invalid is refused, and nothing changes.
func (g *Gateway) PutNetwork(owner string, a APINetwork) (changed bool, err error) {
	if err := validOwner(owner); err != nil {
		return false, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.api == nil {
		return false, ErrNotEnabled
	}
	vrf := a.Network.VRF
	if vrf == "" {
		return false, fmt.Errorf("%w: network without VRF", ErrInvalid)
	}
	var next []APINetwork
	for _, o := range g.api.nets {
		if o.Network.VRF != vrf {
			next = append(next, o)
			continue
		}
		if o.Owner != owner {
			return false, fmt.Errorf("%w: %s belongs to %s", ErrNotOwner, vrf, o.Owner)
		}
		if a.Created.IsZero() {
			a.Created = o.Created
		}
	}
	if a.Created.IsZero() {
		a.Created = time.Now().UTC()
	}
	a.Owner = owner
	next = append(next, a)
	_, all, _ := g.merge(next)
	for _, s := range all {
		if s.VRF == vrf {
			kind := ErrInvalid
			if strings.Contains(s.Reason, "used twice") || strings.Contains(s.Reason, "already used") {
				kind = ErrConflict
			}
			return false, fmt.Errorf("%w: %s: %s", kind, vrf, s.Reason)
		}
	}
	return g.commit(next)
}

// DeleteNetwork removes one API network of owner.
func (g *Gateway) DeleteNetwork(owner, vrf string) error {
	if err := validOwner(owner); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.api == nil {
		return ErrNotEnabled
	}
	var next []APINetwork
	found := false
	for _, o := range g.api.nets {
		if o.Network.VRF != vrf {
			next = append(next, o)
			continue
		}
		if o.Owner != owner {
			return fmt.Errorf("%w: %s belongs to %s", ErrNotOwner, vrf, o.Owner)
		}
		found = true
	}
	if !found {
		return fmt.Errorf("%w: %s", ErrNotFound, vrf)
	}
	_, err := g.commit(next)
	return err
}

// NetworkOrigin says where a network of the current config comes from.
type NetworkOrigin struct {
	Network config.Network
	API     bool   // from the API (else the config file)
	Owner   string // of an API network
	Created time.Time
}

// Networks returns the networks of the current config with their origin,
// and the API networks that aren't applied.
func (g *Gateway) Networks() ([]NetworkOrigin, []config.Skipped) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var api map[string]APINetwork
	if g.api != nil {
		_, _, api = g.merge(g.api.nets)
	}
	var out []NetworkOrigin
	cfg := g.latest
	if cfg == nil {
		cfg = g.Config
	}
	for _, n := range cfg.Networks {
		o := NetworkOrigin{Network: n}
		if a, ok := api[n.VRF]; ok && n.Group == a.Group && !slices.ContainsFunc(g.static.Networks, func(s config.Network) bool { return s.VRF == n.VRF }) {
			o.API, o.Owner, o.Created = true, a.Owner, a.Created
		}
		out = append(out, o)
	}
	return out, slices.Clone(g.latestStatus.Skipped)
}

// apiReady: with the API enabled but no write received yet, the gateway
// doesn't know its networks and must not remove anything.
func (g *Gateway) apiReady() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.api != nil && !g.api.known {
		return fmt.Errorf("api: networks unknown, not reconciling until the first write (SetNetworks/PutNetwork) arrives")
	}
	return nil
}
