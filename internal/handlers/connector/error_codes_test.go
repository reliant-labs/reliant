// Copyright (c) 2025 Reliant Labs
package connector

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connectorgrant"
)

// What a grant-store failure tells the caller: a classified error keeps its
// code, and an unclassified one is Internal with the store's text withheld.
func TestConnectorStoreErrorCodes(t *testing.T) {
	cases := map[string]struct {
		err  error
		code connect.Code
		msg  string
	}{
		"not found": {svcerr.NotFound("connector grant"), connect.CodeNotFound, "connector grant not found"},
		"deadline":  {context.DeadlineExceeded, connect.CodeDeadlineExceeded, ""},
		"canceled":  {context.Canceled, connect.CodeCanceled, ""},
		"opaque":    {errors.New("dsn=postgres://u:secret@h"), connect.CodeInternal, "failed to list connectors"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			grants := &StubGrants{ListGrantsByUserFunc: func(context.Context, string) ([]*connectorgrant.Grant, error) { return nil, c.err }}
			svc := NewTestConnector(t, WithConnectorDeps(Deps{Grants: grants}), WithPlaceholderDeps())
			_, err := svc.ListConnectors(AuthedContext(t), connect.NewRequest(&reliantv1.ListConnectorsRequest{}))
			require.Error(t, err)
			assert.Equal(t, c.code, svcerr.Code(err))
			msg := svcerr.ToConnect(err).Message()
			assert.NotContains(t, msg, "secret")
			if c.msg != "" {
				assert.Equal(t, c.msg, msg)
			}
		})
	}
}

// Over a real Connect client: a validation refusal keeps the text written for
// the person, and a missing principal is Unauthenticated.
func TestConnectorErrorsOnTheWire(t *testing.T) {
	_, client := NewTestConnectorServer(t, WithPlaceholderDeps())
	_, err := client.CreateConnector(context.Background(), connect.NewRequest(&reliantv1.CreateConnectorRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	assert.Equal(t, "a connector must be bound to a specific workspace", svcerr.ToConnect(err).Message())

	_, anon := NewTestConnectorServer(t, WithPlaceholderDeps(), WithoutPrincipal())
	_, err = anon.ListConnectors(context.Background(), connect.NewRequest(&reliantv1.ListConnectorsRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, svcerr.Code(err))
}
