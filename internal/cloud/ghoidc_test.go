package cloud

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/credentials"
)

const fakeRequestToken = "actions-request-bearer"

// fakeActionsTokens is the Actions runtime's ID-token endpoint: each request
// gets a new token ("idtok-1", "idtok-2", …), after fail5xx 503s.
type fakeActionsTokens struct {
	mu        sync.Mutex
	fail5xx   int
	status    int // when set, every request answers it
	audiences []string
	auth      []string
	issued    int
}

func (f *fakeActionsTokens) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audiences = append(f.audiences, r.URL.Query().Get("audience"))
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	if f.fail5xx > 0 {
		f.fail5xx--
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	f.issued++
	_ = json.NewEncoder(w).Encode(map[string]any{"count": 1, "value": fmt.Sprintf("idtok-%d", f.issued)})
}

func (f *fakeActionsTokens) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.audiences)
}

// seen is what the endpoint was sent: the audiences and Authorization headers.
func (f *fakeActionsTokens) seen() (audiences, auth []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.audiences...), append([]string(nil), f.auth...)
}

// fakeFederation is control-plane's AccessTokenService/ExchangeFederatedToken,
// with the proto's JSON names spelled literally.
type fakeFederation struct {
	mu sync.Mutex
	// refuse, when set, is the Connect code every exchange answers (with
	// status and message); otherwise it mints "rlat_minted-N" living ttl.
	refuse  string
	status  int
	message string
	ttl     time.Duration
	bodies  []map[string]any
	auth    []string
	minted  int
}

// seen is what the exchange was sent: its bodies and Authorization headers.
func (f *fakeFederation) seen() (bodies []map[string]any, auth []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.bodies...), append([]string(nil), f.auth...)
}

func (f *fakeFederation) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != "/"+procExchangeFederatedToken {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.bodies = append(f.bodies, body)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	if f.refuse != "" || f.status != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		if f.refuse != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": f.refuse, "message": f.message})
		}
		return
	}
	f.minted++
	ttl := f.ttl
	if ttl == 0 {
		ttl = 15 * time.Minute
	}
	// The minted token carries the scopes asked for, as the control plane's
	// does once it admits them.
	scopes := body["scopes"]
	if scopes == nil {
		scopes = []string{"tests:write"}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token":     fmt.Sprintf("rlat_minted-%d", f.minted),
		"expiresAt": time.Now().Add(ttl).UTC().Format(time.RFC3339Nano),
		"scopes":    scopes,
		"orgId":     "org-1",
		"subject":   "github:acme/shop@refs/heads/main run 7/1",
	})
}

// oidcFixture is a ciIdentity in a GitHub Actions job granted id-token:
// write, with both fakes behind it. env is its whole environment.
type oidcFixture struct {
	ci      *ciIdentity
	env     map[string]string
	actions *fakeActionsTokens
	cp      *fakeFederation
	ep      Endpoint
	mask    *bytes.Buffer
}

func newOIDCFixture(t *testing.T) *oidcFixture {
	t.Helper()
	f := &oidcFixture{actions: &fakeActionsTokens{}, cp: &fakeFederation{}, mask: &bytes.Buffer{}}
	actions := httptest.NewServer(f.actions)
	t.Cleanup(actions.Close)
	cp := httptest.NewServer(f.cp)
	t.Cleanup(cp.Close)
	f.env = map[string]string{
		envGitHubActions:       "true",
		envGitHubRepository:    "acme/shop",
		envIDTokenRequestURL:   actions.URL + "/token?api-version=2.0",
		envIDTokenRequestToken: fakeRequestToken,
	}
	f.ci = &ciIdentity{
		getenv:  func(k string) string { return f.env[k] },
		mask:    f.mask,
		now:     time.Now,
		backoff: func(int) time.Duration { return 0 },
	}
	f.ep = Endpoint{URL: cp.URL, TokenEnv: DefaultTokenEnv}
	return f
}

func (f *oidcFixture) resolve(scope accesstoken.Scope) (Credential, error) {
	return f.ci.resolveCredentialFor("", f.ep, scope)
}

