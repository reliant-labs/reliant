// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"net/http"
	"reflect"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
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
	chain, err := NewDaemonChain(DaemonChainDeps{DaemonTokens: tokenauthority.NewMemory()})
	require.NoError(t, err)
	require.Equal(t, DaemonAudience, chain.Audience())

	path, handler := reliantv1connect.NewSystemServiceHandler(reliantv1connect.UnimplementedSystemServiceHandler{})
	_, err = serverkit.NewListener(serverkit.ListenerDeps{
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
	chain, err := serverkit.NewChain(serverkit.ChainDeps{Auth: serverkit.AuthFor(serverkit.AudienceUser, passThrough)})
	require.NoError(t, err)

	path, handler := reliantv1connect.NewToolsDaemonServiceHandler(reliantv1connect.UnimplementedToolsDaemonServiceHandler{})
	_, err = serverkit.NewListener(serverkit.ListenerDeps{
		Config: listenerConfig("127.0.0.1", 0).Listener(),
		Chain:  chain,
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

// forge's Deadline slot is dropped on the daemon chain: its own watchdog and
// timeout (Extras) bound the daemon's unary RPCs, and a second watchdog would
// log every slow call twice.
func TestDaemonChainHasNoForgeDeadline(t *testing.T) {
	chain, err := NewDaemonChain(DaemonChainDeps{DaemonTokens: tokenauthority.NewMemory()})
	require.NoError(t, err)
	for _, ic := range chain.Interceptors() {
		require.NotEqual(t, reflect.TypeOf(serverkit.Deadline{}), reflect.TypeOf(ic))
	}
}
