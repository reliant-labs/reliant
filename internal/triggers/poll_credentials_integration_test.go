// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/connauth"
	"github.com/reliant-labs/reliant/internal/vault"
)

// These drive the REAL poll activity through the REAL credential path — the
// connauth source over connections.Resolver.ForTrigger, the vault, the token
// source and the database — with only the provider's token endpoint faked.
// The fake-repo tests in poll_test.go pin the activity's decisions; these pin
// that the trigger row, and nothing else, decides whose credential a poller
// gets, and that a dead refresh grant lands in the stored registration.

// credPoller records the credential it was handed and applies it to a
// request at the integration's host, the way a real poller would.
type credPoller struct {
	seen []string // Authorization headers the poller could produce
	errs []error
}

func (p *credPoller) Poll(_ context.Context, req PollRequest) (*PollResult, error) {
	if req.Credential == nil {
		p.errs = append(p.errs, nil)
		return &PollResult{Cursor: "c"}, nil
	}
	r, _ := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	err := req.Credential.Apply(r)
	p.errs = append(p.errs, err)
	p.seen = append(p.seen, r.Header.Get("Authorization"))
	return &PollResult{Cursor: "c"}, nil
}

// tokenEndpoint answers GitHub's token endpoint: refreshes succeed unless
// dead is set, in which case the grant is refused (invalid_grant).
type tokenEndpoint struct {
	srv       *httptest.Server
	dead      bool
	refreshes int
}

func newTokenEndpoint(t *testing.T) *tokenEndpoint {
	te := &tokenEndpoint{}
	te.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		te.refreshes++
		if te.dead {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "ghu_refreshed", "refresh_token": "ghr_next", "expires_in": 3600, "token_type": "bearer"})
	}))
	t.Cleanup(te.srv.Close)
	return te
}

// Do sends every provider call to the fake, whatever host the catalog names.
func (te *tokenEndpoint) Do(req *http.Request) (*http.Response, error) {
	u, _ := url.Parse(te.srv.URL)
	cp := req.Clone(req.Context())
	cp.URL.Scheme, cp.URL.Host, cp.Host = u.Scheme, u.Host, u.Host
	return te.srv.Client().Do(cp)
}

type pollCredEnv struct {
	repo   *db.Repo
	store  core.ConnectionStore
	vault  *vault.Vault
	tokens *tokenEndpoint
	poller *credPoller
	tp     *TriggerPoller
}

func newPollCredEnv(t *testing.T) *pollCredEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("needs a database; skipped under -short")
	}
	repo, raw, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)
	k := make([]byte, 32)
	_, err := rand.Read(k)
	require.NoError(t, err)
	ring, err := crypto.ParseKeyring("v1:" + base64.StdEncoding.EncodeToString(k))
	require.NoError(t, err)
	v := vault.New(raw, vault.NewEnvKeyWrapper(ring))
	providers, err := connections.ProvidersFromCatalog(catalog.MustBuiltin().Manifests(), func(key string) string {
		return map[string]string{"RELIANT_OAUTH_GITHUB_CLIENT_ID": "cid", "RELIANT_OAUTH_GITHUB_CLIENT_SECRET": "csecret"}[key]
	})
	require.NoError(t, err)
	te := newTokenEndpoint(t)
	store := repo.Connections()
	source := connauth.New(connections.NewResolver(repo, store, connections.NewTokenSource(store, v, providers, te)))
	poller := &credPoller{}
	tp := NewTriggerPoller(
		pollRepoOver{Repo: repo, conns: store},
		fakePollers{"github": poller},
		NewIntake(repo, &recordingStarter{}, ""),
		source,
	)
	return &pollCredEnv{repo: repo, store: store, vault: v, tokens: te, poller: poller, tp: tp}
}

// pollRepoOver adds the owner-scoped connection read, as the worker does.
type pollRepoOver struct {
	*db.Repo
	conns core.ConnectionStore
}

func (r pollRepoOver) GetConnection(ctx context.Context, userID, id string) (*core.Connection, error) {
	return r.conns.GetConnection(ctx, userID, id)
}

