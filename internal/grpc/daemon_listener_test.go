// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/reliant-labs/forge/pkg/mountkit"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/grpc/interceptors"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

// The daemon listener is a trust boundary: daemon credentials reach the
// daemon-audience services and nothing else, and a daemon-audience service
// is never served to another kind of caller. serverkit enforces it at boot
// from each service's declared audience; these pin that reliant's
// declarations make it hold.

// ToolsDaemonService declares the daemon audience — the proto option the
// boot check reads.
func TestToolsDaemonServiceDeclaresTheDaemonAudience(t *testing.T) {
	desc, err := protoregistry.GlobalFiles.FindDescriptorByName(reliantv1connect.ToolsDaemonServiceName)
	require.NoError(t, err)
	require.Equal(t, DaemonAudience, serverkit.ServiceAudience(desc.(protoreflect.ServiceDescriptor)))
}

// A daemon chain refuses to serve a user service: a daemon credential can
// never reach a user RPC, even through a hand-written route set.
func TestDaemonChainRefusesAUserService(t *testing.T) {
	chain := newTestDaemonChain(t, tokenauthority.NewMemory())
	require.Equal(t, DaemonAudience, chain.Audience())

	_, err := serverkit.NewListener(serverkit.ListenerDeps{
		Config:  listenerConfig("127.0.0.1", 0).Listener(),
		ChainV2: chain.ChainV2,
		RoutesV2: mountOne(func(s *connect.Server) {
			reliantv1connect.RegisterSystemServiceHandler(s, reliantv1connect.UnimplementedSystemServiceHandler{})
		}, reliantv1connect.SystemServiceName),
	})
	require.ErrorContains(t, err, reliantv1connect.SystemServiceName)
}

// A user chain refuses to serve ToolsDaemonService: a user's token can never
// open a daemon stream.
func TestUserChainRefusesTheDaemonService(t *testing.T) {
	chain, err := NewUserChain(UserChainDeps{Auth: passAuth})
	require.NoError(t, err)

	_, err = serverkit.NewListener(serverkit.ListenerDeps{
		Config:  listenerConfig("127.0.0.1", 0).Listener(),
		ChainV2: chain.ChainV2,
		RoutesV2: mountOne(func(s *connect.Server) {
			reliantv1connect.RegisterToolsDaemonServiceHandler(s, reliantv1connect.UnimplementedToolsDaemonServiceHandler{})
		}, reliantv1connect.ToolsDaemonServiceName),
	})
	require.ErrorContains(t, err, reliantv1connect.ToolsDaemonServiceName)
}

// mountOne is a route set of one service's registration.
func mountOne(register func(*connect.Server), service string) serverkit.RoutesV2 {
	return serverkit.RoutesFuncV2(func(mux *mountkit.MuxV2) []string {
		mux.Register(register)
		return []string{service}
	})
}

// The daemon chain's Deadline slot gives its unary RPCs the 10s they have
// always had and reports slow ones as reliant does; it names no procedure,
// so the daemon streams stay unbounded.
func TestDaemonChainDeadline(t *testing.T) {
	d := daemonChainDeadline()
	require.Equal(t, interceptors.UnaryTimeout, d.Unary)
	require.Empty(t, d.Procedures, "a named stream would be cut at its budget")
	require.NotNil(t, d.OnSlow)
	chain := newTestDaemonChain(t, tokenauthority.NewMemory())
	var deadlines int
	for _, ic := range chain.ServerInterceptors() {
		if funcName(ic) == funcName(serverkit.Deadline{}.InterceptorV2()) {
			deadlines++
		}
	}
	require.Equal(t, 1, deadlines, "the chain has one Deadline slot")
}
