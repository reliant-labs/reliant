package deploytarget

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"oras.land/oras-go/v2/registry/remote/auth"
)

// fakeRealm is a registry and its token realm on one loopback server (plain
// HTTP, as plainHTTPRegistry answers for 127.x): /v2/ challenges, and
// /auth/token issues a JWT whose access claim grants grants[repo], narrowing
// like the platform's realm does rather than refusing.
type fakeRealm struct {
	srv        *httptest.Server
	host       string
	password   string              // the one valid basic-auth password
	grants     map[string][]string // repository path → actions granted
	opaque     bool                // issue a non-JWT token
	challenge  string              // overrides the WWW-Authenticate header
	gotScopes  []string
	gotService string
}

func newFakeRealm(t *testing.T) *fakeRealm {
	t.Helper()
	f := &fakeRealm{password: "good", grants: map[string][]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	f.host = strings.TrimPrefix(f.srv.URL, "http://")
	return f
}

func (f *fakeRealm) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/v2/":
		c := f.challenge
		if c == "" {
			c = fmt.Sprintf(`Bearer realm="%s/auth/token",service="%s"`, f.srv.URL, f.host)
		}
		w.Header().Set("WWW-Authenticate", c)
		w.WriteHeader(http.StatusUnauthorized)
	case "/auth/token":
		f.gotScopes = r.URL.Query()["scope"]
		f.gotService = r.URL.Query().Get("service")
		if _, pass, ok := r.BasicAuth(); !ok || pass != f.password {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"errors":[{"code":"UNAUTHORIZED","message":"the supplied credential is not valid"}]}`))
			return
		}
		token := "opaque-token"
		if !f.opaque {
			var access []tokenGrant
			for _, s := range f.gotScopes {
				path := strings.TrimSuffix(strings.TrimPrefix(s, "repository:"), ":pull,push")
				if actions := f.grants[path]; len(actions) > 0 {
					access = append(access, tokenGrant{Type: "repository", Name: path, Actions: actions})
				}
			}
			claims, _ := json.Marshal(map[string]any{"access": access})
			token = "eyJhbGciOiJFUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": token, "access_token": token})
	default:
		http.NotFound(w, r)
	}
}

func staticCred(c auth.Credential) auth.CredentialFunc {
	return func(context.Context, string) (auth.Credential, error) { return c, nil }
}

var goodCred = auth.Credential{Username: "forge", Password: "good"}

func (f *fakeRealm) repos() (site, maps string) {
	return f.host + "/org/proj/web/static.v1", f.host + "/org/proj/web-sourcemaps"
}

func TestProbePushAccess_GrantedWhenEveryRepositoryGrantsPush(t *testing.T) {
	t.Parallel()
	f := newFakeRealm(t)
	f.grants["org/proj/web/static.v1"] = []string{"pull", "push"}
	f.grants["org/proj/web-sourcemaps"] = []string{"pull", "push"}
	site, maps := f.repos()
	got := probePushAccess(context.Background(), f.srv.Client(), staticCred(goodCred), f.host,
		[]string{site, maps, "ghcr.io/acme/elsewhere"})
	if got.Verdict != PushAccessGranted {
		t.Fatalf("verdict = %+v, want granted", got)
	}
	want := []string{"repository:org/proj/web/static.v1:pull,push", "repository:org/proj/web-sourcemaps:pull,push"}
	if !slices.Equal(f.gotScopes, want) || f.gotService != f.host {
		t.Errorf("realm asked for scopes %v service %q; want %v service %q (another host's repository is not asked about)",
			f.gotScopes, f.gotService, want, f.host)
	}
}

// A realm narrows rather than refuses: the token is issued, without push on
// the source-map repository. That is the authoritative "no".
func TestProbePushAccess_DeniedNamesTheRepositoryWithoutPush(t *testing.T) {
	t.Parallel()
	f := newFakeRealm(t)
	f.grants["org/proj/web/static.v1"] = []string{"pull", "push"}
	f.grants["org/proj/web-sourcemaps"] = []string{"pull"}
	site, maps := f.repos()
	got := probePushAccess(context.Background(), f.srv.Client(), staticCred(goodCred), f.host, []string{site, maps})
	if got.Verdict != PushAccessDenied || !slices.Equal(got.Missing, []string{maps}) || !strings.Contains(got.Reason, maps) {
		t.Fatalf("got %+v, want denied naming %s", got, maps)
	}
}

func TestProbePushAccess_DeniedWhenTheRealmRefusesTheCredential(t *testing.T) {
	t.Parallel()
	f := newFakeRealm(t)
	site, _ := f.repos()
	got := probePushAccess(context.Background(), f.srv.Client(), staticCred(auth.Credential{Username: "forge", Password: "revoked"}), f.host, []string{site})
	if got.Verdict != PushAccessDenied || !strings.Contains(got.Reason, "the supplied credential is not valid") {
		t.Fatalf("got %+v, want denied carrying the realm's message", got)
	}
	got = probePushAccess(context.Background(), f.srv.Client(), staticCred(auth.EmptyCredential), f.host, []string{site})
	if got.Verdict != PushAccessDenied || !strings.Contains(got.Reason, "no credential") {
		t.Fatalf("no credential: got %+v, want denied", got)
	}
}

// Everything that is not an answer is Unknown, never a refusal.
func TestProbePushAccess_UnknownWhenThereIsNoAuthoritativeAnswer(t *testing.T) {
	t.Parallel()
	t.Run("opaque token", func(t *testing.T) {
		t.Parallel()
		f := newFakeRealm(t)
		f.opaque = true
		site, _ := f.repos()
		if got := probePushAccess(context.Background(), f.srv.Client(), staticCred(goodCred), f.host, []string{site}); got.Verdict != PushAccessUnknown {
			t.Errorf("got %+v, want unknown", got)
		}
	})
	t.Run("not a bearer challenge", func(t *testing.T) {
		t.Parallel()
		f := newFakeRealm(t)
		f.challenge = `Basic realm="registry"`
		site, _ := f.repos()
		if got := probePushAccess(context.Background(), f.srv.Client(), staticCred(goodCred), f.host, []string{site}); got.Verdict != PushAccessUnknown {
			t.Errorf("got %+v, want unknown", got)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		t.Parallel()
		f := newFakeRealm(t)
		site, _ := f.repos()
		f.srv.Close()
		got := probePushAccess(context.Background(), &http.Client{}, staticCred(goodCred), f.host, []string{site})
		if got.Verdict != PushAccessUnknown || !strings.Contains(got.Reason, "could not reach") {
			t.Errorf("got %+v, want unknown: could not reach", got)
		}
	})
}

// The probe presents the credential the pushes present: docker's reading of
// the store, where an EMPTY credHelpers entry pins the host to config.json
// (dockercred). The credsStore here does not exist, so reading it — oras's
// own reading — would fail; docker's reading finds the good login.
func TestProbePushAccess_ReadsTheCredentialTheWayThePushesDo(t *testing.T) {
	f := newFakeRealm(t)
	f.grants["org/proj/web/static.v1"] = []string{"pull", "push"}
	dir := t.TempDir()
	cfg := fmt.Sprintf(`{"auths":{%q:{"auth":%q}},"credsStore":"forge-test-no-such-helper","credHelpers":{%q:""}}`,
		f.host, base64.StdEncoding.EncodeToString([]byte("forge:good")), f.host)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", dir)
	site, _ := f.repos()
	if got := ProbePushAccess(context.Background(), f.host, []string{site}); got.Verdict != PushAccessGranted {
		t.Fatalf("got %+v, want granted with the config.json login", got)
	}
}

// realmTransport serves registry.example.test and evil.example.test
// in-process (https, non-loopback) and records every request, so a test can
// assert where a credential went.
type realmTransport struct {
	realm string // the realm registry.example.test's /v2/ names
	mu    sync.Mutex
	seen  []*http.Request
}

func (rt *realmTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.seen = append(rt.seen, r)
	rt.mu.Unlock()
	w := httptest.NewRecorder()
	switch {
	case r.URL.Host == "registry.example.test" && r.URL.Path == "/v2/":
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service="registry.example.test"`, rt.realm))
		w.WriteHeader(http.StatusUnauthorized)
	case r.URL.Path == "/auth/token":
		claims, _ := json.Marshal(map[string]any{"access": []tokenGrant{{Type: "repository", Name: "org/proj/web/static.v1", Actions: []string{"pull", "push"}}}})
		token := "eyJhbGciOiJFUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
		_ = json.NewEncoder(w).Encode(map[string]string{"token": token})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
	return w.Result(), nil
}

