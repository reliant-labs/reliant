package cmd

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

type fakeRevokeTokens struct {
	reliantv1connect.UnimplementedTokenServiceHandler
	tokens  []*reliantv1.TokenInfo
	revoked []string
}

func (f *fakeRevokeTokens) ListTokens(context.Context, *connect.Request[reliantv1.ListTokensRequest]) (*connect.Response[reliantv1.ListTokensResponse], error) {
	return connect.NewResponse(&reliantv1.ListTokensResponse{Tokens: f.tokens}), nil
}

func (f *fakeRevokeTokens) RevokeToken(_ context.Context, r *connect.Request[reliantv1.RevokeTokenRequest]) (*connect.Response[reliantv1.RevokeTokenResponse], error) {
	f.revoked = append(f.revoked, r.Msg.GetId())
	return connect.NewResponse(&reliantv1.RevokeTokenResponse{}), nil
}

// The command end to end: `token revoke ci` revokes the token `token create
// --name ci` minted, stored as reliant-cli@ci.
func TestAuthTokenRevoke_TakesTheNameCreateTook(t *testing.T) {
	isolateForgeCredHome(t)
	fake := &fakeRevokeTokens{tokens: []*reliantv1.TokenInfo{{Id: "t1", Name: "reliant-cli@ci"}}}
	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewTokenServiceHandler(fake))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv(envServerURL, srv.URL)
	t.Setenv(envToken, "rlat_test")

	cmd := newAuthTokenRevokeCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"ci"})
	require.NoError(t, cmd.Execute(), out.String())
	assert.Equal(t, []string{"t1"}, fake.revoked)
	assert.Contains(t, out.String(), `Revoked token "reliant-cli@ci" (t1)`)
}
