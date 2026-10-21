// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/connectorgrant"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The connector ext queries (P5b lane L5), each read back through
// connectorgrant.SQLStore — the hand-SQL store they replaced, and now the
// mapping layer over them — and through the generated rows.

func connectorFixture(t *testing.T) (*storetest.Env, connectorgrant.Store, string) {
	t.Helper()
	env := storetest.New(t)
	daemonID := "daemon-" + uuid.NewString()
	_, err := env.SQL.Exec(`INSERT INTO daemons (id, user_id, created_at, updated_at) VALUES ($1, $2, NOW(), NOW())`, daemonID, "user-1")
	require.NoError(t, err)
	return env, connectorgrant.NewSQLStore(connectorgrant.Deps{DB: env.ORM}), daemonID
}

func connectorGrant(daemonID string) *connectorgrant.Grant {
	return &connectorgrant.Grant{
		ID: uuid.NewString(), UserID: "user-1", DaemonID: daemonID, Name: "phone",
		AllowedTools: []string{"read_file"}, PathRoot: "/workspace", ExecMode: connectorgrant.ExecDeny,
	}
}

func TestConnectorGrantExtQueries(t *testing.T) {
	env, grants, daemonID := connectorFixture(t)
	ctx := context.Background()

	live := connectorGrant(daemonID)
	require.NoError(t, grants.CreateGrant(ctx, live))
	expired := connectorGrant(daemonID)
	past := time.Now().Add(-time.Hour)
	expired.ExpiresAt = &past
	require.NoError(t, grants.CreateGrant(ctx, expired))

	row, err := store.GetLiveConnectorGrant(ctx, env.ORM, live.ID)
	require.NoError(t, err)
	assert.JSONEq(t, `["read_file"]`, string(row.AllowedTools))
	assert.JSONEq(t, `[]`, string(row.ExecAllowlist))
	_, err = store.GetLiveConnectorGrant(ctx, env.ORM, expired.ID)
	assert.ErrorIs(t, err, sql.ErrNoRows, "an expired grant is not live")

	require.NoError(t, store.SetConnectorGrantTokenPrefix(ctx, env.ORM, live.ID, "rlat_ab12"))
	require.NoError(t, store.TouchConnectorGrant(ctx, env.ORM, live.ID))
	before, err := store.GetConnectorGrantByID(ctx, env.ORM, live.ID)
	require.NoError(t, err)
	require.NoError(t, store.TouchConnectorGrant(ctx, env.ORM, live.ID))
	after, err := store.GetConnectorGrantByID(ctx, env.ORM, live.ID)
	require.NoError(t, err)
	assert.True(t, after.UpdatedAt.Equal(before.UpdatedAt), "a touch is telemetry: updated_at is left alone")
	got, err := grants.GetGrant(ctx, "user-1", live.ID)
	require.NoError(t, err)
	assert.Equal(t, "rlat_ab12", got.TokenPrefix)
	require.NotNil(t, got.LastUsedAt)
	require.NoError(t, store.TouchConnectorGrant(ctx, env.ORM, "missing"), "a missing grant is no error")

	ok, err := store.RevokeConnectorGrant(ctx, env.ORM, "someone-else", live.ID)
	require.NoError(t, err)
	assert.False(t, ok, "scoped by user")
	ok, err = store.RevokeConnectorGrant(ctx, env.ORM, "user-1", live.ID)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = grants.RevokeGrant(ctx, "user-1", live.ID)
	require.NoError(t, err)
	assert.False(t, ok, "a repeated revoke changes nothing")
	_, err = grants.GetGrantByID(ctx, live.ID)
	assert.ErrorIs(t, err, connectorgrant.ErrNotFound)
	got, err = grants.GetGrant(ctx, "user-1", live.ID)
	require.NoError(t, err, "GetGrant keeps revoked grants for history")
	assert.NotNil(t, got.RevokedAt)
	_, err = grants.GetGrant(ctx, "someone-else", live.ID)
	assert.ErrorIs(t, err, connectorgrant.ErrNotFound)

	list, err := grants.ListGrantsByUser(ctx, "user-1")
	require.NoError(t, err)
	assert.Len(t, list, 2)
}

