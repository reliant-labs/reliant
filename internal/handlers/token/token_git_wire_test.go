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

	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

// The git credential helper (cmd/reliant/cmd/git_credential.go) reads
// GetGitToken's refusals over the wire: the code, and the reason in
// x-forge-error-reason that says which refusal it is. Both must survive the
// svcerr interceptor exactly, and a failure of the remote git-token service
// must not publish that service's words.
func TestGetGitToken_RefusalsOnTheWire(t *testing.T) {
	for _, tc := range []struct {
		name        string
		src         *fakeGitTokens
		wantCode    connect.Code
		wantReason  string
		wantMessage string
	}{
		{
			name:        "no control plane",
			src:         nil,
			wantCode:    connect.CodeFailedPrecondition,
			wantReason:  reasonGitNoControlPlane,
			wantMessage: "this server has no control plane, so it holds no git provider token for you",
		},
		{
			name:        "not connected",
			src:         &fakeGitTokens{err: gitcredentialclient.ErrNotConnected},
			wantCode:    connect.CodeFailedPrecondition,
			wantReason:  reasonGitNotConnected,
			wantMessage: "connect your github account in Reliant first",
		},
		{
			name:        "needs reconnect",
			src:         &fakeGitTokens{err: gitcredentialclient.ErrNeedsReconnect},
			wantCode:    connect.CodeFailedPrecondition,
			wantReason:  reasonGitNeedsReconnect,
			wantMessage: "your github connection expired; reconnect it in Reliant",
		},
		{
			name:        "git-token service down",
			src:         &fakeGitTokens{err: errors.New("gitcredentialclient: control-plane unavailable: vault.internal:8200 sealed")},
			wantCode:    connect.CodeUnavailable,
			wantMessage: "git token service unavailable; retry",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authority := tokenauthority.NewMemory()
			deps := Deps{Authority: authority}
			if tc.src != nil {
				deps.GitTokens = tc.src
			}
			_, client := NewTestTokenServer(t, WithTokenDeps(deps))
			bearer := mintSession(t, authority, "user-1", tokenauthority.MintRequest{Scopes: []fat.Scope{fat.ScopeDaemonConnect}})

			_, err := client.GetGitToken(context.Background(), gitReq(bearer, ""))
			var cerr *connect.Error
			require.ErrorAs(t, err, &cerr)
			assert.Equal(t, tc.wantCode, cerr.Code())
			assert.Equal(t, tc.wantReason, cerr.Meta().Get(svcerr.ReasonHeader))
			assert.Equal(t, tc.wantMessage, cerr.Message())
		})
	}
}