// 1. A token CI set still wins: the job's OIDC token is never asked for.
func TestResolveCredentialFor_EnvTokenWins(t *testing.T) {
	t.Parallel()
	f := newOIDCFixture(t)
	f.env[DefaultTokenEnv] = "rlat_from-env"
	got, err := f.resolve(accesstoken.ScopeTestsWrite)
	if err != nil || got.Token != "rlat_from-env" || got.Source != SourceEnv {
		t.Fatalf("the env token must win: %+v %v", got, err)
	}
	if n := f.actions.calls(); n != 0 {
		t.Fatalf("the ID-token endpoint was called %d times", n)
	}
}

// 2. A federatable scope in a job with an ID token is exchanged for, with no
// bearer, and the minted token is masked once, then reused from the cache.
func TestResolveCredentialFor_ExchangesTheJobsIDToken(t *testing.T) {
	t.Parallel()
	f := newOIDCFixture(t)
	got, err := f.resolve(accesstoken.ScopeTestsWrite)
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != "rlat_minted-1" || got.Source != SourceCIIdentity || got.ExpiresAt == nil ||
		!strings.Contains(got.From, "GitHub Actions OIDC") || len(got.Scopes) != 1 || got.Scopes[0] != "tests:write" {
		t.Fatalf("credential: %+v", got)
	}
	_, actionsAuth := f.actions.seen()
	bodies, cpAuth := f.cp.seen()
	if actionsAuth[0] != "Bearer "+fakeRequestToken {
		t.Errorf("ID-token request authorization %q", actionsAuth[0])
	}
	body := bodies[0]
	if body["provider"] != "FEDERATION_PROVIDER_GITHUB_ACTIONS" || body["idToken"] != "idtok-1" ||
		fmt.Sprint(body["scopes"]) != "[tests:write]" {
		t.Errorf("exchange body: %v", body)
	}
	if cpAuth[0] != "" {
		t.Errorf("the exchange carried a bearer: %q", cpAuth[0])
	}

	again, err := f.resolve(accesstoken.ScopeTestsWrite)
	if err != nil || again.Token != got.Token {
		t.Fatalf("a live token must be reused: %+v %v", again, err)
	}
	if n := f.actions.calls(); n != 1 {
		t.Fatalf("a cache hit asked for another ID token (%d calls)", n)
	}
	if f.mask.String() != "::add-mask::rlat_minted-1\n" {
		t.Fatalf("mask output %q, want one add-mask line", f.mask.String())
	}
}

// 2b. sbom:write is federatable (on a protected ref; the control plane judges
// the ref), so it reaches the OIDC path too, asks for sbom:write alone, and is
// cached apart from tests:write: a token minted for one scope never answers
// for the other.
func TestResolveCredentialFor_SBOMWriteExchangesTheJobsIDToken(t *testing.T) {
	t.Parallel()
	f := newOIDCFixture(t)
	got, err := f.resolve(accesstoken.ScopeSBOMWrite)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceCIIdentity || len(got.Scopes) != 1 || got.Scopes[0] != "sbom:write" {
		t.Fatalf("credential: %+v", got)
	}
	bodies, _ := f.cp.seen()
	if len(bodies) != 1 || fmt.Sprint(bodies[0]["scopes"]) != "[sbom:write]" {
		t.Fatalf("exchange bodies: %v", bodies)
	}
	tests, err := f.resolve(accesstoken.ScopeTestsWrite)
	if err != nil {
		t.Fatal(err)
	}
	if tests.Token == got.Token {
		t.Fatalf("tests:write reused the sbom:write token %q", got.Token)
	}
	if n := f.actions.calls(); n != 2 {
		t.Fatalf("ID-token requests = %d, want one per scope", n)
	}
}

// 2c. A token CI set still wins for sbom:write.
func TestResolveCredentialFor_EnvTokenWinsForSBOMWrite(t *testing.T) {
	t.Parallel()
	f := newOIDCFixture(t)
	f.env[DefaultTokenEnv] = "rlat_from-env"
	got, err := f.resolve(accesstoken.ScopeSBOMWrite)
	if err != nil || got.Token != "rlat_from-env" || got.Source != SourceEnv {
		t.Fatalf("the env token must win: %+v %v", got, err)
	}
	if n := f.actions.calls(); n != 0 {
		t.Fatalf("the ID-token endpoint was called %d times", n)
	}
}