// oauthConnection stores an oauth2 github connection for userID whose access
// token is accessToken, already expired when expired is set.
func (e *pollCredEnv) oauthConnection(t *testing.T, userID, accessToken string, expired bool) *core.Connection {
	t.Helper()
	ctx := context.Background()
	id := "conn_" + uuid.NewString()
	seal := func(field, value string) core.ConnectionSecret {
		ct, err := e.vault.Seal(ctx, vault.UserTenant(userID), []byte(value), connections.SecretAAD(id, field))
		require.NoError(t, err)
		keyID, err := vault.KeyIDOf(ct)
		require.NoError(t, err)
		return core.ConnectionSecret{ConnectionID: id, Field: field, VaultKeyID: keyID, Ciphertext: ct}
	}
	expires := time.Now().Add(time.Hour)
	if expired {
		expires = time.Now().Add(-time.Minute)
	}
	external := uuid.NewString()
	conn := &core.Connection{
		ID: id, OwnerKind: core.ConnectionOwnerUser, UserID: userID, IntegrationID: "github",
		AuthKind: core.ConnectionAuthOAuth2, Name: "gh-" + external[:8], Status: core.ConnectionStatusActive,
		AccessExpiresAt: &expires, ExternalAccountID: &external,
	}
	require.NoError(t, e.store.CreateConnection(ctx, conn,
		[]core.ConnectionSecret{seal(core.SecretFieldAccessToken, accessToken), seal(core.SecretFieldRefreshToken, "ghr_"+userID)},
		core.ConnectionEvent{UserID: userID, Kind: core.ConnectionEventCreated, Actor: connections.ActorUser(userID)}))
	return conn
}

func (e *pollCredEnv) githubTrigger(t *testing.T, userID string, connID string) *core.Trigger {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	projectID := "proj-" + uuid.NewString()
	require.NoError(t, e.repo.CreateProject(ctx, &db.Project{ID: projectID, UserID: userID, Name: "p", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now}))
	cfg, err := json.Marshal(core.IntegrationConfig{Integration: "github", Events: []string{"issues.opened"}})
	require.NoError(t, err)
	trigger := &core.Trigger{
		ID: "trg-" + uuid.NewString(), UserID: userID, ProjectID: projectID, Name: "poll", Kind: core.TriggerKindIntegration,
		Enabled: true, Workflow: "builtin://agent", DaemonID: "d", Config: cfg, ConnectionID: &connID, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, e.repo.CreateTrigger(ctx, trigger))
	return trigger
}

func TestPollCredentialIsTheTriggerOwnersAndNobodyElses(t *testing.T) {
	e := newPollCredEnv(t)
	ctx := context.Background()
	alice := e.oauthConnection(t, "alice", "ghu_ALICE", false)
	bob := e.oauthConnection(t, "bob", "ghu_BOB", false)
	aliceTrigger := e.githubTrigger(t, "alice", alice.ID)

	out, err := e.tp.Poll(ctx, PollInput{TriggerID: aliceTrigger.ID})
	require.NoError(t, err)
	assert.False(t, out.Skipped, out.Reason)
	require.Len(t, e.poller.seen, 1)
	assert.Equal(t, "Bearer ghu_ALICE", e.poller.seen[0])

	// A trigger row that names bob's connection but is owned by alice — the
	// one way a poll could be aimed at another account — gets nothing. The
	// activity's own pre-check refuses it, and so does the resolver: point
	// the activity at a repo whose pre-check is blind, and the resolver
	// still reads the row and refuses.
	stolen := e.githubTrigger(t, "alice", bob.ID)
	out, err = e.tp.Poll(ctx, PollInput{TriggerID: stolen.ID})
	require.NoError(t, err)
	assert.True(t, out.Skipped)

	blind := NewTriggerPoller(blindConnections{pollRepoOver: e.tp.repo.(pollRepoOver), as: bob}, e.tp.pollers, e.tp.intake, e.tp.creds)
	out, err = blind.Poll(ctx, PollInput{TriggerID: stolen.ID})
	require.NoError(t, err)
	assert.True(t, out.Skipped, "the resolver, not the pre-check, is what keeps a poll on its owner's account")
	assert.Contains(t, out.Reason, "unavailable")
	assert.Len(t, e.poller.seen, 1, "the poller was never handed bob's credential")
}

// blindConnections answers every connection read with as, standing in for a
// pre-check that is wrong. Only the resolver's own read stands in the way.
type blindConnections struct {
	pollRepoOver
	as *core.Connection
}

func (b blindConnections) GetConnection(context.Context, string, string) (*core.Connection, error) {
	cp := *b.as
	cp.UserID = "alice"
	return &cp, nil
}

