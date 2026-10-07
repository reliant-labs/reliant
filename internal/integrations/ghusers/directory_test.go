// Copyright (c) 2025 Reliant Labs

package ghusers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type tokenFunc func(ctx context.Context, userID string) (string, error)

func (f tokenFunc) Token(ctx context.Context, userID string) (string, error) { return f(ctx, userID) }

func ownToken(_ context.Context, userID string) (string, error) { return "tok-" + userID, nil }

// fakeGitHub answers /users/{login} and /user/{id} from a fixed directory,
// recording what it was asked and with which token. Nothing reaches GitHub.
type fakeGitHub struct {
	mu     sync.Mutex
	asked  []string
	auth   []string
	status int // when set, every request answers it
	users  []map[string]any
}

func (g *fakeGitHub) serve(t *testing.T) *Directory {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.asked = append(g.asked, r.URL.EscapedPath())
		g.auth = append(g.auth, r.Header.Get("Authorization"))
		status := g.status
		g.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		for _, u := range g.users {
			login, _ := u["login"].(string)
			id, _ := u["id"].(int)
			if (strings.HasPrefix(r.URL.Path, "/users/") && strings.EqualFold(strings.TrimPrefix(r.URL.Path, "/users/"), login)) ||
				r.URL.Path == "/user/"+itoa(id) {
				_ = json.NewEncoder(w).Encode(u)
				return
			}
		}
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return New(tokenFunc(ownToken), Options{APIBaseURL: srv.URL, HTTPClient: srv.Client()})
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

var people = []map[string]any{
	{"login": "OctoCat", "id": 583231, "type": "User"},
	{"login": "dependabot[bot]", "id": 49699333, "type": "Bot"},
	{"login": "acme", "id": 1000, "type": "Organization"},
}

// A login resolves to the numeric id trigger.sender.id carries, and an id to
// the login GitHub reports for it now — both asked with the caller's token.
func TestResolveLoginsToIDsAndIDsToCurrentLogins(t *testing.T) {
	gh := &fakeGitHub{users: people}
	dir := gh.serve(t)

	found, err := dir.Resolve(context.Background(), "alice", []string{"@octocat", "dependabot[bot]"}, []string{"583231"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []Found{
		{Query: "@octocat", User: User{ID: "583231", Login: "OctoCat"}},
		{Query: "dependabot[bot]", User: User{ID: "49699333", Login: "dependabot[bot]"}},
		{Query: "583231", User: User{ID: "583231", Login: "OctoCat"}},
	}, found)
	assert.ElementsMatch(t, []string{"/users/octocat", "/users/dependabot%5Bbot%5D", "/user/583231"}, gh.asked)
	for _, a := range gh.auth {
		assert.Equal(t, "Bearer tok-alice", a, "the caller's own token")
	}
}

// A renamed account is found by its id under its NEW login; its old login,
// once someone else registers it, resolves to that someone else's id.
func TestResolveFollowsARename(t *testing.T) {
	gh := &fakeGitHub{users: []map[string]any{
		{"login": "octo-renamed", "id": 583231, "type": "User"},
		{"login": "octocat", "id": 99999999, "type": "User"},
	}}
	dir := gh.serve(t)

	found, err := dir.Resolve(context.Background(), "alice", []string{"octocat"}, []string{"583231"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []Found{
		{Query: "octocat", User: User{ID: "99999999", Login: "octocat"}},
		{Query: "583231", User: User{ID: "583231", Login: "octo-renamed"}},
	}, found)
}

// Unknown people, organizations and strings that are not logins or ids are
// absent, not errors; the last never reach GitHub at all.
func TestResolveLeavesOutWhatIsNotAPerson(t *testing.T) {
	gh := &fakeGitHub{users: people}
	dir := gh.serve(t)

	found, err := dir.Resolve(context.Background(), "alice",
		[]string{"nobody-here", "acme", "../admin", "a/b", "", "-leading"}, []string{"0", "12abc", "-1"})
	require.NoError(t, err)
	assert.Empty(t, found)
	assert.ElementsMatch(t, []string{"/users/nobody-here", "/users/acme"}, gh.asked)
}

// The caller's token problem is theirs to fix, and is returned as is; GitHub
// refusing the token, rate-limiting or failing is an error, never "no such
// user".
func TestResolveErrors(t *testing.T) {
	notConnected := errors.New("not connected")
	dir := New(tokenFunc(func(context.Context, string) (string, error) { return "", notConnected }), Options{})
	_, err := dir.Resolve(context.Background(), "alice", []string{"octocat"}, nil)
	require.ErrorIs(t, err, notConnected)

	for status, want := range map[int]error{http.StatusUnauthorized: ErrTokenRejected, http.StatusForbidden: nil, http.StatusBadGateway: nil} {
		gh := &fakeGitHub{status: status}
		_, err := gh.serve(t).Resolve(context.Background(), "alice", []string{"octocat"}, nil)
		require.Error(t, err, status)
		if want != nil {
			assert.ErrorIs(t, err, want)
		}
	}

	many := make([]string, MaxQueries+1)
	for i := range many {
		many[i] = "user" + itoa(i)
	}
	_, err = (&fakeGitHub{}).serve(t).Resolve(context.Background(), "alice", many, nil)
	require.ErrorIs(t, err, ErrTooManyQueries)
}

// Nothing to ask is no request, and needs no token.
func TestResolveNothingAsksNothing(t *testing.T) {
	dir := New(tokenFunc(func(context.Context, string) (string, error) {
		t.Fatal("no token is needed to look nobody up")
		return "", nil
	}), Options{})
	found, err := dir.Resolve(context.Background(), "alice", nil, []string{" "})
	require.NoError(t, err)
	assert.Empty(t, found)
}