// 3. A scope the catalog does not federate never reaches the OIDC path: it
// resolves exactly as ResolveCredential does, here from the login file.
func TestResolveCredentialFor_NonFederatableScopeSkipsOIDC(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	f := newOIDCFixture(t)
	storeFor(t, f.ep.URL, "from-file")
	got, err := f.resolve(accesstoken.ScopeDeployWrite)
	if err != nil || got.Token != "from-file" || got.Source != SourceLogin {
		t.Fatalf("deploy:write must fall through to the file: %+v %v", got, err)
	}
	if n := f.actions.calls(); n != 0 {
		t.Fatalf("deploy:write asked for an ID token (%d calls)", n)
	}
}

// 4 and 5. Every refusal the job cannot get past degrades to ErrNoCredential,
// its first line carrying the server's remedy; an older control plane says it
// does not exchange yet.
func TestResolveCredentialFor_RefusedExchangeIsNoCredential(t *testing.T) {
	t.Parallel()
	const remedy = "install the Reliant GitHub App on acme/shop, then link it in Organization settings → GitHub"
	cases := []struct {
		code    string
		status  int
		message string
		want    string
	}{
		{CodeUnimplemented, http.StatusNotFound, "unimplemented", "does not exchange GitHub Actions OIDC tokens yet"},
		{"", http.StatusNotFound, "", "does not exchange GitHub Actions OIDC tokens yet"},
		{CodeFailedPrecondition, http.StatusBadRequest, remedy, remedy},
		{CodePermissionDenied, http.StatusForbidden, "event pull_request_target is not federated", "pull_request_target"},
		{CodeUnauthenticated, http.StatusUnauthorized, "the ID token's audience is not accepted", "audience"},
		{CodeResourceExhausted, http.StatusTooManyRequests, "too many exchanges for acme/shop", "too many"},
		{CodeUnavailable, http.StatusServiceUnavailable, "GitHub JWKS unreachable", "JWKS"},
	}
	for _, tc := range cases {
		t.Run(tc.code+fmt.Sprint(tc.status), func(t *testing.T) {
			t.Parallel()
			f := newOIDCFixture(t)
			f.cp.refuse, f.cp.status, f.cp.message = tc.code, tc.status, tc.message
			_, err := f.resolve(accesstoken.ScopeTestsWrite)
			if !errors.Is(err, ErrNoCredential) {
				t.Fatalf("want ErrNoCredential, got %v", err)
			}
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Code != tc.code {
				t.Fatalf("the refusal must still unwrap to the server's *Error: %v", err)
			}
			first, _, _ := strings.Cut(err.Error(), "\n")
			if !strings.Contains(first, tc.want) {
				t.Fatalf("first line %q does not carry %q", first, tc.want)
			}
			if why, ok := CIIdentityRefusal(err); !ok || !strings.Contains(why, tc.want) {
				t.Fatalf("CIIdentityRefusal = %q, %v; want it to carry %q", why, ok, tc.want)
			}
		})
	}
}

// An answer that is not a refusal (a defect in the request) stays an error of
// its own, not "no credential".
func TestResolveCredentialFor_InvalidArgumentIsNotNoCredential(t *testing.T) {
	t.Parallel()
	f := newOIDCFixture(t)
	f.cp.refuse, f.cp.status, f.cp.message = CodeInvalidArgument, http.StatusBadRequest, "provider is required"
	_, err := f.resolve(accesstoken.ScopeTestsWrite)
	if err == nil || errors.Is(err, ErrNoCredential) {
		t.Fatalf("want a plain error, got %v", err)
	}
}

// 6. A token with under a minute left is re-exchanged, with a fresh ID token.
func TestResolveCredentialFor_NearExpiryReexchanges(t *testing.T) {
	t.Parallel()
	f := newOIDCFixture(t)
	f.cp.ttl = 30 * time.Second
	first, err := f.resolve(accesstoken.ScopeTestsWrite)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.resolve(accesstoken.ScopeTestsWrite)
	if err != nil {
		t.Fatal(err)
	}
	if first.Token == second.Token || second.Token != "rlat_minted-2" {
		t.Fatalf("a token about to expire was reused: %s then %s", first.Token, second.Token)
	}
	bodies, _ := f.cp.seen()
	if len(bodies) != 2 || bodies[0]["idToken"] != "idtok-1" || bodies[1]["idToken"] != "idtok-2" {
		t.Fatalf("each exchange must carry a fresh ID token: %v", bodies)
	}
	if f.mask.String() != "::add-mask::rlat_minted-1\n::add-mask::rlat_minted-2\n" {
		t.Fatalf("each new token is masked once: %q", f.mask.String())
	}
}

