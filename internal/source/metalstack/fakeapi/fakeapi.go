// Package fakeapi is a minimal metal-apiserver for tests and the lab: it
// answers the network List calls (admin and project API) from a YAML file,
// read on every request so that tests can change it.
package fakeapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"

	"connectrpc.com/connect"
	adminv2 "github.com/metal-stack/api/go/metalstack/admin/v2"
	"github.com/metal-stack/api/go/metalstack/admin/v2/adminv2connect"
	apiv2 "github.com/metal-stack/api/go/metalstack/api/v2"
	"github.com/metal-stack/api/go/metalstack/api/v2/apiv2connect"
	"sigs.k8s.io/yaml"
)

// Fixture is the content of the YAML file.
type Fixture struct {
	Networks []Network `json:"networks"`
}

// Network is a private network (type child).
type Network struct {
	ID        string            `json:"id"`
	Project   string            `json:"project"`
	Partition string            `json:"partition"`
	VRF       uint32            `json:"vrf"`
	Prefixes  []string          `json:"prefixes"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// Server serves File, or Override if that exists. While Down exists, it
// answers every call with "unavailable". With Token set, requests must carry
// it as bearer token.
type Server struct {
	File, Override, Down string
	Token                string
}

// Handler returns the connect handlers of both network services.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle(adminv2connect.NewNetworkServiceHandler(admin{s: s}))
	mux.Handle(apiv2connect.NewNetworkServiceHandler(project{s: s}))
	return mux
}

func (s *Server) list(h http.Header, project string, q *apiv2.NetworkQuery) ([]*apiv2.Network, error) {
	if s.Token != "" && h.Get("Authorization") != "Bearer "+s.Token {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid token"))
	}
	if s.Down != "" {
		if _, err := os.Stat(s.Down); err == nil {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("down for maintenance"))
		}
	}
	file := s.File
	if s.Override != "" {
		if _, err := os.Stat(s.Override); err == nil {
			file = s.Override
		}
	}
	nets, err := Load(file)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var out []*apiv2.Network
	for _, n := range nets {
		if project != "" && n.GetProject() != project {
			continue
		}
		if q.GetType() != apiv2.NetworkType_NETWORK_TYPE_UNSPECIFIED && q.GetType() != n.GetType() {
			continue
		}
		out = append(out, n)
	}
	return out, nil
}

// Load reads a fixture file as metal-apiserver networks.
func Load(path string) ([]*apiv2.Network, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f Fixture
	if err := yaml.UnmarshalStrict(raw, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var out []*apiv2.Network
	for _, n := range f.Networks {
		out = append(out, &apiv2.Network{
			Id: n.ID, Project: &n.Project, Partition: &n.Partition, Vrf: &n.VRF, Prefixes: n.Prefixes,
			Type: apiv2.NetworkType_NETWORK_TYPE_CHILD,
			Meta: &apiv2.Meta{Labels: &apiv2.Labels{Labels: n.Labels}},
		})
	}
	return out, nil
}

type admin struct {
	adminv2connect.UnimplementedNetworkServiceHandler
	s *Server
}

func (a admin) List(ctx context.Context, req *adminv2.NetworkServiceListRequest) (*adminv2.NetworkServiceListResponse, error) {
	nets, err := a.s.list(header(ctx), "", req.GetQuery())
	return &adminv2.NetworkServiceListResponse{Networks: nets}, err
}

type project struct {
	apiv2connect.UnimplementedNetworkServiceHandler
	s *Server
}

func (p project) List(ctx context.Context, req *apiv2.NetworkServiceListRequest) (*apiv2.NetworkServiceListResponse, error) {
	nets, err := p.s.list(header(ctx), req.GetProject(), req.GetQuery())
	return &apiv2.NetworkServiceListResponse{Networks: nets}, err
}

func header(ctx context.Context) http.Header {
	if ci, ok := connect.CallInfoForHandlerContext(ctx); ok {
		return ci.RequestHeader()
	}
	return http.Header{}
}
