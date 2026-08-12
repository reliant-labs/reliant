// Copyright (c) 2025 Reliant Labs

package connectorgrant_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/connectorgrant"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/db"
)

// setupStore returns a store over the migrated test database, plus a daemon id
// the grants can reference (connector_grants.daemon_id is a foreign key).
func setupStore(t *testing.T) (*connectorgrant.SQLStore, *sql.DB, string) {
	t.Helper()

	_, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)

	daemonID := "daemon-" + uuid.New().String()
	_, err := rawDB.Exec(
		`INSERT INTO daemons (id, user_id, created_at, updated_at) VALUES ($1, $2, NOW(), NOW())`,
		daemonID, "user-1",
	)
	require.NoError(t, err)

	return connectorgrant.NewSQLStore(rawDB), rawDB, daemonID
}

func newGrant(daemonID string) *connectorgrant.Grant {
	raw, hash, prefix, _ := connectorgrant.GenerateCredential()
	_ = raw
	return &connectorgrant.Grant{
		ID:           uuid.New().String(),
		UserID:       "user-1",
		DaemonID:     daemonID,
		Name:         "ChatGPT on my phone",
		TokenHash:    hash,
		TokenPrefix:  prefix,
		AllowedTools: []string{"read_file", "search"},
		PathRoot:     "/workspace",
		ExecMode:     connectorgrant.ExecDeny,
	}
}

func TestCreateAndResolveGrant(t *testing.T) {
	store, _, daemonID := setupStore(t)
	ctx := context.Background()

	raw, hash, prefix, err := connectorgrant.GenerateCredential()
	require.NoError(t, err)
	require.True(t, connectorgrant.IsCredentialFormat(raw))

	g := newGrant(daemonID)
	g.TokenHash = hash
	g.TokenPrefix = prefix
	require.NoError(t, store.CreateGrant(ctx, g))

	// The credential resolves by hash — the plaintext is never stored.
	got, err := store.GetGrantByTokenHash(ctx, connectorgrant.HashCredential(raw))
	require.NoError(t, err)
	require.Equal(t, g.ID, got.ID)
	require.Equal(t, []string{"read_file", "search"}, got.AllowedTools)
	require.Equal(t, "/workspace", got.PathRoot)
	require.Equal(t, connectorgrant.ExecDeny, got.ExecMode)
}

func TestUnknownCredentialRejected(t *testing.T) {
	store, _, _ := setupStore(t)

	_, err := store.GetGrantByTokenHash(context.Background(), connectorgrant.HashCredential("rlnt_conn_nope"))
	require.ErrorIs(t, err, connectorgrant.ErrNotFound)
}

// TestRevokedGrantStopsResolving is the property revocation exists for: after
// revoking, the credential must stop working immediately.
func TestRevokedGrantStopsResolving(t *testing.T) {
	store, _, daemonID := setupStore(t)
	ctx := context.Background()

	g := newGrant(daemonID)
	require.NoError(t, store.CreateGrant(ctx, g))

	_, err := store.GetGrantByTokenHash(ctx, g.TokenHash)
	require.NoError(t, err, "grant should resolve before revocation")

	revoked, err := store.RevokeGrant(ctx, g.UserID, g.ID)
	require.NoError(t, err)
	require.True(t, revoked)

	_, err = store.GetGrantByTokenHash(ctx, g.TokenHash)
	require.ErrorIs(t, err, connectorgrant.ErrNotFound, "a revoked credential must stop resolving")

	// Revoking again reports no change rather than erroring.
	again, err := store.RevokeGrant(ctx, g.UserID, g.ID)
	require.NoError(t, err)
	require.False(t, again)
}

// TestRevokeIsScopedToOwner guards against one user revoking another's grant.
func TestRevokeIsScopedToOwner(t *testing.T) {
	store, _, daemonID := setupStore(t)
	ctx := context.Background()

	g := newGrant(daemonID)
	require.NoError(t, store.CreateGrant(ctx, g))

	revoked, err := store.RevokeGrant(ctx, "someone-else", g.ID)
	require.NoError(t, err)
	require.False(t, revoked, "a different user must not be able to revoke this grant")

	_, err = store.GetGrantByTokenHash(ctx, g.TokenHash)
	require.NoError(t, err, "grant should be untouched")
}

