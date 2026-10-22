// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"net/http"
	"testing"

	"connectrpc.com/connect"
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

	path, handler := reliantv1connect.NewSystemServiceHandler(reliantv1connect.UnimplementedSystemServiceHandler{})
	_, err := serverkit.NewListener(serverkit.ListenerDeps{
		Config: listenerConfig("127.0.0.1", 0).Listener(),
		Chain:  chain.Chain,
		Routes: mountOne(path, handler, reliantv1connect.SystemServiceName),
	})
	require.ErrorContains(t, err, reliantv1connect.SystemServiceName)
}

// A user chain refuses to serve ToolsDaemonService: a user's token can never
// open a daemon stream.
func TestUserChainRefusesTheDaemonService(t *testing.T) {
	passThrough := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc { return next })
	chain, err := NewUserChain(UserChainDeps{Auth: passThrough})
	require.NoError(t, err)

	path, handler := reliantv1connect.NewToolsDaemonServiceHandler(reliantv1connect.UnimplementedToolsDaemonServiceHandler{})
	_, err = serverkit.NewListener(serverkit.ListenerDeps{
		Config: listenerConfig("127.0.0.1", 0).Listener(),
		Chain:  chain.Chain,
		Routes: mountOne(path, handler, reliantv1connect.ToolsDaemonServiceName),
	})
	require.ErrorContains(t, err, reliantv1connect.ToolsDaemonServiceName)
}

// mountOne is a route set of one already-built Connect handler.
func mountOne(path string, handler http.Handler, service string) serverkit.Routes {
	return serverkit.RoutesFunc(func(mux *http.ServeMux, _ ...connect.HandlerOption) []string {
		mux.Handle(path, handler)
		return []string{service}
	})
}

// The daemon chain's Deadline slot gives its unary RPCs the 10s they have
// always had and reports slow ones as reliant does; it names no procedure,
// so the daemon streams stay unbounded.
func TestDaemonChainDeadline(t *testing.T) {
	chain := newTestDaemonChain(t, tokenauthority.NewMemory())
	var deadlines []serverkit.Deadline
	for _, ic := range chain.Interceptors() {
		if d, ok := ic.(serverkit.Deadline); ok {
			deadlines = append(deadlines, d)
		}
	}
	require.Len(t, deadlines, 1)
	require.Equal(t, interceptors.UnaryTimeout, deadlines[0].Unary)
	require.Empty(t, deadlines[0].Procedures, "a named stream would be cut at its budget")
	require.NotNil(t, deadlines[0].OnSlow)
}
