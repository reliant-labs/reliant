package grpc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

func sign(t *testing.T, secret string) string {
	t.Helper()
	token, err := auth.SignInternalServiceToken(secret)
	require.NoError(t, err)
	return token
}

type fakeEnsureStore struct {
	calls []db.EnsureProjectForAppInput
	err   error
}

func (s *fakeEnsureStore) EnsureProjectForApp(_ context.Context, in db.EnsureProjectForAppInput) (db.EnsureProjectForAppResult, error) {
	s.calls = append(s.calls, in)
	return db.EnsureProjectForAppResult{ProjectID: "p1", Created: true}, s.err
}

type fakeMembership struct {
	member, ok bool
	err        error
}

func (m fakeMembership) IsMember(context.Context, string, string) (bool, bool, error) {
	return m.member, m.ok, m.err
}

const ensureBody = `{"org_id":"o1","user_id":"u1","app_name":"shop","remote":"github.com/acme/shop","subpath":"apps/web"}`

func TestEnsureProjectForApp(t *testing.T) {
	t.Parallel()
	secret := "internal-secret"
	member := fakeMembership{member: true, ok: true}
	tests := []struct {
		name     string
		method   string
		auth     string
		secret   string
		body     string
		members  OrgMembership
		storeErr error
		want     int
		wantBody string
		writes   bool
	}{
		{name: "created", want: http.StatusOK, wantBody: `{"project_id":"p1","created":true,"linked_by":"created"}`, writes: true},
		{name: "get refused", method: http.MethodGet, want: http.StatusMethodNotAllowed},
		{name: "no token", auth: "-", want: http.StatusUnauthorized},
		{name: "wrong secret", auth: "Bearer " + sign(t, "other"), want: http.StatusUnauthorized},
		{name: "unset secret", secret: "-", want: http.StatusUnauthorized},
		{name: "bad json", body: `{`, want: http.StatusBadRequest},
		{name: "unknown field", body: `{"org_id":"o1","user_id":"u1","app_name":"shop","remote":"github.com/acme/shop","x":1}`, want: http.StatusBadRequest},
		{name: "trailing data", body: ensureBody + `{}`, want: http.StatusBadRequest},
		{name: "no org", body: `{"user_id":"u1","app_name":"shop","remote":"github.com/acme/shop"}`, want: http.StatusBadRequest},
		{name: "no app", body: `{"org_id":"o1","user_id":"u1","app_name":" ","remote":"github.com/acme/shop"}`, want: http.StatusBadRequest},
		{name: "remote not canonical", body: `{"org_id":"o1","user_id":"u1","app_name":"shop","remote":"https://github.com/acme/shop.git"}`, want: http.StatusBadRequest},
		{name: "local remote", body: `{"org_id":"o1","user_id":"u1","app_name":"shop","remote":"/home/me/shop"}`, want: http.StatusBadRequest},
		{name: "escaping subpath", body: `{"org_id":"o1","user_id":"u1","app_name":"shop","remote":"github.com/acme/shop","subpath":"../x"}`, want: http.StatusBadRequest},
		{name: "not member", members: fakeMembership{ok: true}, want: http.StatusForbidden, wantBody: `{"code":"not_member"}`},
		{name: "no orgs", members: fakeMembership{}, want: http.StatusForbidden, wantBody: `{"code":"not_member"}`},
		{name: "membership outage", members: fakeMembership{ok: true, err: errors.New("down")}, want: http.StatusServiceUnavailable},
		{name: "store error", storeErr: errors.New("db"), want: http.StatusServiceUnavailable, writes: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			method, body, sec, members := http.MethodPost, ensureBody, secret, OrgMembership(member)
			if tt.method != "" {
				method = tt.method
			}
			if tt.body != "" {
				body = tt.body
			}
			if tt.secret == "-" {
				sec = ""
			}
			if tt.members != nil {
				members = tt.members
			}
			auth := "Bearer " + sign(t, secret)
			if tt.auth == "-" {
				auth = ""
			} else if tt.auth != "" {
				auth = tt.auth
			}
			store := &fakeEnsureStore{err: tt.storeErr}
			req := httptest.NewRequest(method, EnsureProjectForAppPath, strings.NewReader(body))
			if auth != "" {
				req.Header.Set("Authorization", auth)
			}
			w := httptest.NewRecorder()
			NewEnsureProjectForAppHandler(store, members, sec).ServeHTTP(w, req)
			require.Equal(t, tt.want, w.Code, w.Body.String())
			if tt.wantBody != "" {
				assert.JSONEq(t, tt.wantBody, w.Body.String())
			}
			if tt.writes {
				require.Len(t, store.calls, 1)
				assert.Equal(t, db.EnsureProjectForAppInput{OrgID: "o1", UserID: "u1", AppName: "shop", Remote: "github.com/acme/shop", Subpath: "apps/web"}, store.calls[0])
			} else {
				assert.Empty(t, store.calls, "nothing is written")
			}
		})
	}
}

func TestEnsureProjectForAppNilMembershipFailsClosed(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, EnsureProjectForAppPath, strings.NewReader(ensureBody))
	req.Header.Set("Authorization", "Bearer "+sign(t, "s"))
	w := httptest.NewRecorder()
	NewEnsureProjectForAppHandler(&fakeEnsureStore{}, nil, "s").ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