// Testing-mode Google refresh tokens die after seven days. The refused grant
// marks the connection needs_reauth, and the stored registration says so, with
// the cursor kept and no error (so Temporal does not retry it).
func TestPollDeadRefreshGrantIsRecordedOnTheRegistration(t *testing.T) {
	e := newPollCredEnv(t)
	ctx := context.Background()
	conn := e.oauthConnection(t, "alice", "ghu_stale", true)
	trigger := e.githubTrigger(t, "alice", conn.ID)
	require.NoError(t, e.repo.UpsertTriggerRegistration(ctx, &core.TriggerRegistration{TriggerID: trigger.ID, Provider: "github", Cursor: "42"}))

	e.tokens.dead = true
	out, err := e.tp.Poll(ctx, PollInput{TriggerID: trigger.ID})
	require.NoError(t, err, "a dead grant must not be retried")
	assert.True(t, out.Skipped)
	assert.Empty(t, e.poller.seen)

	reg, err := e.repo.GetTriggerRegistration(ctx, trigger.ID)
	require.NoError(t, err)
	assert.Equal(t, core.TriggerRegistrationNeedsReauth, reg.Status)
	assert.Contains(t, reg.StatusDetail, "reconnect")
	assert.Equal(t, "42", reg.Cursor)
	stored, err := e.store.GetConnection(ctx, "alice", conn.ID)
	require.NoError(t, err)
	assert.Equal(t, core.ConnectionStatusNeedsReauth, stored.Status)

	// The next scheduled poll does not touch the provider again.
	refreshes := e.tokens.refreshes
	_, err = e.tp.Poll(ctx, PollInput{TriggerID: trigger.ID})
	require.NoError(t, err)
	assert.Equal(t, refreshes, e.tokens.refreshes)
}

// The registration's source state round-trips through the real store:
// status_since moves only on a status change (so a stuck needs_reauth is one
// inbox episode), the gap is kept, and the batched read is owner-scoped.
func TestTriggerRegistrationStateInTheStore(t *testing.T) {
	e := newPollCredEnv(t)
	ctx := context.Background()
	conn := e.oauthConnection(t, "alice", "ghu_a", false)
	trigger := e.githubTrigger(t, "alice", conn.ID)
	bobConn := e.oauthConnection(t, "bob", "ghu_b", false)
	bobs := e.githubTrigger(t, "bob", bobConn.ID)

	save := func(reg *core.TriggerRegistration) *core.TriggerRegistration {
		require.NoError(t, e.repo.UpsertTriggerRegistration(ctx, reg))
		got, err := e.repo.GetTriggerRegistration(ctx, reg.TriggerID)
		require.NoError(t, err)
		return got
	}
	first := save(&core.TriggerRegistration{TriggerID: trigger.ID, Provider: "github", Cursor: "1", Status: core.TriggerRegistrationNeedsReauth, StatusDetail: "reconnect"})
	require.False(t, first.StatusSince.IsZero())
	time.Sleep(20 * time.Millisecond)
	polled := time.Now()
	again := save(&core.TriggerRegistration{TriggerID: trigger.ID, Provider: "github", Cursor: "1", Status: core.TriggerRegistrationNeedsReauth, StatusDetail: "reconnect", LastPolledAt: &polled})
	assert.True(t, first.StatusSince.Equal(again.StatusSince), "re-polling a stuck source keeps its episode start")

	time.Sleep(20 * time.Millisecond)
	gapAt := time.Now().UTC().Truncate(time.Microsecond)
	active := save(&core.TriggerRegistration{TriggerID: trigger.ID, Provider: "github", Cursor: "2", Status: core.TriggerRegistrationActive, LastGapAt: &gapAt, LastGapDetail: "lost"})
	assert.True(t, active.StatusSince.After(first.StatusSince), "a status change starts a new episode")
	require.NotNil(t, active.LastGapAt)
	assert.True(t, gapAt.Equal(active.LastGapAt.UTC()))
	assert.Equal(t, "lost", active.LastGapDetail)

	save(&core.TriggerRegistration{TriggerID: bobs.ID, Provider: "github", Cursor: "9"})
	regs, err := e.repo.ListTriggerRegistrations(ctx, "alice", []string{trigger.ID, bobs.ID})
	require.NoError(t, err)
	require.Contains(t, regs, trigger.ID)
	assert.NotContains(t, regs, bobs.ID, "another user's registration never appears")
}
