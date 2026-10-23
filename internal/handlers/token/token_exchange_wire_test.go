// Copyright (c) 2025 Reliant Labs
package token

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

// ExchangeToken's two refusals that forge's credential helper (internal/forgecred)
// and, through forge's --json document, the deploy panel route on
// (13-p9-frontend-plan.md §7.1 #7), read by a real Connect client: the code, the
// reason, the scopes in structure, and the sentence byte for byte.
func TestExchangeToken_RefusalsOnTheWire(t *testing.T) {
	for _, tc := range []struct {
		name        string
		cp          TokenControlPlane
		scopes      []string
		wantCode    connect.Code
		wantReason  string
		wantScopes  string
		wantMessage string
	}{
		{
			name:       "the credential holds none of the scopes asked for",
			cp:         TokenControlPlane{Issuer: testControlPlane},
			scopes:     []string{"deploy:write", "secret:read"},
			wantCode:   connect.CodePermissionDenied,
			wantReason: errreason.ScopeMissing,
			wantScopes: "deploy:write,secret:read",
			wantMessage: "this Reliant credential cannot authorize deploy:write, secret:read: it holds daemon:connect. " +
				"Sign in again with `reliant auth login` (it asks for deploy, secret and domain permission), " +
				"or grant this credential those permissions in Reliant → Settings → Access Tokens",
		},
		{
			name:        "this server has no control plane",
			cp:          TokenControlPlane{},
			wantCode:    connect.CodeFailedPrecondition,
			wantReason:  errreason.NoControlPlaneCredential,
			wantMessage: "this server has no control plane, so it issues no control-plane credentials",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authority := tokenauthority.NewMemory()
			_, client := NewTestTokenServer(t, WithTokenDeps(Deps{Authority: authority, ControlPlane: tc.cp}))
			bearer := mintSession(t, authority, "user-1", tokenauthority.MintRequest{Scopes: []fat.Scope{fat.ScopeDaemonConnect}})

			_, err := client.ExchangeToken(context.Background(), exchangeReq(bearer, &reliantv1.ExchangeTokenRequest{
				Audience: testControlPlane, Scopes: tc.scopes,
			}))
			var cerr *connect.Error
			require.True(t, errors.As(err, &cerr), "want a Connect error, got %v", err)
			assert.Equal(t, tc.wantCode, cerr.Code())
			assert.Equal(t, tc.wantReason, cerr.Meta().Get(svcerr.ReasonHeader))
			assert.Equal(t, tc.wantScopes, cerr.Meta().Get(fat.ScopesHeader))
			assert.Equal(t, tc.wantMessage, cerr.Message())
		})
	}
}
