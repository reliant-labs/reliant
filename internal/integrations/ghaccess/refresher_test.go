// Copyright (c) 2025 Reliant Labs
package ghaccess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// fakeGitHub serves the three endpoints a refresh reads, per token: who the
// token is, which installations it can reach, and which repositories in
// each. Pages are perPage long, linked with a Link header, as GitHub does.
type fakeGitHub struct {
	t       *testing.T
	srv     *httptest.Server
	perPage int

	mu    sync.Mutex
	users map[string]fakeUser // token -> user
	calls []string
	fail  map[string]int // path prefix -> status
}

type fakeUser struct {
	id            int64
	installations map[int64][]fakeRepo
}

type fakeRepo struct {
	id   int64
	name string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{t: t, perPage: 2, users: map[string]fakeUser{}, fail: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	for prefix, status := range f.fail {
		if strings.HasPrefix(r.URL.Path, prefix) {
			http.Error(w, `{"message":"boom"}`, status)
			return
		}
	}
	assert.Equal(f.t, "application/vnd.github+json", r.Header.Get("Accept"))
	assert.Equal(f.t, "2022-11-28", r.Header.Get("X-GitHub-Api-Version"))
	assert.NotEmpty(f.t, r.Header.Get("User-Agent"))
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	u, ok := f.users[token]
	if !ok {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page == 0 {
		page = 1
	}
	if got := r.URL.Query().Get("per_page"); r.URL.Path != "/user" && got != "100" {
		f.t.Errorf("%s: per_page=%q, want 100 (the most GitHub serves)", r.URL.Path, got)
	}
	switch {
	case r.URL.Path == "/user":
		_ = json.NewEncoder(w).Encode(map[string]any{"id": u.id, "login": "user" + strconv.FormatInt(u.id, 10)})
	case r.URL.Path == "/user/installations":
		var ids []int64
		for id := range u.installations {
			ids = append(ids, id)
		}
		sortInts(ids)
		items := pageOf(ids, page, f.perPage)
		var out []map[string]any
		for _, id := range items {
			out = append(out, map[string]any{"id": id, "account": map[string]any{"login": "org" + strconv.FormatInt(id, 10)}})
		}
		f.link(w, r, page, len(ids))
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(ids), "installations": out})
	case strings.HasPrefix(r.URL.Path, "/user/installations/") && strings.HasSuffix(r.URL.Path, "/repositories"):
		id, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/user/installations/"), "/repositories"), 10, 64)
		repos, ok := u.installations[id]
		if !ok {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		items := pageOf(repos, page, f.perPage)
		var out []map[string]any
		for _, rp := range items {
			out = append(out, map[string]any{"id": rp.id, "full_name": rp.name})
		}
		f.link(w, r, page, len(repos))
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(repos), "repositories": out})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeGitHub) link(w http.ResponseWriter, r *http.Request, page, total int) {
	if page*f.perPage >= total {
		return
	}
	next := *r.URL
	q := next.Query()
	q.Set("page", strconv.Itoa(page+1))
	next.RawQuery = q.Encode()
	w.Header().Set("Link", fmt.Sprintf(`<%s%s>; rel="next", <%s/x?page=99>; rel="last"`, f.srv.URL, next.RequestURI(), f.srv.URL))
}

func pageOf[T any](items []T, page, per int) []T {
	start := (page - 1) * per
	if start >= len(items) {
		return nil
	}
	end := start + per
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}

func sortInts(a []int64) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// fakeStore records what refreshes write.
type fakeStore struct {
	mu       sync.Mutex
	grants   map[string][]core.IntegrationAccessGrant // user -> grants
	subjects map[string]string
	at       map[string]time.Time
	owners   []string
	claimed  map[string]bool
	finished map[string]error
	pruned   int
	replaces int
}

func newFakeStore() *fakeStore {
	return &fakeStore{grants: map[string][]core.IntegrationAccessGrant{}, subjects: map[string]string{},
		at: map[string]time.Time{}, claimed: map[string]bool{}, finished: map[string]error{}}
}