// credentialsSentTo is the hosts that received an Authorization header.
func (rt *realmTransport) credentialsSentTo() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	var hosts []string
	for _, r := range rt.seen {
		if r.Header.Get("Authorization") != "" || r.URL.User != nil {
			hosts = append(hosts, r.URL.Host)
		}
	}
	return hosts
}

// [B1] The realm is the registry's say-so. A realm off the registry's host,
// over plain http to a non-local host, or carrying userinfo, gets no
// credential: the probe reports a note (Unknown) and sends nothing.
func TestProbePushAccess_PresentsTheCredentialOnlyToTheRegistrysOwnRealm(t *testing.T) {
	t.Parallel()
	const site = "registry.example.test/org/proj/web/static.v1"
	for _, tc := range []struct {
		name, realm, why string
	}{
		{"cross-origin", "https://evil.example.test/auth/token", "is not the registry's host"},
		{"same host on another port", "https://registry.example.test:8443/auth/token", "is not the registry's host"},
		{"plain http to a non-local host", "http://registry.example.test/auth/token", "plain http"},
		{"userinfo", "https://forge:x@registry.example.test/auth/token", "userinfo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rt := &realmTransport{realm: tc.realm}
			got := probePushAccess(context.Background(), &http.Client{Transport: rt}, staticCred(goodCred), "registry.example.test", []string{site})
			if got.Verdict != PushAccessUnknown || !strings.Contains(got.Reason, "could not verify push access") || !strings.Contains(got.Reason, tc.why) {
				t.Errorf("got %+v; want a note saying %q", got, tc.why)
			}
			if sent := rt.credentialsSentTo(); len(sent) != 0 {
				t.Errorf("the credential was sent to %v", sent)
			}
		})
	}

	// The same-host https realm — the platform's own shape — is asked.
	rt := &realmTransport{realm: "https://registry.example.test/auth/token"}
	if got := probePushAccess(context.Background(), &http.Client{Transport: rt}, staticCred(goodCred), "registry.example.test", []string{site}); got.Verdict != PushAccessGranted {
		t.Fatalf("same-host https realm: got %+v, want granted", got)
	}
	if sent := rt.credentialsSentTo(); !slices.Equal(sent, []string{"registry.example.test"}) {
		t.Errorf("credential sent to %v, want the registry's own host once", sent)
	}
}

