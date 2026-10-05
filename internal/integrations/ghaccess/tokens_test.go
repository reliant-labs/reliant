// Copyright (c) 2025 Reliant Labs
package ghaccess

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/vault"
)

type fakeCP struct {
	tok   string
	err   error
	asked []string
}

func (f *fakeCP) UserAccessToken(_ context.Context, user, provider string) (gitcredentialclient.Token, error) {
	f.asked = append(f.asked, user+"|"+provider)
	if f.err != nil {
		return gitcredentialclient.Token{}, f.err
	}
	exp := time.Now().Add(time.Hour)
	return gitcredentialclient.NewToken(f.tok, &exp), nil
}

func TestDelegatedTokens(t *testing.T) {
	ctx := context.Background()
	cp := &fakeCP{tok: "ghu_x"}
	tok, err := NewDelegatedTokens(cp).Token(ctx, "idp|alice")
	require.NoError(t, err)
	assert.Equal(t, "ghu_x", tok)
	assert.Equal(t, []string{"idp|alice|github"}, cp.asked, "the user's own token, for github")

	for in, want := range map[error]error{
		gitcredentialclient.ErrNotConnected:   ErrNotConnected,
		gitcredentialclient.ErrNeedsReconnect: ErrNeedsReconnect,
	} {
		_, err := NewDelegatedTokens(&fakeCP{err: in}).Token(ctx, "u")
		assert.ErrorIs(t, err, want)
		assert.True(t, IsPermanent(err))
	}
	_, err = NewDelegatedTokens(&fakeCP{err: errors.New("dial tcp: refused")}).Token(ctx, "u")
	require.Error(t, err)
	assert.False(t, IsPermanent(err), "control-plane unreachable is transient")
}

type fakeConns struct {
	conn *core.Connection
	err  error
}

func (f fakeConns) DefaultConnection(context.Context, string, string) (*core.Connection, error) {
	return f.conn, f.err
}

type fakeConnTokens struct {
	tok string
	err error
}

func (f fakeConnTokens) Token(context.Context, string, string) (vault.Secret, error) {
	if f.err != nil {
		return vault.Secret{}, f.err
	}
	return vault.NewSecret([]byte(f.tok)), nil
}

func TestSavedTokens(t *testing.T) {
	ctx := context.Background()
	oauth := &core.Connection{ID: "c1", AuthKind: core.ConnectionAuthOAuth2}
	tok, err := NewSavedTokens(fakeConns{conn: oauth}, fakeConnTokens{tok: "ghu_y"}).Token(ctx, "u")
	require.NoError(t, err)
	assert.Equal(t, "ghu_y", tok)

	_, err = NewSavedTokens(fakeConns{err: core.ErrConnectionNotFound}, fakeConnTokens{}).Token(ctx, "u")
	assert.ErrorIs(t, err, ErrNotConnected)

	pat := &core.Connection{ID: "c2", AuthKind: core.ConnectionAuthAPIKey}
	_, err = NewSavedTokens(fakeConns{conn: pat}, fakeConnTokens{tok: "ghp_z"}).Token(ctx, "u")
	assert.ErrorIs(t, err, ErrUnsupportedToken, "a PAT cannot list App installations")

	reauth := &connections.Error{Code: connections.CodeNeedsReauth, Message: "reconnect"}
	_, err = NewSavedTokens(fakeConns{conn: oauth}, fakeConnTokens{err: reauth}).Token(ctx, "u")
	assert.ErrorIs(t, err, ErrNeedsReconnect)

	_, err = NewSavedTokens(fakeConns{conn: oauth}, fakeConnTokens{err: &connections.Error{Code: connections.CodeUnavailable}}).Token(ctx, "u")
	require.Error(t, err)
	assert.False(t, IsPermanent(err))
}