func (s *fakeStore) ReplaceIntegrationAccess(_ context.Context, userID, integrationID, subjectID string, at time.Time, grants []core.IntegrationAccessGrant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if integrationID != "github" {
		return errors.New("wrong integration")
	}
	s.replaces++
	s.grants[userID] = append([]core.IntegrationAccessGrant(nil), grants...)
	s.subjects[userID] = subjectID
	s.at[userID] = at
	return nil
}

func (s *fakeStore) ListIntegrationTriggerOwners(context.Context, string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.owners...), nil
}

func (s *fakeStore) ClaimIntegrationAccessRefresh(_ context.Context, userID, _ string, _, _, _ time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed[userID] {
		return false, nil
	}
	s.claimed[userID] = true
	return true, nil
}

func (s *fakeStore) FinishIntegrationAccessRefresh(_ context.Context, userID, _ string, _ time.Time, err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished[userID] = err
	delete(s.claimed, userID)
	return nil
}

func (s *fakeStore) PruneIntegrationAccess(context.Context, time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruned++
	return 0, nil
}

// staticTokens hands out a fixed token per user, or an error.
type staticTokens map[string]any

func (s staticTokens) Token(_ context.Context, userID string) (string, error) {
	switch v := s[userID].(type) {
	case string:
		return v, nil
	case error:
		return "", v
	}
	return "", ErrNotConnected
}

func newTestRefresher(t *testing.T, gh *fakeGitHub, store *fakeStore, tokens TokenSource) *Refresher {
	t.Helper()
	r := New(store, tokens, Options{APIBaseURL: gh.srv.URL, HTTPClient: gh.srv.Client()})
	r.now = func() time.Time { return time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC) }
	return r
}

func TestRefreshRecordsEveryVisibleRepositoryAcrossPages(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.users["tok-alice"] = fakeUser{id: 11, installations: map[int64][]fakeRepo{
		100: {{1, "acme/app"}, {2, "acme/secret"}, {3, "acme/docs"}},
		200: {{9, "alice/side"}},
		300: {{4, "big/a"}},
	}}
	store := newFakeStore()
	r := newTestRefresher(t, gh, store, staticTokens{"alice": "tok-alice"})

	require.NoError(t, r.Refresh(context.Background(), "alice"))
	assert.ElementsMatch(t, []core.IntegrationAccessGrant{
		{AccountKey: "100", ResourceKey: "1", ResourceLabel: "acme/app"},
		{AccountKey: "100", ResourceKey: "2", ResourceLabel: "acme/secret"},
		{AccountKey: "100", ResourceKey: "3", ResourceLabel: "acme/docs"},
		{AccountKey: "200", ResourceKey: "9", ResourceLabel: "alice/side"},
		{AccountKey: "300", ResourceKey: "4", ResourceLabel: "big/a"},
	}, store.grants["alice"], "both the installation list and each repository list are paginated (2 per page here)")
	assert.Equal(t, "11", store.subjects["alice"], "the GitHub user id, for membership revocations")
	assert.Equal(t, r.now(), store.at["alice"])
}

// Each user's grants come from THEIR token: two members of one org
// installation record different repositories.
func TestRefreshIsPerUserToken(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.users["tok-a"] = fakeUser{id: 11, installations: map[int64][]fakeRepo{100: {{1, "acme/app"}, {2, "acme/secret"}}}}
	gh.users["tok-b"] = fakeUser{id: 22, installations: map[int64][]fakeRepo{100: {{1, "acme/app"}}}}
	store := newFakeStore()
	r := newTestRefresher(t, gh, store, staticTokens{"alice": "tok-a", "bob": "tok-b"})
	require.NoError(t, r.Refresh(context.Background(), "alice"))
	require.NoError(t, r.Refresh(context.Background(), "bob"))
	assert.Len(t, store.grants["alice"], 2)
	assert.Equal(t, []core.IntegrationAccessGrant{{AccountKey: "100", ResourceKey: "1", ResourceLabel: "acme/app"}}, store.grants["bob"])
}