// A real hostile server on another loopback port, as a registry could name
// it: it never sees a request, let alone an Authorization header.
func TestProbePushAccess_CrossOriginRealmServerSeesNoCredential(t *testing.T) {
	t.Parallel()
	var (
		mu          sync.Mutex
		attackerSaw []string
	)
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		attackerSaw = append(attackerSaw, r.Header.Get("Authorization"))
	}))
	t.Cleanup(attacker.Close)
	f := newFakeRealm(t)
	f.challenge = fmt.Sprintf(`Bearer realm="%s/auth/token",service="%s"`, attacker.URL, f.host)
	site, _ := f.repos()
	got := probePushAccess(context.Background(), f.srv.Client(), staticCred(goodCred), f.host, []string{site})
	if got.Verdict != PushAccessUnknown || !strings.Contains(got.Reason, "is not the registry's host") {
		t.Errorf("got %+v, want a note", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(attackerSaw) != 0 {
		t.Errorf("the attacker's realm was called %d time(s) (Authorization %q)", len(attackerSaw), attackerSaw)
	}
}

// The loopback http realm on the registry's own host — dev's shape,
// http://localhost:<port>/auth/token — is the fixture every granted and
// denied case above already uses (newFakeRealm); this pins the rule itself.
func TestCheckRealm(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		realm, host string
		ok          bool
	}{
		{"https://registry.reliantapi.com/auth/token", "registry.reliantapi.com", true},
		{"https://registry.reliantapi.com:443/auth/token", "registry.reliantapi.com", true},
		{"http://localhost:5055/auth/token", "localhost:5055", true},
		{"http://127.0.0.1:5055/auth/token", "127.0.0.1:5055", true},
		{"http://localhost:5056/auth/token", "localhost:5055", false},
		{"http://registry.reliantapi.com/auth/token", "registry.reliantapi.com", false},
		{"https://auth.reliantapi.com/token", "registry.reliantapi.com", false},
		{"https://u:p@registry.reliantapi.com/auth/token", "registry.reliantapi.com", false},
		{"ftp://registry.reliantapi.com/auth/token", "registry.reliantapi.com", false},
		{"/auth/token", "registry.reliantapi.com", false},
	} {
		if err := checkRealm(tc.realm, tc.host); (err == nil) != tc.ok {
			t.Errorf("checkRealm(%q, %q) = %v, want ok=%v", tc.realm, tc.host, err, tc.ok)
		}
	}
}

func TestParseChallenge(t *testing.T) {
	t.Parallel()
	scheme, p := parseChallenge(`Bearer realm="https://r.example/auth/token",service="r.example",scope="repository:a/b:pull,push"`)
	if scheme != "Bearer" || p["realm"] != "https://r.example/auth/token" || p["service"] != "r.example" || p["scope"] != "repository:a/b:pull,push" {
		t.Errorf("scheme %q params %v", scheme, p)
	}
	if _, p := parseChallenge(`Bearer realm=https://r.example/t, service=r.example`); p["realm"] != "https://r.example/t" || p["service"] != "r.example" {
		t.Errorf("unquoted params %v", p)
	}
}
