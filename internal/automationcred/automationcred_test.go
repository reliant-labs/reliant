// Copyright (c) 2025 Reliant Labs
package automationcred

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
)

type memKeys map[string]string

func (m memKeys) GetAutomationToken(_ context.Context, user, daemonID string) (string, error) {
	if v, ok := m[user+"|"+daemonID]; ok {
		return v, nil
	}
	return "", svcerr.NotFound("api key")
}

func TestBearerForOrder(t *testing.T) {
	const user = "u-order"
	keys := memKeys{user + "|d1": "rlat_d1"}
	r := New(Deps{Keys: keys})
	trigger := Allow(context.Background())

	// 1. JWT beats the stored token.
	auth.SetUserJWT(user, "jwt")
	got, err := r.BearerFor(trigger, user, "d1")
	require.NoError(t, err)
	assert.Equal(t, "jwt", got)
	auth.SetUserJWT(user, "")

	// 2. Stored token for exactly that daemon.
	got, _ = r.BearerFor(trigger, user, "d1")
	assert.Equal(t, "rlat_d1", got)

	// Another daemon's token is never used, and its classified miss is no
	// error: it is simply no bearer.
	got, err = r.BearerFor(trigger, user, "d2")
	require.NoError(t, err)
	assert.Empty(t, got)

	// No daemon id: never the stored token.
	got, _ = r.BearerFor(trigger, user, "")
	assert.Empty(t, got)

	// A context that is not trigger-launched (attended run, start_run) never
	// falls back.
	got, _ = r.BearerFor(context.Background(), user, "d1")
	assert.Empty(t, got)
}

type failingKeys struct{ err error }

func (f failingKeys) GetAutomationToken(context.Context, string, string) (string, error) {
	return "", f.err
}

// A lookup failure that is not a miss is propagated, never read as "no
// credential", and grants no bearer.
func TestBearerForPropagatesLookupError(t *testing.T) {
	boom := errors.New("store unavailable")
	r := New(Deps{Keys: failingKeys{err: boom}})
	got, err := r.BearerFor(Allow(context.Background()), "u-err", "d1")
	require.ErrorIs(t, err, boom)
	assert.Empty(t, got)
}