// A permanent token failure (not connected, needs reconnect, a token GitHub
// rejects) clears the user's grants: access nobody can re-confirm is not
// kept. A transient one keeps them, so a GitHub blip does not drop events;
// they still age out at the freshness bound if it persists.
func TestRefreshFailures(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.users["tok-ok"] = fakeUser{id: 1, installations: map[int64][]fakeRepo{100: {{1, "a/b"}}}}
	store := newFakeStore()
	r := newTestRefresher(t, gh, store, staticTokens{
		"gone":    ErrNotConnected,
		"reauth":  ErrNeedsReconnect,
		"blip":    errors.New("control-plane unreachable"),
		"revoked": "tok-rejected",
	})
	ctx := context.Background()

	for _, user := range []string{"gone", "reauth", "revoked"} {
		store.grants[user] = []core.IntegrationAccessGrant{{AccountKey: "100", ResourceKey: "1"}}
		err := r.Refresh(ctx, user)
		require.Error(t, err, user)
		assert.True(t, IsPermanent(err), user)
		assert.Empty(t, store.grants[user], "%s: grants cleared", user)
	}

	store.grants["blip"] = []core.IntegrationAccessGrant{{AccountKey: "100", ResourceKey: "1"}}
	err := r.Refresh(ctx, "blip")
	require.Error(t, err)
	assert.False(t, IsPermanent(err))
	assert.Len(t, store.grants["blip"], 1, "a transient failure keeps the last known access")

	// GitHub failing mid-refresh is transient and records nothing partial.
	gh.fail["/user/installations/100"] = http.StatusBadGateway
	store.grants["ok"] = []core.IntegrationAccessGrant{{AccountKey: "100", ResourceKey: "1"}, {AccountKey: "100", ResourceKey: "2"}}
	r.tokens = staticTokens{"ok": "tok-ok"}
	before := store.replaces
	err = r.Refresh(ctx, "ok")
	require.Error(t, err)
	assert.False(t, IsPermanent(err))
	assert.Equal(t, before, store.replaces, "no half-listed set is ever written")
}

// An installation the token suddenly cannot list (removed between the two
// calls) is skipped, not fatal: it simply contributes no repositories.
func TestRefreshSkipsAnInstallationThatVanished(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.users["tok"] = fakeUser{id: 1, installations: map[int64][]fakeRepo{100: {{1, "a/b"}}, 200: {{2, "c/d"}}}}
	store := newFakeStore()
	r := newTestRefresher(t, gh, store, staticTokens{"u": "tok"})
	gh.fail["/user/installations/200/"] = http.StatusNotFound
	require.NoError(t, r.Refresh(context.Background(), "u"))
	assert.Equal(t, []core.IntegrationAccessGrant{{AccountKey: "100", ResourceKey: "1", ResourceLabel: "a/b"}}, store.grants["u"])
}

// A runaway listing is bounded rather than followed forever.
func TestRefreshBoundsPagination(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.perPage = 1
	repos := make([]fakeRepo, maxPages+5)
	for i := range repos {
		repos[i] = fakeRepo{int64(i + 1), fmt.Sprintf("o/r%d", i)}
	}
	gh.users["tok"] = fakeUser{id: 1, installations: map[int64][]fakeRepo{100: repos}}
	store := newFakeStore()
	r := newTestRefresher(t, gh, store, staticTokens{"u": "tok"})
	err := r.Refresh(context.Background(), "u")
	require.Error(t, err, "a listing that never ends is an error, not a silently partial set")
	assert.Zero(t, store.replaces)
}

// RefreshDue refreshes every trigger owner whose lease it wins, once.
func TestRefreshDueRefreshesEachOwnerOnce(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.users["tok-a"] = fakeUser{id: 11, installations: map[int64][]fakeRepo{100: {{1, "acme/app"}}}}
	store := newFakeStore()
	store.owners = []string{"alice", "nobody"}
	r := newTestRefresher(t, gh, store, staticTokens{"alice": "tok-a"})
	r.RefreshDue(context.Background())
	assert.Len(t, store.grants["alice"], 1)
	assert.NoError(t, store.finished["alice"])
	assert.ErrorIs(t, store.finished["nobody"], ErrNotConnected)
	assert.Equal(t, 1, store.pruned)
}

// The refresher speaks to GitHub with the user's token and nothing else, and
// never logs or returns it.
func TestTokenNeverLeaksIntoErrors(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.fail["/user"] = http.StatusInternalServerError
	r := newTestRefresher(t, gh, newFakeStore(), staticTokens{"u": "ghu_supersecret_token"})
	err := r.Refresh(context.Background(), "u")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "ghu_supersecret_token")
}