func TestExpiredGrantStopsResolving(t *testing.T) {
	store, _, daemonID := setupStore(t)
	ctx := context.Background()

	past := time.Now().Add(-time.Hour).UTC()
	g := newGrant(daemonID)
	g.ExpiresAt = &past
	require.NoError(t, store.CreateGrant(ctx, g))

	_, err := store.GetGrantByTokenHash(ctx, g.TokenHash)
	require.ErrorIs(t, err, connectorgrant.ErrNotFound, "an expired credential must not resolve")
}

// TestDaemonDeletionRevokesGrants confirms the cascade: a deleted workspace
// must not leave live credentials pointing at a recycled daemon id.
func TestDaemonDeletionRevokesGrants(t *testing.T) {
	store, rawDB, daemonID := setupStore(t)
	ctx := context.Background()

	g := newGrant(daemonID)
	require.NoError(t, store.CreateGrant(ctx, g))

	_, err := rawDB.Exec(`DELETE FROM daemons WHERE id = $1`, daemonID)
	require.NoError(t, err)

	_, err = store.GetGrantByTokenHash(ctx, g.TokenHash)
	require.ErrorIs(t, err, connectorgrant.ErrNotFound, "deleting a daemon must invalidate its grants")
}

func TestStoreRejectsUnusableGrants(t *testing.T) {
	store, _, daemonID := setupStore(t)
	ctx := context.Background()

	t.Run("no tools", func(t *testing.T) {
		g := newGrant(daemonID)
		g.AllowedTools = nil
		require.Error(t, store.CreateGrant(ctx, g), "a grant with no tools must be rejected")
	})

	t.Run("no path root", func(t *testing.T) {
		g := newGrant(daemonID)
		g.PathRoot = ""
		require.Error(t, store.CreateGrant(ctx, g))
	})

	t.Run("no daemon binding", func(t *testing.T) {
		g := newGrant(daemonID)
		g.DaemonID = ""
		require.Error(t, store.CreateGrant(ctx, g), "a grant must name exactly one daemon")
	})

	t.Run("allowlist mode with empty allowlist", func(t *testing.T) {
		g := newGrant(daemonID)
		g.ExecMode = connectorgrant.ExecAllowlist
		require.Error(t, store.CreateGrant(ctx, g))
	})

	t.Run("unknown exec mode", func(t *testing.T) {
		g := newGrant(daemonID)
		g.ExecMode = connectorgrant.ExecMode("yolo")
		require.Error(t, store.CreateGrant(ctx, g))
	})

	t.Run("empty exec mode defaults to deny", func(t *testing.T) {
		g := newGrant(daemonID)
		g.ExecMode = ""
		require.NoError(t, store.CreateGrant(ctx, g), "an unset exec mode should default rather than fail")

		got, err := store.GetGrantByTokenHash(ctx, g.TokenHash)
		require.NoError(t, err)
		require.Equal(t, connectorgrant.ExecDeny, got.ExecMode, "the safe default must be deny")
	})
}

func TestListAndGetGrants(t *testing.T) {
	store, _, daemonID := setupStore(t)
	ctx := context.Background()

	first := newGrant(daemonID)
	require.NoError(t, store.CreateGrant(ctx, first))

	second := newGrant(daemonID)
	second.Name = "Claude mobile"
	require.NoError(t, store.CreateGrant(ctx, second))

	grants, err := store.ListGrantsByUser(ctx, "user-1")
	require.NoError(t, err)
	require.Len(t, grants, 2)

	// Revoked grants stay listed so the UI can show history.
	_, err = store.RevokeGrant(ctx, "user-1", first.ID)
	require.NoError(t, err)

	grants, err = store.ListGrantsByUser(ctx, "user-1")
	require.NoError(t, err)
	require.Len(t, grants, 2)

	got, err := store.GetGrant(ctx, "user-1", first.ID)
	require.NoError(t, err)
	require.NotNil(t, got.RevokedAt)

	// Another user's grant is not reachable by id.
	_, err = store.GetGrant(ctx, "user-2", first.ID)
	require.ErrorIs(t, err, connectorgrant.ErrNotFound)
}

