package cloud

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/credentials"
)

func endpointFor(t *testing.T, env, url, tokenEnv string) Endpoint {
	t.Helper()
	ep, err := ResolveEndpoint(env, &Declaration{Endpoint: url, TokenEnv: tokenEnv})
	if err != nil {
		t.Fatal(err)
	}
	return ep
}

func storeFor(t *testing.T, url, token string) {
	t.Helper()
	path, _ := CredentialsPath()
	if err := credentials.Store(path, url, ClientID, credentials.Credential{Token: token, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

// TestResolveCredential_PrecedenceFlagBeatsEnvBeatsFile pins the
// documented order: flag > env var > credentials file entry.
//
// All three credentials are present SIMULTANEOUSLY in each case, which
// is the point — a test that supplies only one proves nothing about
// precedence, only that each source works in isolation.
func TestResolveCredential_PrecedenceFlagBeatsEnvBeatsFile(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	ep := endpointFor(t, "prod", "https://cp.example.com", "ACME_DEPLOY_TOKEN")
	storeFor(t, ep.URL, "from-file")
	t.Setenv("ACME_DEPLOY_TOKEN", "from-env")

	got, err := ResolveCredential("from-flag", ep)
	if err != nil || got.Token != "from-flag" || got.Source != SourceFlag {
		t.Fatalf("flag must win: %+v %v", got, err)
	}
	got, err = ResolveCredential("", ep)
	if err != nil || got.Token != "from-env" || got.Source != SourceEnv || got.From != "ACME_DEPLOY_TOKEN" {
		t.Fatalf("env must beat the file: %+v %v", got, err)
	}
	t.Setenv("ACME_DEPLOY_TOKEN", "")
	got, err = ResolveCredential("", ep)
	if err != nil || got.Token != "from-file" || got.Source != SourceLogin {
		t.Fatalf("the file is the fallback: %+v %v", got, err)
	}
	if path, _ := CredentialsPath(); got.From != path {
		t.Errorf("From must name the file; got %q", got.From)
	}
}

// TestResolveCredential_FileIsKeyedByEndpoint: a login to staging is never
// presented to prod. Two endpoints' entries coexist, each command reads its
// own, and an endpoint with no entry is "not logged in", not someone else's
// token.
func TestResolveCredential_FileIsKeyedByEndpoint(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	t.Setenv(DefaultTokenEnv, "")
	staging := endpointFor(t, "staging", "https://staging-cp.example.com", "")
	prod := endpointFor(t, "prod", "https://cp.example.com", "")
	other := endpointFor(t, "dev", "http://127.0.0.1:8090", "")
	storeFor(t, staging.URL, "staging-tok")
	storeFor(t, "https://CP.example.com:443/", "prod-tok") // another spelling of prod

	for ep, want := range map[Endpoint]string{staging: "staging-tok", prod: "prod-tok"} {
		got, err := ResolveCredential("", ep)
		if err != nil || got.Token != want {
			t.Errorf("%s: got %q %v, want %q", ep.URL, got.Token, err, want)
		}
	}
	_, err := ResolveCredential("", other)
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("an endpoint with no entry must be ErrNoCredential; got %v", err)
	}
	// Login takes no env: the hint is the bare `forge login` (it logs into
	// every control plane the project declares), never `forge login dev`.
	if !strings.Contains(err.Error(), "forge login ") || strings.Contains(err.Error(), "forge login dev") || !strings.Contains(err.Error(), other.URL) {
		t.Errorf("the hint must name the endpoint and an env-free `forge login`; got:\n%s", err)
	}
}

// TestResolveCredential_HonorsDeclaredTokenEnv proves the variable read
// is the one the ENVIRONMENT declared, not a hardcoded name.
func TestResolveCredential_HonorsDeclaredTokenEnv(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	t.Setenv("STAGING_TOKEN", "staging-cred")
	t.Setenv("PROD_TOKEN", "prod-cred")

	staging, err := ResolveCredential("", endpointFor(t, "staging", "https://s.example", "STAGING_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	prod, err := ResolveCredential("", endpointFor(t, "prod", "https://p.example", "PROD_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	if staging.Token != "staging-cred" || prod.Token != "prod-cred" {
		t.Errorf("wrong variables read: staging=%q prod=%q", staging.Token, prod.Token)
	}
}

// TestResolveCredential_MissingNamesLoginAndEnvVar is the message
// contract: a user with no credential is told the ways to get one.
func TestResolveCredential_MissingNamesLoginAndEnvVar(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	const tokenEnv = "ACME_DEPLOY_TOKEN"
	t.Setenv(tokenEnv, "")

	_, err := ResolveCredential("", endpointFor(t, "prod", "https://cp.example.com", tokenEnv))
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("want ErrNoCredential; got %v", err)
	}
	if strings.Contains(err.Error(), "forge login prod") {
		t.Errorf("login takes no env; the hint must not say `forge login prod`:\n%s", err)
	}
	for _, want := range []string{"forge login", tokenEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message must name %q; got:\n%s", want, err)
		}
	}
}

// TestResolveCredential_ExpiredEntryAsksForLogin: a stored login past its
// issuer-reported expiry is not sent (it would 401) and the message says
// to log in again.
func TestResolveCredential_ExpiredEntryAsksForLogin(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	t.Setenv(DefaultTokenEnv, "")
	ep := endpointFor(t, "prod", "https://cp.example.com", "")
	past := time.Now().Add(-time.Hour)
	path, _ := CredentialsPath()
	if err := credentials.Store(path, ep.URL, ClientID, credentials.Credential{Token: "old", ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveCredential("", ep)
	if !errors.Is(err, ErrNoCredential) || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("want an expired-login error; got %v", err)
	}
}

// TestResolveCredential_CorruptFileIsAnError: a user who DID log in must
// not be told they did not.
func TestResolveCredential_CorruptFileIsAnError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FORGE_HOME", home)
	t.Setenv(DefaultTokenEnv, "")
	if err := os.WriteFile(filepath.Join(home, credentials.FileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveCredential("", endpointFor(t, "prod", "https://cp.example.com", ""))
	if err == nil || errors.Is(err, ErrNoCredential) {
		t.Fatalf("a corrupt file must be a distinct error; got %v", err)
	}
}

// TestResolveCredential_DefaultsToConventionalEnvVar: an endpoint with no
// declared token_env still resolves FORGE_CONTROL_PLANE_TOKEN.
func TestResolveCredential_DefaultsToConventionalEnvVar(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	t.Setenv(DefaultTokenEnv, "conventional")
	got, err := ResolveCredential("", Endpoint{URL: "https://cp.example.com"})
	if err != nil || got.Token != "conventional" || got.From != DefaultTokenEnv {
		t.Fatalf("got %+v %v", got, err)
	}
}

// TestNoLegacyLoginJSON: login.json is gone, with no dual read. A stale one
// on disk is ignored, never silently used.
func TestNoLegacyLoginJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FORGE_HOME", home)
	t.Setenv(DefaultTokenEnv, "")
	if err := os.WriteFile(filepath.Join(home, "login.json"), []byte(`{"token":"legacy"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveCredential("", endpointFor(t, "prod", "https://cp.example.com", "")); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("login.json must not be read; got %v", err)
	}
}
