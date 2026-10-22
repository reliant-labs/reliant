package connection_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/handlers/connection"
)

// What a connections failure tells the caller. A typed *connections.Error
// carries its kind (connections.Error.Unwrap): the code follows Code and the
// text is Message alone, never the wrapped Err. Anything unclassified is the
// handler's fixed Internal.
func TestConnectionErrorCodes(t *testing.T) {
	t.Parallel()

	cause := errors.New("dsn=postgres://u:secret@h")
	cases := map[string]struct {
		err      error
		wantCode connect.Code
		wantMsg  string
	}{
		"typed not found":      {&connections.Error{Code: connections.CodeNotFound, Message: "connection not found", Err: cause}, connect.CodeNotFound, "connection not found"},
		"typed invalid":        {&connections.Error{Code: connections.CodeInvalidArgument, Message: "name is required"}, connect.CodeInvalidArgument, "name is required"},
		"typed already exists": {&connections.Error{Code: connections.CodeAlreadyExists, Message: "a connection has that name"}, connect.CodeAlreadyExists, "a connection has that name"},
		"needs reauth":         {&connections.Error{Code: connections.CodeNeedsReauth, Message: "reconnect GitHub", Err: cause}, connect.CodeFailedPrecondition, "reconnect GitHub"},
		"typed unavailable":    {&connections.Error{Code: connections.CodeUnavailable, Message: "GitHub is down", Err: cause}, connect.CodeUnavailable, "GitHub is down"},
		"typed internal":       {&connections.Error{Code: connections.CodeInternal, Message: "seal failed", Err: cause}, connect.CodeInternal, "connection service error"},
		"store not found":      {core.ErrConnectionNotFound, connect.CodeNotFound, ""},
		"deadline":             {context.DeadlineExceeded, connect.CodeDeadlineExceeded, ""},
		"canceled":             {context.Canceled, connect.CodeCanceled, ""},
		"opaque":               {cause, connect.CodeInternal, "connection service error"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			conns := &connection.StubConnections{
				GetFunc: func(context.Context, string, string) (*core.Connection, error) { return nil, tc.err },
			}
			svc := connection.NewTestConnection(t, connection.WithConnectionDeps(connection.Deps{Connections: conns}), connection.WithPlaceholderDeps())
			_, err := svc.GetConnection(connection.AuthedContext(t), connect.NewRequest(&reliantv1.GetConnectionRequest{Id: "conn-1"}))
			require.Error(t, err)
			assert.Equal(t, tc.wantCode, svcerr.Code(err))
			msg := svcerr.ToConnect(err).Message()
			assert.NotContains(t, msg, "secret")
			if tc.wantMsg != "" {
				assert.Equal(t, tc.wantMsg, msg)
			}
			// The server keeps what the client was not shown.
			if errors.Is(tc.err, cause) {
				assert.ErrorIs(t, err, cause)
			}
		})
	}
}

// Over a real Connect client, through the svcerr interceptor.
func TestConnectionErrorsOnTheWire(t *testing.T) {
	t.Parallel()

	conns := &connection.StubConnections{
		GetFunc: func(context.Context, string, string) (*core.Connection, error) {
			return nil, &connections.Error{Code: connections.CodeNeedsReauth, Message: "reconnect GitHub", Err: errors.New("invalid_grant: token=secret")}
		},
	}
	_, client := connection.NewTestConnectionServer(t, connection.WithConnectionDeps(connection.Deps{Connections: conns}), connection.WithPlaceholderDeps())
	_, err := client.GetConnection(context.Background(), connect.NewRequest(&reliantv1.GetConnectionRequest{Id: "conn-1"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	assert.Equal(t, "reconnect GitHub", svcerr.ToConnect(err).Message())
}