func TestAuditRecording(t *testing.T) {
	store, _, daemonID := setupStore(t)
	ctx := context.Background()

	g := newGrant(daemonID)
	require.NoError(t, store.CreateGrant(ctx, g))

	args, _ := json.Marshal(map[string]string{"path": "/etc/passwd"})
	require.NoError(t, store.RecordAudit(ctx, &connectorgrant.AuditRecord{
		ID:          uuid.New().String(),
		GrantID:     g.ID,
		UserID:      g.UserID,
		DaemonID:    daemonID,
		ToolName:    "read_file",
		CommandType: "fs.read_file",
		Arguments:   args,
		Denied:      true,
		ErrorMsg:    "outside allowed directory",
		DurationMS:  3,
	}))

	require.NoError(t, store.RecordAudit(ctx, &connectorgrant.AuditRecord{
		ID:          uuid.New().String(),
		GrantID:     g.ID,
		UserID:      g.UserID,
		DaemonID:    daemonID,
		ToolName:    "read_file",
		CommandType: "fs.read_file",
		Denied:      false,
		DurationMS:  12,
	}))

	records, err := store.ListAuditByUser(ctx, g.UserID, 10)
	require.NoError(t, err)
	require.Len(t, records, 2)

	byGrant, err := store.ListAuditByGrant(ctx, g.UserID, g.ID, 10)
	require.NoError(t, err)
	require.Len(t, byGrant, 2)

	// Denials must be preserved verbatim: they are the signal that a connector
	// was talked into trying something it shouldn't.
	var denied *connectorgrant.AuditRecord
	for _, r := range byGrant {
		if r.Denied {
			denied = r
		}
	}
	require.NotNil(t, denied)
	require.Contains(t, denied.ErrorMsg, "outside allowed directory")
	require.Contains(t, string(denied.Arguments), "/etc/passwd")

	// Another user cannot read this log.
	other, err := store.ListAuditByUser(ctx, "user-2", 10)
	require.NoError(t, err)
	require.Empty(t, other)
}

// TestAuditSurvivesGrantRevocation guards the reason audit rows carry no
// foreign key: deleting a connector must not erase what it did.
func TestAuditSurvivesGrantRevocation(t *testing.T) {
	store, rawDB, daemonID := setupStore(t)
	ctx := context.Background()

	g := newGrant(daemonID)
	require.NoError(t, store.CreateGrant(ctx, g))
	require.NoError(t, store.RecordAudit(ctx, &connectorgrant.AuditRecord{
		ID:          uuid.New().String(),
		GrantID:     g.ID,
		UserID:      g.UserID,
		DaemonID:    daemonID,
		ToolName:    "run_command",
		CommandType: "exec.run",
	}))

	_, err := rawDB.Exec(`DELETE FROM connector_grants WHERE id = $1`, g.ID)
	require.NoError(t, err)

	records, err := store.ListAuditByUser(ctx, g.UserID, 10)
	require.NoError(t, err)
	require.Len(t, records, 1, "audit history must outlive the grant it describes")
}

func TestTouchGrant(t *testing.T) {
	store, _, daemonID := setupStore(t)
	ctx := context.Background()

	g := newGrant(daemonID)
	require.NoError(t, store.CreateGrant(ctx, g))
	require.NoError(t, store.TouchGrant(ctx, g.ID))

	got, err := store.GetGrant(ctx, g.UserID, g.ID)
	require.NoError(t, err)
	require.NotNil(t, got.LastUsedAt)
}

// TestToPolicy checks the conversion that turns stored consent into the
// enforceable policy the daemon receives.
func TestToPolicy(t *testing.T) {
	identity := func(names []string) []string { return names }

	t.Run("deny mode", func(t *testing.T) {
		g := &connectorgrant.Grant{
			ID:           "g1",
			AllowedTools: []string{"fs.read_file"},
			PathRoot:     "/workspace",
			ExecMode:     connectorgrant.ExecDeny,
		}
		p := g.ToPolicy(identity)
		require.Equal(t, daemonpolicy.ExecDenied, p.ExecMode)
		require.True(t, p.Tools["fs.read_file"])
		require.Equal(t, "/workspace", p.PathRoot)
	})

	t.Run("allowlist mode", func(t *testing.T) {
		g := &connectorgrant.Grant{
			ID:            "g2",
			AllowedTools:  []string{"exec.run"},
			PathRoot:      "/workspace",
			ExecMode:      connectorgrant.ExecAllowlist,
			ExecAllowlist: []string{"git"},
		}
		p := g.ToPolicy(identity)
		require.Equal(t, daemonpolicy.ExecAllowlist, p.ExecMode)
		require.True(t, p.ExecAllowlist["git"])
	})

	// A stored mode that somehow escaped the CHECK constraint must convert to
	// the most restrictive policy, not the most permissive.
	t.Run("unknown mode denies exec", func(t *testing.T) {
		g := &connectorgrant.Grant{
			ID:           "g3",
			AllowedTools: []string{"exec.run"},
			PathRoot:     "/workspace",
			ExecMode:     connectorgrant.ExecMode("corrupted"),
		}
		require.Equal(t, daemonpolicy.ExecDenied, g.ToPolicy(identity).ExecMode)
	})

	t.Run("nil grant yields nil policy", func(t *testing.T) {
		var g *connectorgrant.Grant
		require.Nil(t, g.ToPolicy(identity))
	})
}

