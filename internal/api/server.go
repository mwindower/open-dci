// Package api serves the open-dci gateway API (api/opendci/v1) for
// "open-dci run --api-listen": gRPC, gRPC-Web and Connect on one handler,
// with gRPC reflection for tools like grpcurl. Every call needs the bearer
// token. Reads answer from what run collected last (the Monitor), so they
// never call into FRR; writes change the gateway's API networks, which run
// reconciles right away.
package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpcreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	opendciv1 "github.com/mwindower/open-dci/api/opendci/v1"
	"github.com/mwindower/open-dci/api/opendci/v1/opendciv1connect"
	"github.com/mwindower/open-dci/internal/config"
	"github.com/mwindower/open-dci/internal/gateway"
)

// Server implements opendciv1connect.GatewayServiceHandler.
type Server struct {
	Gateway *gateway.Gateway
	Monitor *gateway.Monitor
	Version string
	now     func() time.Time
}

// Handler serves the API; every call needs "Authorization: Bearer <token>".
func Handler(s *Server, token string) http.Handler {
	if s.now == nil {
		s.now = time.Now
	}
	mux := http.NewServeMux()
	mux.Handle(opendciv1connect.NewGatewayServiceHandler(s, connect.WithInterceptors(auth(token))))
	reflector := grpcreflect.NewStaticReflector(opendciv1connect.GatewayServiceName)
	mux.Handle(grpcreflect.NewHandlerV1(reflector))
	mux.Handle(grpcreflect.NewHandlerV1Alpha(reflector))
	return mux
}

// auth checks the bearer token (constant time).
func auth(token string) connect.UnaryInterceptorFunc {
	want := []byte("Bearer " + token)
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			got := []byte(req.Header().Get("Authorization"))
			if token == "" || subtle.ConstantTimeCompare(got, want) != 1 {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing or wrong bearer token"))
			}
			return next(ctx, req)
		}
	}
}

func (s *Server) GetInfo(ctx context.Context, _ *opendciv1.GetInfoRequest) (*opendciv1.GetInfoResponse, error) {
	snap := s.Monitor.Snapshot()
	now := s.now()
	resp := &opendciv1.GetInfoResponse{
		Version:      s.Version,
		Healthy:      snap.Healthy(now),
		LocatorState: snap.Locator,
		Problems:     snap.Problems(now),
	}
	if st := snap.Status; st != nil {
		resp.Asn, resp.RouterId, resp.Loopback, resp.Locator = st.ASN, st.RouterID, st.Loopback, st.Locator
	}
	return resp, nil
}

func (s *Server) ListNetworks(ctx context.Context, _ *opendciv1.ListNetworksRequest) (*opendciv1.ListNetworksResponse, error) {
	nets, skipped := s.Gateway.Networks()
	state := map[string]gateway.NetworkStatus{}
	if st := s.Monitor.Snapshot().Status; st != nil {
		for _, n := range st.Networks {
			state[n.VRF] = n
		}
	}
	resp := &opendciv1.ListNetworksResponse{}
	for _, o := range nets {
		ns := &opendciv1.NetworkState{Network: toProto(o.Network, o.Created), Origin: "config"}
		if o.API {
			ns.Origin, ns.Owner = "api", o.Owner
		}
		if st, ok := state[o.Network.VRF]; ok {
			ns.SidAddress, ns.L3VniUp = st.SID, st.L3VNIState == "Up"
			ns.LocalPrefixes = uint32(st.LocalV4 + st.LocalV6)
			ns.RemotePrefixes = uint32(st.RemoteV4 + st.RemoteV6)
		}
		resp.Networks = append(resp.Networks, ns)
	}
	resp.Skipped = toSkipped(skipped)
	return resp, nil
}

func (s *Server) SetNetworks(ctx context.Context, req *opendciv1.SetNetworksRequest) (*opendciv1.SetNetworksResponse, error) {
	var nets []gateway.APINetwork
	for _, n := range req.GetNetworks() {
		nets = append(nets, fromProto(n))
	}
	changed, skipped, err := s.Gateway.SetNetworks(req.GetOwner(), nets)
	if err != nil {
		return nil, toError(err)
	}
	return &opendciv1.SetNetworksResponse{Changed: changed, Skipped: toSkipped(skipped)}, nil
}

func (s *Server) PutNetwork(ctx context.Context, req *opendciv1.PutNetworkRequest) (*opendciv1.PutNetworkResponse, error) {
	if req.GetNetwork() == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("network required"))
	}
	changed, err := s.Gateway.PutNetwork(req.GetOwner(), fromProto(req.GetNetwork()))
	if err != nil {
		return nil, toError(err)
	}
	return &opendciv1.PutNetworkResponse{Changed: changed}, nil
}

func (s *Server) DeleteNetwork(ctx context.Context, req *opendciv1.DeleteNetworkRequest) (*opendciv1.DeleteNetworkResponse, error) {
	if err := s.Gateway.DeleteNetwork(req.GetOwner(), req.GetVrf()); err != nil {
		return nil, toError(err)
	}
	return &opendciv1.DeleteNetworkResponse{}, nil
}

func toError(err error) error {
	code := connect.CodeInternal
	switch {
	case errors.Is(err, gateway.ErrInvalid):
		code = connect.CodeInvalidArgument
	case errors.Is(err, gateway.ErrConflict):
		code = connect.CodeAlreadyExists
	case errors.Is(err, gateway.ErrNotFound):
		code = connect.CodeNotFound
	case errors.Is(err, gateway.ErrNotOwner):
		code = connect.CodePermissionDenied
	case errors.Is(err, gateway.ErrNotEnabled):
		code = connect.CodeUnavailable
	}
	return connect.NewError(code, err)
}

func toSkipped(skipped []config.Skipped) []*opendciv1.Skipped {
	var out []*opendciv1.Skipped
	for _, s := range skipped {
		out = append(out, &opendciv1.Skipped{Vrf: s.VRF, Group: s.Group, Reason: s.Reason})
	}
	return out
}

// FromProto converts an API network.
func fromProto(n *opendciv1.Network) gateway.APINetwork {
	a := gateway.APINetwork{
		Group: n.GetGroup(),
		Network: config.Network{
			VRF:          strings.TrimSpace(n.GetVrf()),
			VNI:          n.GetVni(),
			RouteTarget:  n.GetRouteTarget(),
			Aggregates:   n.GetAggregates(),
			Prefixes:     n.GetPrefixes(),
			DefaultRoute: n.GetDefaultRoute(),
			SID:          n.GetSid(),
		},
	}
	if n.GetCreated() != nil {
		a.Created = n.GetCreated().AsTime().UTC()
	}
	return a
}

// ToProto converts a network of the config.
func toProto(n config.Network, created time.Time) *opendciv1.Network {
	p := &opendciv1.Network{
		Vrf: n.VRF, Vni: n.VNI, RouteTarget: n.RouteTarget,
		Aggregates: n.Aggregates, Prefixes: n.Prefixes, DefaultRoute: n.DefaultRoute,
		Sid: n.SID, Group: n.Group,
	}
	if !created.IsZero() {
		p.Created = timestamppb.New(created)
	}
	return p
}
