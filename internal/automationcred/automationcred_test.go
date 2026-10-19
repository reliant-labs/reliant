// Copyright (c) 2025 Reliant Labs
package automationcred

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
)

type memKeys map[string]string

func (m memKeys) GetAutomationToken(_ context.Context, user, daemonID string) (string, error) {
	if v, ok := m[user+"|"+daemonID]; ok {
		return v, nil
	}
	return "", sql.ErrNoRows
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

	// Another daemon's token is never used.
	got, _ = r.BearerFor(trigger, user, "d2")
	assert.Empty(t, got)

	// No daemon id: never the stored token.
	got, _ = r.BearerFor(trigger, user, "")
	assert.Empty(t, got)

	// A context that is not trigger-launched (attended run, start_run) never
	// falls back.
	got, _ = r.BearerFor(context.Background(), user, "d1")
	assert.Empty(t, got)
}
