// Copyright (c) 2025 Reliant Labs

package connections_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

// A poll has no run, so ForCall cannot serve it. ForTrigger resolves the
// trigger's connection with the trigger ROW as the only source of identity:
// these tests pin that it gives exactly ForCall's guarantees — owner-only,
// active only, refreshed, host-pinned, audited — and that nothing a poll
// input could carry reaches another user's credential.

// newTrigger stores an integration trigger owned by userID that listens
// through connID (nil for none). It returns the trigger id.
func (e *env) newTrigger(userID, integration string, connID *string) string {
	e.t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	projectID := "proj-" + uuid.NewString()
	require.NoError(e.t, e.repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "p", Path: e.t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	cfg, err := json.Marshal(core.IntegrationConfig{Integration: integration, Events: []string{"item.created"}})
	require.NoError(e.t, err)
	id := "trg-" + uuid.NewString()
	require.NoError(e.t, e.repo.CreateTrigger(ctx, &core.Trigger{
		ID: id, UserID: userID, ProjectID: projectID, Name: "t-" + id, Kind: core.TriggerKindIntegration,
		Enabled: true, Workflow: "builtin://agent", DaemonID: "d1", Config: cfg, ConnectionID: connID,
		CreatedAt: now, UpdatedAt: now,
	}))
	return id
}

func serverSite(triggerID string) connections.TriggerSite {
	return connections.TriggerSite{TriggerID: triggerID, Placement: connections.PlacementServer}
}

func TestForTrigger_ResolvesTheOwnersConnectionWithNoRun(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	conn := e.connect("alice", "a")
	trigger := e.newTrigger("alice", "github", &conn.ID)

	got, err := e.resolver.ForTrigger(ctx, serverSite(trigger))
	require.NoError(t, err, "a poll has no run id; the trigger row is enough")
	require.Equal(t, conn.ID, got.ConnectionID)
	require.Equal(t, "github", got.IntegrationID)

	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	require.NoError(t, got.Apply(req))
	require.Regexp(t, `^Bearer ghu_access_\d+$`, req.Header.Get("Authorization"))

	require.Equal(t, 1, e.count(
		`SELECT count(*) FROM connection_events WHERE connection_id=$1 AND kind='used' AND actor=$2 AND run_id IS NULL`,
		conn.ID, "trigger:"+trigger), "the use is audited to the trigger, with no run")
}

// The heart of it: the trigger row names alice's connection but belongs to
// bob. Whoever wrote that row (a bug, a hand edit, a forged activity input
// that only carries a trigger id), bob's poll must not read alice's mailbox.
func TestForTrigger_AForeignConnectionOnTheRowIsRefused(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.connect("alice", "a")
	trigger := e.newTrigger("bob", "github", &alice.ID)

	got, err := e.resolver.ForTrigger(ctx, serverSite(trigger))
	require.Nil(t, got)
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
	require.Zero(t, e.count(`SELECT count(*) FROM connection_events WHERE kind='used'`), "nothing was used")

	// A foreign id reads exactly like one of bob's own that is gone (the FK
	// keeps a trigger from naming an id that never existed; a deleted
	// connection's row is kept for audit), so a trigger row cannot be used to
	// probe for another user's connection ids.
	e.gh.accountID, e.gh.login = 77, "bobby"
	bobs := e.connect("bob", "b")
	require.NoError(t, e.svc.Delete(ctx, "bob", bobs.ID))
	_, goneErr := e.resolver.ForTrigger(ctx, serverSite(e.newTrigger("bob", "github", &bobs.ID)))
	require.Error(t, goneErr)
	require.Equal(t, goneErr.Error(), err.Error())
}

func TestForTrigger_RefusesWhatForCallRefuses(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	t.Run("unknown trigger", func(t *testing.T) {
		_, err := e.resolver.ForTrigger(ctx, serverSite("trg-does-not-exist"))
		require.ErrorIs(t, err, connections.ErrFailedPrecondition)
	})
	t.Run("no trigger id", func(t *testing.T) {
		_, err := e.resolver.ForTrigger(ctx, serverSite(""))
		require.ErrorIs(t, err, connections.ErrFailedPrecondition)
	})
	t.Run("connection deleted from the row", func(t *testing.T) {
		trigger := e.newTrigger("alice", "github", nil)
		_, err := e.resolver.ForTrigger(ctx, serverSite(trigger))
		require.ErrorIs(t, err, connections.ErrFailedPrecondition)
	})
	t.Run("daemon and unspecified placement", func(t *testing.T) {
		e.gh.accountID, e.gh.login = 101, "placement"
		conn := e.connect("alice", "p")
		trigger := e.newTrigger("alice", "github", &conn.ID)
		for _, p := range []connections.Placement{connections.PlacementDaemon, connections.PlacementUnspecified} {
			_, err := e.resolver.ForTrigger(ctx, connections.TriggerSite{TriggerID: trigger, Placement: p})
			require.ErrorIs(t, err, connections.ErrDaemonPlacement)
		}
	})
	t.Run("connection for another integration", func(t *testing.T) {
		key, err := e.svc.CreateAPIKey(ctx, connections.CreateAPIKeyParams{
			UserID: "alice", IntegrationID: "svc", Name: "k", Kind: connections.APIKeyKindAPIKey,
			Fields: map[string]string{"api_key": "svc_key_value"},
		})
		require.NoError(t, err)
		trigger := e.newTrigger("alice", "github", &key.ID)
		_, err = e.resolver.ForTrigger(ctx, serverSite(trigger))
		require.ErrorIs(t, err, connections.ErrFailedPrecondition, "a svc key must never authenticate a github poll")
	})
	t.Run("revoked connection", func(t *testing.T) {
		e.gh.accountID, e.gh.login = 102, "revoked"
		conn := e.connect("alice", "r")
		_, err := e.raw.Exec(`UPDATE connections SET status='revoked' WHERE id=$1`, conn.ID)
		require.NoError(t, err)
		trigger := e.newTrigger("alice", "github", &conn.ID)
		_, err = e.resolver.ForTrigger(ctx, serverSite(trigger))
		require.ErrorIs(t, err, connections.ErrFailedPrecondition)
	})
}

func TestForTrigger_NeedsReauthIsTyped(t *testing.T) {
	e := newEnv(t)
	conn := e.connect("alice", "a")
	trigger := e.newTrigger("alice", "github", &conn.ID)
	_, err := e.raw.Exec(`UPDATE connections SET status='needs_reauth', status_reason='invalid_grant' WHERE id=$1`, conn.ID)
	require.NoError(t, err)

	_, err = e.resolver.ForTrigger(context.Background(), serverSite(trigger))
	require.ErrorIs(t, err, connections.ErrNeedsReauth)
}

// The Gmail testing-mode reality: refresh tokens die after seven days. The
// refusal must flip the connection to needs_reauth (typed), not loop.
func TestForTrigger_RefreshRotatesAndADeadGrantNeedsReauth(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	conn := e.connect("alice", "a")
	trigger := e.newTrigger("alice", "github", &conn.ID)

	e.expireAccessToken(conn.ID)
	e.gh.nextAccess, e.gh.nextRefresh = "ghu_ROTATED", "ghr_ROTATED"
	got, err := e.resolver.ForTrigger(ctx, serverSite(trigger))
	require.NoError(t, err)
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	require.NoError(t, got.Apply(req))
	require.Equal(t, "Bearer ghu_ROTATED", req.Header.Get("Authorization"), "an expiring token is refreshed")
	require.Equal(t, 1, int(e.gh.refreshCalls.Load()))

	e.expireAccessToken(conn.ID)
	e.tokens.Forget(conn.ID)
	e.gh.refreshStatus, e.gh.refreshBody = 400, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`
	_, err = e.resolver.ForTrigger(ctx, serverSite(trigger))
	require.ErrorIs(t, err, connections.ErrNeedsReauth)
	require.Equal(t, "needs_reauth", mustStatus(t, e, conn.ID))

	calls := e.gh.refreshCalls.Load()
	_, err = e.resolver.ForTrigger(ctx, serverSite(trigger))
	require.ErrorIs(t, err, connections.ErrNeedsReauth)
	require.Equal(t, calls, e.gh.refreshCalls.Load(), "a dead grant is not retried")
}

// A poller builds its own requests, so the credential itself refuses any host
// the integration does not declare, and plain http. ForCall's Resolved gets
// the same pin: the declarative runner already enforces it, a go: executor
// might not.
func TestResolved_AppliesOnlyToTheIntegrationsHosts(t *testing.T) {
	e := newEnv(t)
	conn := e.connect("alice", "a")
	trigger := e.newTrigger("alice", "github", &conn.ID)
	viaTrigger, err := e.resolver.ForTrigger(context.Background(), serverSite(trigger))
	require.NoError(t, err)
	viaRun, err := e.resolver.ForCall(context.Background(),
		connections.CallSite{RunID: e.newRun("alice"), Placement: connections.PlacementServer}, connections.Ref{ConnectionID: conn.ID})
	require.NoError(t, err)

	for name, got := range map[string]*connections.Resolved{"ForTrigger": viaTrigger, "ForCall": viaRun} {
		for _, target := range []string{
			"https://evil.example/user",
			"https://api.github.com.evil.example/user",
			"http://api.github.com/user",
		} {
			req, _ := http.NewRequest(http.MethodGet, target, nil)
			require.Error(t, got.Apply(req), "%s: %s", name, target)
			require.Empty(t, req.Header.Get("Authorization"), "%s: nothing is written for %s", name, target)
		}
		req, _ := http.NewRequest(http.MethodGet, "https://API.GITHUB.COM/user", nil)
		require.NoError(t, got.Apply(req), "%s: host match is case-insensitive", name)
	}
}

// A token refused before it expired (revoked at the provider) is replaced by a
// refresh, through the same single-flight and generation rules as an expiry
// refresh; a refused refresh grant marks the connection needs_reauth.
func TestResolved_RefreshAfterRejection(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	conn := e.connect("alice", "a")
	trigger := e.newTrigger("alice", "github", &conn.ID)
	got, err := e.resolver.ForTrigger(ctx, serverSite(trigger))
	require.NoError(t, err)
	refreshesBefore := e.gh.refreshCalls.Load()

	e.gh.nextAccess, e.gh.nextRefresh = "ghu_AFTER_REVOKE", "ghr_AFTER_REVOKE"
	next, err := got.RefreshAfterRejection(ctx)
	require.NoError(t, err, "the token had not expired; a rejection still refreshes")
	require.Equal(t, refreshesBefore+1, e.gh.refreshCalls.Load())
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	require.NoError(t, next.Apply(req))
	require.Equal(t, "Bearer ghu_AFTER_REVOKE", req.Header.Get("Authorization"))
	bad, _ := http.NewRequest(http.MethodGet, "https://evil.example/", nil)
	require.Error(t, next.Apply(bad), "the replacement keeps the host pin")

	// A second caller that saw the SAME rejected token gets the replacement
	// without another provider call (two pollers racing on one revocation).
	again, err := got.RefreshAfterRejection(ctx)
	require.NoError(t, err)
	require.Equal(t, refreshesBefore+1, e.gh.refreshCalls.Load(), "one rejection, one refresh")
	req2, _ := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	require.NoError(t, again.Apply(req2))
	require.Equal(t, "Bearer ghu_AFTER_REVOKE", req2.Header.Get("Authorization"))

	// The replacement is refused too, and the grant is dead.
	e.gh.refreshStatus, e.gh.refreshBody = 400, `{"error":"invalid_grant"}`
	_, err = next.RefreshAfterRejection(ctx)
	require.ErrorIs(t, err, connections.ErrNeedsReauth)
	require.Equal(t, "needs_reauth", mustStatus(t, e, conn.ID))
}

// An API key cannot be refreshed: a refused one marks the connection
// needs_reauth, because sending it again would only be refused again.
func TestResolved_RejectedAPIKeyNeedsReauth(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pat, err := e.svc.CreateAPIKey(ctx, connections.CreateAPIKeyParams{
		UserID: "alice", IntegrationID: "github", Name: "pat", Kind: connections.APIKeyKindAPIKey,
		Fields: map[string]string{"api_key": "ghp_revoked"},
	})
	require.NoError(t, err)
	got, err := e.resolver.ForTrigger(ctx, serverSite(e.newTrigger("alice", "github", &pat.ID)))
	require.NoError(t, err)
	_, err = got.RefreshAfterRejection(ctx)
	require.ErrorIs(t, err, connections.ErrNeedsReauth)
	require.Equal(t, "needs_reauth", mustStatus(t, e, pat.ID))
}
