// Package metalstacktest fakes metal-apiserver's answers for unit tests the
// way metal-stack's own clients do: client.NewTestInterceptor checks each call
// against the next expected request and answers it without a server.
// (The lab needs a real server: internal/metalstack/fakeapi.)
package metalstacktest

import (
	"errors"

	"connectrpc.com/connect"
	"github.com/metal-stack/api/go/client"
	adminv2 "github.com/metal-stack/api/go/metalstack/admin/v2"
	apiv2 "github.com/metal-stack/api/go/metalstack/api/v2"
)

// Network is a private network (type child) as metal-apiserver lists it.
func Network(id, project, partition string, vrf uint32, labels map[string]string, prefixes ...string) *apiv2.Network {
	return &apiv2.Network{
		Id: id, Project: &project, Partition: &partition, Vrf: &vrf, Prefixes: prefixes,
		Type: apiv2.NetworkType_NETWORK_TYPE_CHILD, Meta: &apiv2.Meta{Labels: &apiv2.Labels{Labels: labels}},
	}
}

// List expects metalstack.Client's network List and answers with nets.
func List(nets ...*apiv2.Network) client.ClientCall {
	child := apiv2.NetworkType_NETWORK_TYPE_CHILD
	return client.ClientCall{
		WantRequest: &adminv2.NetworkServiceListRequest{Query: &apiv2.NetworkQuery{Type: &child}},
		WantResponse: func() connect.AnyResponse {
			return connect.NewResponse(&adminv2.NetworkServiceListResponse{Networks: nets})
		},
	}
}

// Unavailable expects the network List and answers "unavailable".
func Unavailable() client.ClientCall {
	call := List()
	call.WantResponse = nil
	call.WantError = connect.NewError(connect.CodeUnavailable, errors.New("down for maintenance"))
	return call
}