// 7. FORGE_CI_OIDC=off resolves a federatable scope as any other, and says
// nothing about OIDC.
func TestResolveCredentialFor_SwitchedOff(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	f := newOIDCFixture(t)
	f.env[CIOIDCEnv] = "off"
	_, err := f.resolve(accesstoken.ScopeTestsWrite)
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("want ErrNoCredential, got %v", err)
	}
	if strings.Contains(err.Error(), "has no OIDC token") {
		t.Fatalf("a switched-off exchange still hints at OIDC: %v", err)
	}
	if n := f.actions.calls(); n != 0 {
		t.Fatalf("FORGE_CI_OIDC=off still asked for an ID token (%d calls)", n)
	}
}

// A GitHub Actions job without `id-token: write` is told so on the first line,
// and the help names the CI identity for a federatable scope only.
func TestResolveCredentialFor_JobWithoutIDTokenGetsTheHint(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	f := newOIDCFixture(t)
	delete(f.env, envIDTokenRequestURL)
	_, err := f.resolve(accesstoken.ScopeTestsWrite)
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("want ErrNoCredential, got %v", err)
	}
	first, rest, _ := strings.Cut(err.Error(), "\n")
	if !strings.Contains(first, "add `permissions: id-token: write` to this job (fork pull requests never get one)") {
		t.Fatalf("first line %q lacks the id-token hint", first)
	}
	if !strings.Contains(rest, "permissions: id-token: write   in GitHub Actions") {
		t.Fatalf("the help lacks the CI-identity line:\n%s", rest)
	}
	_, err = f.resolve(accesstoken.ScopeDeployWrite)
	if strings.Contains(err.Error(), "id-token") {
		t.Fatalf("deploy:write's help mentions OIDC:\n%v", err)
	}
	if n := f.actions.calls(); n != 0 {
		t.Fatalf("no request URL, yet %d ID-token calls", n)
	}
}

// 8. The audience is the normalized endpoint URL: scheme and host, lowercased,
// no default port, no trailing slash.
func TestResolveCredentialFor_AudienceIsTheNormalizedEndpoint(t *testing.T) {
	t.Parallel()
	f := newOIDCFixture(t)
	u, err := url.Parse(f.ep.URL)
	if err != nil {
		t.Fatal(err)
	}
	f.ep.URL = "http://LOCALHOST:" + u.Port()
	if _, err := f.resolve(accesstoken.ScopeTestsWrite); err != nil {
		t.Fatal(err)
	}
	want, _ := credentials.Normalize(f.ep.URL)
	audiences, _ := f.actions.seen()
	if want != "http://localhost:"+u.Port() || len(audiences) != 1 || audiences[0] != want {
		t.Fatalf("audience %v, want %q", audiences, want)
	}
}

// The ID-token request is retried twice on a 5xx, and not on a 4xx.
func TestFetchGitHubIDToken_Retries(t *testing.T) {
	t.Parallel()
	f := newOIDCFixture(t)
	f.actions.fail5xx = 2
	if _, err := f.resolve(accesstoken.ScopeTestsWrite); err != nil {
		t.Fatalf("two 503s then a token must succeed: %v", err)
	}
	if n := f.actions.calls(); n != 3 {
		t.Fatalf("%d ID-token calls, want 3", n)
	}

	g := newOIDCFixture(t)
	g.actions.status = http.StatusForbidden
	_, err := g.resolve(accesstoken.ScopeTestsWrite)
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("want the 403 named, got %v", err)
	}
	if n := g.actions.calls(); n != 1 {
		t.Fatalf("a 403 was retried: %d calls", n)
	}
}