func TestGrantLiveness(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Hour)

	require.NoError(t, (&connectorgrant.Grant{}).IsLive(now))
	require.ErrorIs(t, (&connectorgrant.Grant{RevokedAt: &past}).IsLive(now), connectorgrant.ErrRevoked)
	require.ErrorIs(t, (&connectorgrant.Grant{ExpiresAt: &past}).IsLive(now), connectorgrant.ErrExpired)

	var nilGrant *connectorgrant.Grant
	require.True(t, errors.Is(nilGrant.IsLive(now), connectorgrant.ErrNotFound))
}

// TestAuditTwoPhaseWrite covers the lifecycle that makes the log survive a
// crash: an intent row is durable before the command is dispatched, and is
// resolved afterward.
func TestAuditTwoPhaseWrite(t *testing.T) {
	store, _, daemonID := setupStore(t)
	ctx := context.Background()

	g := newGrant(daemonID)
	require.NoError(t, store.CreateGrant(ctx, g))

	id := uuid.New().String()
	require.NoError(t, store.RecordAudit(ctx, &connectorgrant.AuditRecord{
		ID:          id,
		GrantID:     g.ID,
		UserID:      g.UserID,
		DaemonID:    daemonID,
		ToolName:    "run_command",
		CommandType: "exec.run",
		Status:      connectorgrant.AuditStarted,
	}))

	// Visible as started before any outcome is known.
	records, err := store.ListAuditByGrant(ctx, g.UserID, g.ID, 10)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, connectorgrant.AuditStarted, records[0].Status)

	require.NoError(t, store.CompleteAudit(ctx, id, connectorgrant.AuditCompleted, "", 42))

	records, err = store.ListAuditByGrant(ctx, g.UserID, g.ID, 10)
	require.NoError(t, err)
	require.Equal(t, connectorgrant.AuditCompleted, records[0].Status)
	require.Equal(t, 42, records[0].DurationMS)
	require.False(t, records[0].Denied)
}

// TestCompleteAuditIsIdempotent: a late or duplicated completion must not
// overwrite an outcome already recorded.
func TestCompleteAuditIsIdempotent(t *testing.T) {
	store, _, daemonID := setupStore(t)
	ctx := context.Background()

	g := newGrant(daemonID)
	require.NoError(t, store.CreateGrant(ctx, g))

	id := uuid.New().String()
	require.NoError(t, store.RecordAudit(ctx, &connectorgrant.AuditRecord{
		ID: id, GrantID: g.ID, UserID: g.UserID, DaemonID: daemonID,
		ToolName: "read_file", CommandType: "fs.read_file",
		Status: connectorgrant.AuditStarted,
	}))

	require.NoError(t, store.CompleteAudit(ctx, id, connectorgrant.AuditCompleted, "", 10))
	require.NoError(t, store.CompleteAudit(ctx, id, connectorgrant.AuditDenied, "late", 999))

	records, err := store.ListAuditByGrant(ctx, g.UserID, g.ID, 10)
	require.NoError(t, err)
	require.Equal(t, connectorgrant.AuditCompleted, records[0].Status, "a second completion must not overwrite the first")
	require.Equal(t, 10, records[0].DurationMS)
}

// TestGetGrantByIDReturnsLiveGrantsOnly backs the MCP server's per-call
// re-resolution: a grant revoked mid-session must stop resolving.
func TestGetGrantByIDReturnsLiveGrantsOnly(t *testing.T) {
	store, _, daemonID := setupStore(t)
	ctx := context.Background()

	g := newGrant(daemonID)
	require.NoError(t, store.CreateGrant(ctx, g))

	got, err := store.GetGrantByID(ctx, g.ID)
	require.NoError(t, err)
	require.Equal(t, g.ID, got.ID)

	_, err = store.RevokeGrant(ctx, g.UserID, g.ID)
	require.NoError(t, err)

	_, err = store.GetGrantByID(ctx, g.ID)
	require.ErrorIs(t, err, connectorgrant.ErrNotFound,
		"a revoked grant must not re-resolve for an open session")
}
