// Copyright (c) 2025 Reliant Labs

package connections_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

func TestRefresh_NotExpiringDoesNotRefresh(t *testing.T) {
	e := newEnv(t)
	conn := e.connect("alice", "a")
	tok, err := e.tokens.Token(context.Background(), "alice", conn.ID)
	require.NoError(t, err)
	require.Equal(t, 0, int(e.gh.refreshCalls.Load()))
	require.NotZero(t, tok.Len())
}

func TestRefresh_ExpiredTokenRefreshesAndRotates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	conn := e.connect("alice", "a")
	genBefore := e.generation(conn.ID, "access_token")
	e.expireAccessToken(conn.ID)
	e.gh.nextAccess, e.gh.nextRefresh = "ghu_NEW_ACCESS", "ghr_NEW_REFRESH"

	tok, err := e.tokens.Token(ctx, "alice", conn.ID)
	require.NoError(t, err)
	require.Equal(t, len("ghu_NEW_ACCESS"), tok.Len())
	require.Equal(t, 1, int(e.gh.refreshCalls.Load()))
	require.Greater(t, e.generation(conn.ID, "access_token"), genBefore)

	// The rotated refresh token was stored: the next refresh presents it.
	e.expireAccessToken(conn.ID)
	e.tokens.Forget(conn.ID)
	_, err = e.tokens.Token(ctx, "alice", conn.ID)
	require.NoError(t, err)
	e.gh.mu.Lock()
	presented := e.gh.lastForm.Get("refresh_token")
	e.gh.mu.Unlock()
	require.Equal(t, "ghr_NEW_REFRESH", presented, "the rotated refresh token must replace the old one")
	require.Equal(t, 2, e.count(`SELECT count(*) FROM connection_events WHERE connection_id=$1 AND kind='refreshed' AND actor='worker'`, conn.ID))
}

// Twenty concurrent callers on an expiring token cause exactly one refresh POST.
func TestRefresh_SingleFlightInProcess(t *testing.T) {
	e := newEnv(t)
	conn := e.connect("alice", "a")
	e.expireAccessToken(conn.ID)
	e.gh.refreshDelay = 150 * 1000 * 1000 // 150ms: long enough that all callers pile in

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.tokens.Token(context.Background(), "alice", conn.ID)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 1, int(e.gh.refreshCalls.Load()), "single-flight: one refresh for %d concurrent callers", n)
}

// Two TokenSources on one DB stand in for two worker replicas: the row lock and
// the generation make exactly one of them call the provider.
func TestRefresh_TwoWorkersRaceResolvedByGeneration(t *testing.T) {
	e := newEnv(t)
	conn := e.connect("alice", "a")
	e.expireAccessToken(conn.ID)
	e.gh.refreshDelay = 150 * 1000 * 1000
	doer := redirectingDoer{target: e.gh.srv}
	workerA := connections.NewTokenSource(e.store, e.vault, e.providers, doer)
	workerB := connections.NewTokenSource(e.store, e.vault, e.providers, doer)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, w := range []*connections.TokenSource{workerA, workerB} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := w.Token(context.Background(), "alice", conn.ID)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 1, int(e.gh.refreshCalls.Load()), "the loser must read the winner's generation, not refresh again")
	require.Equal(t, "active", mustStatus(t, e, conn.ID), "a lost race must not push the connection into needs_reauth")
}

func mustStatus(t *testing.T, e *env, id string) string {
	t.Helper()
	var s string
	require.NoError(t, e.raw.QueryRow(`SELECT status FROM connections WHERE id=$1`, id).Scan(&s))
	return s
}

func TestRefresh_GenerationCASRejectsStaleWriter(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	conn := e.connect("alice", "a")
	gen := e.generation(conn.ID, "access_token")

	err := e.store.WithSecretsLock(ctx, "alice", conn.ID, func(tx core.SecretsTx) error {
		return tx.Replace(ctx, gen-1, []core.ConnectionSecret{{Field: "access_token", VaultKeyID: "x", Ciphertext: []byte("x")}}, nil)
	})
	require.ErrorIs(t, err, core.ErrGenerationConflict)
	require.Equal(t, gen, e.generation(conn.ID, "access_token"), "a stale writer must change nothing")
}