func TestConnectorAuditExtQueries(t *testing.T) {
	env, grants, daemonID := connectorFixture(t)
	ctx := context.Background()
	g := connectorGrant(daemonID)
	require.NoError(t, grants.CreateGrant(ctx, g))

	start := &connectorgrant.AuditRecord{
		ID: uuid.NewString(), GrantID: g.ID, UserID: "user-1", DaemonID: daemonID,
		ToolName: "read_file", CommandType: "read", Status: connectorgrant.AuditStarted,
		Arguments: json.RawMessage(`{"path":"a"}`),
	}
	require.NoError(t, grants.RecordAudit(ctx, start))
	plain := &connectorgrant.AuditRecord{
		ID: uuid.NewString(), GrantID: g.ID, UserID: "user-1", DaemonID: daemonID,
		ToolName: "search", CommandType: "search", CreatedAt: time.Now().Add(-time.Minute),
	}
	require.NoError(t, grants.RecordAudit(ctx, plain))
	row, err := store.GetConnectorAuditLogByID(ctx, env.ORM, plain.ID)
	require.NoError(t, err)
	assert.Equal(t, connectorgrant.AuditCompleted, row.Status, "an unset status is completed")
	assert.JSONEq(t, `{}`, string(row.Arguments), "an absent payload is the empty object")
	require.NotNil(t, row.DurationMs)
	assert.EqualValues(t, 0, *row.DurationMs)
	assert.Nil(t, row.ErrorMessage)

	msg := "denied by policy"
	require.NoError(t, store.CompleteConnectorAudit(ctx, env.ORM, start.ID, connectorgrant.AuditDenied, &msg, 12))
	require.NoError(t, store.CompleteConnectorAudit(ctx, env.ORM, start.ID, connectorgrant.AuditCompleted, nil, 99),
		"a late completion is a no-op")
	recs, err := grants.ListAuditByGrant(ctx, "user-1", g.ID, 10)
	require.NoError(t, err)
	require.Len(t, recs, 2)
	assert.Equal(t, start.ID, recs[0].ID, "newest first")
	assert.Equal(t, connectorgrant.AuditDenied, recs[0].Status)
	assert.True(t, recs[0].Denied)
	assert.Equal(t, msg, recs[0].ErrorMsg)
	assert.Equal(t, 12, recs[0].DurationMS)
	assert.JSONEq(t, `{"path":"a"}`, string(recs[0].Arguments))

	recs, err = grants.ListAuditByUser(ctx, "user-1", 1)
	require.NoError(t, err)
	assert.Len(t, recs, 1)
	recs, err = grants.ListAuditByGrant(ctx, "someone-else", g.ID, 10)
	require.NoError(t, err)
	assert.Empty(t, recs, "scoped by user")
}

func TestConnectorClientBindingExtQueries(t *testing.T) {
	env, grants, daemonID := connectorFixture(t)
	ctx := context.Background()
	first, second := connectorGrant(daemonID), connectorGrant(daemonID)
	require.NoError(t, grants.CreateGrant(ctx, first))
	require.NoError(t, grants.CreateGrant(ctx, second))

	require.NoError(t, store.PutConnectorClientBinding(ctx, env.ORM, &store.ConnectorClientBinding{
		ID: uuid.NewString(), UserID: "user-1", ClientID: "chatgpt", GrantID: first.ID, ClientName: "ChatGPT",
	}))
	b, err := grants.GetBinding(ctx, "user-1", "chatgpt")
	require.NoError(t, err)
	assert.Equal(t, first.ID, b.GrantID)

	// Re-consent moves the client; it never adds a second binding.
	require.NoError(t, grants.PutBinding(ctx, &connectorgrant.ClientBinding{
		ID: uuid.NewString(), UserID: "user-1", ClientID: "chatgpt", GrantID: second.ID, ClientName: "ChatGPT 2",
	}))
	row, err := store.GetLiveConnectorClientBinding(ctx, env.ORM, "user-1", "chatgpt")
	require.NoError(t, err)
	assert.Equal(t, second.ID, row.GrantID)
	assert.Equal(t, "ChatGPT 2", row.ClientName)
	assert.Equal(t, b.ID, row.ID, "the upsert keeps the original row")
	all, err := grants.ListBindingsByUser(ctx, "user-1")
	require.NoError(t, err)
	assert.Len(t, all, 1)

	// A binding to a revoked connector is absent.
	_, err = grants.RevokeGrant(ctx, "user-1", second.ID)
	require.NoError(t, err)
	_, err = store.GetLiveConnectorClientBinding(ctx, env.ORM, "user-1", "chatgpt")
	assert.True(t, errors.Is(err, sql.ErrNoRows), "got %v", err)
	_, err = grants.GetBinding(ctx, "user-1", "chatgpt")
	assert.ErrorIs(t, err, connectorgrant.ErrNotFound)

	ok, err := store.UnbindConnectorClient(ctx, env.ORM, "someone-else", "chatgpt")
	require.NoError(t, err)
	assert.False(t, ok, "scoped by user")
	ok, err = grants.DeleteBinding(ctx, "user-1", "chatgpt")
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = store.UnbindConnectorClient(ctx, env.ORM, "user-1", "chatgpt")
	require.NoError(t, err)
	assert.False(t, ok)
}
