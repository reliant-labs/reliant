// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
	"github.com/reliant-labs/reliant/internal/toolexec/transport"
)

// identityGateway records the Reliant-Client header each gateway stream opens
// with, then refuses the registration so the daemon stops.
type identityGateway struct {
	reliantv1connect.UnimplementedToolsDaemonServiceHandler

	mu   sync.Mutex
	seen []string
}

func (g *identityGateway) ConnectDaemon(_ context.Context, stream *connect.BidiStream[reliantv1.DaemonMessage, reliantv1.ServerMessage]) error {
	g.mu.Lock()
	g.seen = append(g.seen, stream.RequestHeader().Get(transport.ReliantClientHeader))
	g.mu.Unlock()
	return bootstrap.NewReservedDaemonIDError()
}

// The daemon's gateway stream names the daemon and its version: a server may
// refuse a request with no Reliant-Client header, the stream's open included.
func TestDaemonGatewayStreamSendsReliantClient(t *testing.T) {
	gw := &identityGateway{}
	client := newDaemonClientForInstance(t, t.TempDir(), serveGateway(t, gw), "launcher-id")

	require.Error(t, runUntilDone(t, client))

	gw.mu.Lock()
	defer gw.mu.Unlock()
	require.NotEmpty(t, gw.seen)
	for _, got := range gw.seen {
		require.Equal(t, transport.ClientIdentity(transport.DaemonClientKind), got)
	}
}