func TestRefresh_InvalidGrantMarksNeedsReauthWithoutStoringBody(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	conn := e.connect("alice", "a")
	e.expireAccessToken(conn.ID)
	e.gh.refreshStatus = 200
	e.gh.refreshBody = `{"error":"bad_refresh_token","error_description":"LEAKY-PROVIDER-BODY the refresh token ghr_refresh_1 is expired"}`

	_, err := e.tokens.Token(ctx, "alice", conn.ID)
	require.ErrorIs(t, err, connections.ErrNeedsReauth)
	require.NotContains(t, err.Error(), "LEAKY-PROVIDER-BODY")

	got, err := e.svc.Get(ctx, "alice", conn.ID)
	require.NoError(t, err)
	require.Equal(t, "needs_reauth", got.Status)
	require.Equal(t, "invalid_grant", *got.StatusReason)
	require.Equal(t, 1, e.count(`SELECT count(*) FROM connection_events WHERE connection_id=$1 AND kind='needs_reauth'`, conn.ID))
	require.Zero(t, e.count(`SELECT count(*) FROM connections WHERE status_reason ILIKE '%LEAKY%'`))
	require.Zero(t, e.count(`SELECT count(*) FROM connection_events WHERE actor ILIKE '%LEAKY%'`))

	// Further calls fail fast without touching the provider.
	calls := e.gh.refreshCalls.Load()
	_, err = e.tokens.Token(ctx, "alice", conn.ID)
	require.ErrorIs(t, err, connections.ErrNeedsReauth)
	require.Equal(t, calls, e.gh.refreshCalls.Load())
}

func TestRefresh_RFCInvalidGrantAlsoPermanent(t *testing.T) {
	e := newEnv(t)
	conn := e.connect("alice", "a")
	e.expireAccessToken(conn.ID)
	e.gh.refreshStatus = 400
	e.gh.refreshBody = `{"error":"invalid_grant"}`
	_, err := e.tokens.Token(context.Background(), "alice", conn.ID)
	require.ErrorIs(t, err, connections.ErrNeedsReauth)
	require.Equal(t, "needs_reauth", mustStatus(t, e, conn.ID))
}

func TestRefresh_TransientFailureIsRetryableAndLeavesStatus(t *testing.T) {
	e := newEnv(t)
	conn := e.connect("alice", "a")
	e.expireAccessToken(conn.ID)
	e.gh.refreshStatus = 503
	e.gh.refreshBody = `upstream down`
	_, err := e.tokens.Token(context.Background(), "alice", conn.ID)
	require.ErrorIs(t, err, connections.ErrUnavailable)
	require.Equal(t, "active", mustStatus(t, e, conn.ID))

	e.gh.refreshStatus = 0
	tok, err := e.tokens.Token(context.Background(), "alice", conn.ID)
	require.NoError(t, err)
	require.NotZero(t, tok.Len())
}

func TestRefresh_InvalidClientDoesNotCondemnTheConnection(t *testing.T) {
	e := newEnv(t)
	conn := e.connect("alice", "a")
	e.expireAccessToken(conn.ID)
	e.gh.refreshStatus = 401
	e.gh.refreshBody = `{"error":"invalid_client"}`
	_, err := e.tokens.Token(context.Background(), "alice", conn.ID)
	require.Error(t, err)
	require.NotErrorIs(t, err, connections.ErrNeedsReauth, "a deployment misconfiguration is not the user's fault")
	require.Equal(t, "active", mustStatus(t, e, conn.ID))
}

func TestRefresh_MissingRefreshTokenNeedsReauth(t *testing.T) {
	e := newEnv(t)
	conn := e.connect("alice", "a")
	_, err := e.raw.Exec(`DELETE FROM connection_secrets WHERE connection_id=$1 AND field='refresh_token'`, conn.ID)
	require.NoError(t, err)
	e.expireAccessToken(conn.ID)
	_, err = e.tokens.Token(context.Background(), "alice", conn.ID)
	require.ErrorIs(t, err, connections.ErrNeedsReauth)
}

// countingStore counts how many refresh transactions reach the database.
type countingStore struct {
	core.ConnectionStore
	locks atomic.Int32
}

func (c *countingStore) WithSecretsLock(ctx context.Context, userID, id string, fn func(core.SecretsTx) error) error {
	c.locks.Add(1)
	return c.ConnectionStore.WithSecretsLock(ctx, userID, id, fn)
}

// The row lock alone would also yield one provider call, but only after every
// caller queued a transaction on the same row. Single-flight is what keeps
// twenty callers to one database transaction.
func TestRefresh_SingleFlightCollapsesDatabaseTransactions(t *testing.T) {
	e := newEnv(t)
	conn := e.connect("alice", "a")
	e.expireAccessToken(conn.ID)
	e.gh.refreshDelay = 150 * 1000 * 1000
	counting := &countingStore{ConnectionStore: e.store}
	src := connections.NewTokenSource(counting, e.vault, e.providers, redirectingDoer{target: e.gh.srv})

	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := src.Token(context.Background(), "alice", conn.ID)
			require.NoError(t, err)
		}()
	}
	wg.Wait()
	require.Equal(t, 1, int(e.gh.refreshCalls.Load()))
	require.Equal(t, 1, int(counting.locks.Load()), "20 concurrent callers must share one refresh transaction")
}