// 9. Neither token reaches an error message or the credential's printable
// fields; the minted one appears only in its add-mask line.
func TestResolveCredentialFor_TokensNeverInOutput(t *testing.T) {
	t.Parallel()
	f := newOIDCFixture(t)
	got, err := f.resolve(accesstoken.ScopeTestsWrite)
	if err != nil {
		t.Fatal(err)
	}
	printable := strings.Join([]string{got.From, got.Identity(), string(got.Source)}, " ")
	for _, secret := range []string{"idtok-1", fakeRequestToken} {
		if strings.Contains(printable, secret) || strings.Contains(f.mask.String(), secret) {
			t.Fatalf("%q leaked: %q / %q", secret, printable, f.mask.String())
		}
	}
	if strings.Contains(got.From, got.Token) {
		t.Fatalf("From carries the minted token: %q", got.From)
	}

	refused := newOIDCFixture(t)
	refused.cp.refuse, refused.cp.status, refused.cp.message = CodeUnauthenticated, http.StatusUnauthorized, "token replayed"
	_, err = refused.resolve(accesstoken.ScopeTestsWrite)
	failed := newOIDCFixture(t)
	failed.actions.status = http.StatusInternalServerError
	_, ferr := failed.resolve(accesstoken.ScopeTestsWrite)
	for _, e := range []error{err, ferr} {
		if e == nil {
			t.Fatal("want an error")
		}
		for _, secret := range []string{"idtok-1", fakeRequestToken} {
			if strings.Contains(e.Error(), secret) {
				t.Fatalf("%q leaked into %q", secret, e.Error())
			}
		}
	}
	if refused.mask.Len() != 0 || failed.mask.Len() != 0 {
		t.Fatal("a failed exchange printed an add-mask line")
	}
}

// redirectTrap is a server every request to which is a leak: the place a
// redirect points. hits counts them; bodies and auth are what it was handed.
type redirectTrap struct {
	mu     sync.Mutex
	hits   int
	bodies []string
	auth   []string
}

func (r *redirectTrap) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hits++
	b, _ := io.ReadAll(req.Body)
	r.bodies = append(r.bodies, string(b))
	r.auth = append(r.auth, req.Header.Get("Authorization"))
	_ = json.NewEncoder(w).Encode(map[string]any{"value": "idtok-trap", "token": "rlat_trap", "expiresAt": "2999-01-01T00:00:00Z"})
}

// redirector answers every request with a 307 (which net/http would replay,
// body and all) to target's same path.
func redirector(t *testing.T, target string) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		http.Redirect(w, r, target+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	return srv, func() int {
		mu.Lock()
		defer mu.Unlock()
		return hits
	}
}

// A control-plane endpoint that redirects the exchange elsewhere never gets
// the ID token forwarded: the redirect is the answer, and it fails.
func TestExchangeFederated_NeverFollowsARedirect(t *testing.T) {
	t.Parallel()
	trap := &redirectTrap{}
	trapSrv := httptest.NewServer(trap)
	t.Cleanup(trapSrv.Close)
	redir, _ := redirector(t, trapSrv.URL)

	f := newOIDCFixture(t)
	f.ep.URL = redir.URL
	_, err := f.resolve(accesstoken.ScopeTestsWrite)
	if err == nil {
		t.Fatal("a redirected exchange must fail")
	}
	trap.mu.Lock()
	defer trap.mu.Unlock()
	if trap.hits != 0 {
		t.Fatalf("the redirect target was reached %d times, with bodies %q", trap.hits, trap.bodies)
	}
	if strings.Contains(err.Error(), "idtok-1") || f.mask.Len() != 0 {
		t.Fatalf("the ID token leaked: %v / %q", err, f.mask.String())
	}
}

// The ID-token request, whose header carries the runner's bearer, never
// follows a redirect either, and a redirect is not retried.
func TestFetchGitHubIDToken_NeverFollowsARedirect(t *testing.T) {
	t.Parallel()
	trap := &redirectTrap{}
	trapSrv := httptest.NewServer(trap)
	t.Cleanup(trapSrv.Close)
	redir, hits := redirector(t, trapSrv.URL)

	f := newOIDCFixture(t)
	f.env[envIDTokenRequestURL] = redir.URL + "/token?api-version=2.0"
	_, err := f.resolve(accesstoken.ScopeTestsWrite)
	if err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatalf("want the 307 named, got %v", err)
	}
	trap.mu.Lock()
	defer trap.mu.Unlock()
	if trap.hits != 0 {
		t.Fatalf("the redirect target was reached %d times, with Authorization %q", trap.hits, trap.auth)
	}
	if n := hits(); n != 1 {
		t.Fatalf("a redirect was retried: %d requests", n)
	}
	if bodies, _ := f.cp.seen(); len(bodies) != 0 {
		t.Fatalf("an exchange ran without an ID token: %v", bodies)
	}
}
