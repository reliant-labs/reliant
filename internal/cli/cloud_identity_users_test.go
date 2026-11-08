package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
)

// identityFake plays the control plane for the per-user verbs: the org and
// environment lookups every identity verb starts with, ListUsers for the
// email -> id lookup, and one recording handler for the verb under test.
type identityFake struct {
	*httptest.Server
	mu      sync.Mutex
	calls   []string
	bodies  map[string]map[string]any
	users   []map[string]any
	replies map[string]string
}

func newIdentityFake(t *testing.T, users ...map[string]any) *identityFake {
	t.Helper()
	f := &identityFake{bodies: map[string]map[string]any{}, users: users, replies: map[string]string{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proc := strings.TrimPrefix(r.URL.Path, "/")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.calls = append(f.calls, proc)
		f.bodies[proc] = body
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch proc {
		case cloud.ProcGetOrganization:
			fmt.Fprintf(w, `{"%s":{"orgId":"org-1"}}`, orgResponseKey)
		case "controlplane.v1.DeployService/ListEnvironments":
			_, _ = w.Write([]byte(`{"environments":[{"id":"env-1","name":"prod","project":"shop"}]}`))
		case procIdentityUsers:
			_ = json.NewEncoder(w).Encode(map[string]any{"users": f.users})
		default:
			if reply, ok := f.replies[proc]; ok {
				_, _ = w.Write([]byte(reply))
				return
			}
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(f.Close)
	identityTestTarget(t, f.URL)
	t.Setenv("IDENTITY_TEST_TOKEN", "human-session")
	return f
}

func (f *identityFake) body(proc string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[proc]
}

func (f *identityFake) called(proc string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == proc {
			return true
		}
	}
	return false
}

func idUser(id, email string) map[string]any {
	return map[string]any{"userId": id, "email": email, "state": "IDENTITY_USER_STATE_ACTIVE"}
}

func TestCloudIdentityUserVerbsWire(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proc  string
		reply string
		args  []string
		extra map[string]any
		want  []string
	}{
		{"set-roles by id", procIdentitySetRoles, `{"user":{"userId":"u1","roles":["editor","viewer"]}}`,
			[]string{"set-roles", "prod", "--user", "u1", "--role", "editor", "--role", "viewer"},
			map[string]any{"roles": []any{"editor", "viewer"}}, []string{"Roles of u1 are now: editor, viewer"}},
		{"set-roles none", procIdentitySetRoles, `{"user":{"userId":"u1"}}`,
			[]string{"set-roles", "prod", "--user", "u1", "--none"},
			map[string]any{"roles": []any{}}, []string{"(none)"}},
		{"reset", procIdentityResetPassword, `{}`, []string{"reset", "prod", "--user", "u1"}, nil, []string{"Password reset link emailed to u1"}},
		{"reset --return-code", procIdentityResetPassword,
			`{"code":{"code":"c0de","expiresAt":"2026-10-21T18:00:00Z","resetUrl":"https://app.example/auth/reset?userID=u1&code=c0de&orgID=o"}}`,
			[]string{"reset", "prod", "--user", "u1", "--return-code"}, map[string]any{"returnCode": true},
			[]string{"Password-set code issued for u1", "a credential", "expires at 2026-10-21T18:00:00Z", "https://app.example/auth/reset?userID=u1&code=c0de&orgID=o"}},
		{"reset --return-code, no reset page", procIdentityResetPassword, `{"code":{"code":"c0de"}}`,
			[]string{"reset", "prod", "--user", "u1", "--return-code"}, map[string]any{"returnCode": true},
			[]string{"code: c0de", "no reset link"}},
		{"reset --return-code --json", procIdentityResetPassword, `{"code":{"code":"c0de","resetUrl":"https://app.example/auth/reset"}}`,
			[]string{"reset", "prod", "--user", "u1", "--return-code", "--json"}, map[string]any{"returnCode": true},
			[]string{`"action": "reset"`, `"code": "c0de"`, `"resetUrl": "https://app.example/auth/reset"`}},
		{"lock", procIdentityLockUser, `{}`, []string{"lock", "prod", "--user", "u1"}, nil, []string{"Locked u1"}},
		{"unlock", procIdentityUnlockUser, `{}`, []string{"unlock", "prod", "--user", "u1"}, nil, []string{"Unlocked u1"}},
		{"delete --yes", procIdentityDeleteUser, `{}`, []string{"delete", "prod", "--user", "u1", "--yes"}, nil, []string{"Deleted u1"}},
		{"revoke-sessions", procIdentityRevokeSessions, `{"sessionsRevoked":3}`, []string{"revoke-sessions", "prod", "--user", "u1"}, nil, []string{"Revoked 3 session(s) of u1"}},
		{"json", procIdentityLockUser, `{}`, []string{"lock", "prod", "--user", "u1", "--json"}, nil, []string{`"ok": true`, `"action": "lock"`, `"userId": "u1"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newIdentityFake(t)
			f.replies[tc.proc] = tc.reply
			out, err := executeIdentity(tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]any{"orgId": "org-1", "environmentId": "env-1", "userId": "u1"}
			for k, v := range tc.extra {
				want[k] = v
			}
			if got := f.body(tc.proc); !reflect.DeepEqual(got, want) {
				t.Errorf("%s request = %#v, want %#v", tc.proc, got, want)
			}
			if f.called(procIdentityUsers) {
				t.Error("a user id must be used as given, not looked up")
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("output missing %q:\n%s", w, out)
				}
			}
		})
	}
}

func TestCloudIdentityResolvesEmail(t *testing.T) {
	f := newIdentityFake(t, idUser("ux", "ba@example.com"), idUser("u7", "Ann@Example.com"))
	out, err := executeIdentity("lock", "prod", "--user", "ann@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.body(procIdentityLockUser)["userId"]; got != "u7" {
		t.Errorf("locked user %v, want u7 (the exact, case-insensitive match; ba@ must not win on substring)", got)
	}
	if q := f.body(procIdentityUsers)["query"]; q != "ann@example.com" {
		t.Errorf("lookup query = %v", q)
	}
	if !strings.Contains(out, "Locked Ann@Example.com (user u7)") {
		t.Errorf("output = %q", out)
	}
}

func TestCloudIdentityEmailLookupRefusals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		users []map[string]any
		want  string
	}{
		{"none", []map[string]any{idUser("u1", "other@example.com")}, "no user with the email"},
		{"ambiguous", []map[string]any{idUser("u1", "a@example.com"), idUser("u2", "A@example.com")}, "refusing to guess"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newIdentityFake(t, tc.users...)
			_, err := executeIdentity("delete", "prod", "--user", "a@example.com", "--yes")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if f.called(procIdentityDeleteUser) {
				t.Error("an unresolved user must not reach the rpc")
			}
		})
	}
}

func TestCloudIdentityDeleteAsksFirst(t *testing.T) {
	for _, tc := range []struct {
		name, stdin string
		deleted     bool
	}{
		{"yes", "y\n", true},
		{"YES", "YES\n", true},
		{"no", "n\n", false},
		{"bare enter", "\n", false},
		{"closed stdin", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newIdentityFake(t)
			cmd := newCloudCmd()
			var out strings.Builder
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetIn(strings.NewReader(tc.stdin))
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
			cmd.SetArgs([]string{"identity", "delete", "prod", "--user", "u1"})
			err := cmd.Execute()
			if got := f.called(procIdentityDeleteUser); got != tc.deleted {
				t.Fatalf("delete rpc called = %v, want %v (err %v)", got, tc.deleted, err)
			}
			if (err == nil) != tc.deleted {
				t.Errorf("err = %v, deleted = %v", err, tc.deleted)
			}
			if !strings.Contains(out.String(), "Delete u1 from prod?") {
				t.Errorf("no confirmation question:\n%s", out.String())
			}
		})
	}
}

func TestCloudIdentityUserVerbParsing(t *testing.T) {
	for _, args := range [][]string{
		{"set-roles", "prod", "--user", "u1"},
		{"set-roles", "prod", "--user", "u1", "--role", "a", "--none"},
		{"set-roles", "prod", "--user", "u1", "--role", ""},
		{"set-roles", "prod", "--role", "a"},
		{"reset", "prod"},
		{"lock", "prod", "--user", ""},
		{"unlock", "prod", "--user", "u1", "extra"},
		{"delete", "prod"},
		{"revoke-sessions", "--user", "u1"},
		{"lock", "prod", "--user", "u1", "--org", "other"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			previous := resolveIdentityCloudTarget
			resolveIdentityCloudTarget = func(context.Context, string, string) (cloud.Endpoint, cloud.Credential, error) {
				t.Fatal("invalid command reached credential resolution")
				return cloud.Endpoint{}, cloud.Credential{}, nil
			}
			t.Cleanup(func() { resolveIdentityCloudTarget = previous })
			if _, err := executeIdentity(args...); err == nil {
				t.Fatal("invalid command succeeded")
			}
		})
	}
	for _, verb := range []string{"set-roles", "reset", "lock", "unlock", "delete", "revoke-sessions"} {
		if out, err := executeIdentity(verb, "--help"); err != nil || !strings.Contains(out, "--user") {
			t.Errorf("%s --help: %v\n%s", verb, err, out)
		}
	}
}

// A Reliant session's credential carries neither identity scope. The control
// plane's refusal names the one a verb needs, and forge trades the session for
// exactly that scope and retries: identity:read for a read, identity:manage
// for a change, each requested only when a call proves it is needed.
func TestCloudIdentityElevatesSessionCredentialPerVerb(t *testing.T) {
	var (
		mu        sync.Mutex
		exchanges []string
		bearers   = map[string][]string{}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proc := strings.TrimPrefix(r.URL.Path, "/")
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		bearers[proc] = append(bearers[proc], bearer)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		deny := func(scope string) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprintf(w, `{"code":"permission_denied","message":"this machine credential does not carry the %s scope"}`, scope)
		}
		switch proc {
		case "controlplane.v1.AccessTokenService/ExchangeToken":
			scopes, _ := body["scopes"].([]any)
			mu.Lock()
			for _, s := range scopes {
				exchanges = append(exchanges, fmt.Sprint(s))
			}
			mu.Unlock()
			_, _ = fmt.Fprintf(w, `{"secret":"elevated-%v","token":{}}`, scopes[0])
		case cloud.ProcGetOrganization:
			fmt.Fprintf(w, `{"%s":{"orgId":"org-1"}}`, orgResponseKey)
		case "controlplane.v1.DeployService/ListEnvironments":
			_, _ = w.Write([]byte(`{"environments":[{"id":"env-1","name":"prod","project":"shop"}]}`))
		case procIdentityStatus:
			if bearer != "elevated-identity:read" {
				deny("identity:read")
				return
			}
			_, _ = w.Write([]byte(`{"hasInstance":true,"state":"IDENTITY_STATE_READY"}`))
		case procIdentityLockUser:
			if bearer != "elevated-identity:manage" {
				deny("identity:manage")
				return
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected %s", proc)
		}
	}))
	defer srv.Close()

	previous := resolveIdentityCloudTarget
	resolveIdentityCloudTarget = func(_ context.Context, env, _ string) (cloud.Endpoint, cloud.Credential, error) {
		ep, err := cloud.ResolveEndpoint(env, &cloud.Declaration{Endpoint: srv.URL})
		return ep, cloud.Credential{Token: "reliant-session", Source: cloud.SourceHelper, From: "Reliant session"}, err
	}
	t.Cleanup(func() { resolveIdentityCloudTarget = previous })
	withProjectName(t, "shop")

	if _, err := executeIdentity("status", "prod"); err != nil {
		t.Fatalf("status: %v", err)
	}
	if _, err := executeIdentity("lock", "prod", "--user", "u1"); err != nil {
		t.Fatalf("lock: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"identity:read", "identity:manage"}; !reflect.DeepEqual(exchanges, want) {
		t.Errorf("exchanged for %v, want %v: a read must not ask for manage, nor a change for more than manage", exchanges, want)
	}
	if got := bearers[procIdentityLockUser]; !reflect.DeepEqual(got, []string{"reliant-session", "elevated-identity:manage"}) {
		t.Errorf("lock presented %v: the session first, then the elevated token once", got)
	}
}

// An explicit --token is used exactly as given: a refusal for a missing scope
// is reported, never papered over by trading the token for another.
func TestCloudIdentityExplicitTokenIsNotElevated(t *testing.T) {
	var exchanged bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proc := strings.TrimPrefix(r.URL.Path, "/")
		w.Header().Set("Content-Type", "application/json")
		switch proc {
		case "controlplane.v1.AccessTokenService/ExchangeToken":
			exchanged = true
		case cloud.ProcGetOrganization:
			fmt.Fprintf(w, `{"%s":{"orgId":"org-1"}}`, orgResponseKey)
		case "controlplane.v1.DeployService/ListEnvironments":
			_, _ = w.Write([]byte(`{"environments":[{"id":"env-1","name":"prod","project":"shop"}]}`))
		default:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":"permission_denied","message":"this machine credential does not carry the identity:manage scope"}`))
		}
	}))
	defer srv.Close()
	identityTestTarget(t, srv.URL)
	t.Setenv("IDENTITY_TEST_TOKEN", "ci-token")
	_, err := executeIdentity("lock", "prod", "--user", "u1")
	if err == nil || !strings.Contains(err.Error(), "identity:manage") {
		t.Fatalf("err = %v, want the missing-scope refusal", err)
	}
	if exchanged {
		t.Error("an explicit token was traded for another credential")
	}
}
