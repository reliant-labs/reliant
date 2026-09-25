// Copyright (c) 2025 Reliant Labs
package commands

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/pkg/credentials"

	"github.com/reliant-labs/reliant/internal/cliauth"
)

// fakeCP plays the control plane's CLI login: RFC 8414 metadata, an
// /oauth/authorize that approves at once (as if the user clicked Approve), and
// an /oauth/token that checks the PKCE verifier and mints a token. It records
// every authorize request.
type fakeCP struct {
	srv       *httptest.Server
	mu        sync.Mutex
	requests  []url.Values
	challenge string
	token     string
}

func newFakeCP(t *testing.T, token string) *fakeCP {
	t.Helper()
	cp := &fakeCP{token: token}
	cp.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer":                 cp.srv.URL,
				"authorization_endpoint": cp.srv.URL + "/oauth/authorize",
				"token_endpoint":         cp.srv.URL + "/oauth/token",
			})
		case "/oauth/authorize":
			q := r.URL.Query()
			cp.mu.Lock()
			cp.requests = append(cp.requests, q)
			cp.challenge = q.Get("code_challenge")
			cp.mu.Unlock()
			back, _ := url.Parse(q.Get("redirect_uri"))
			back.RawQuery = url.Values{"code": {"c"}, "state": {q.Get("state")}}.Encode()
			http.Redirect(w, r, back.String(), http.StatusFound)
		case "/oauth/token":
			_ = r.ParseForm()
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			cp.mu.Lock()
			ok := base64.RawURLEncoding.EncodeToString(sum[:]) == cp.challenge && r.PostForm.Get("client_id") == cliauth.ClientID
			scope := cp.requests[len(cp.requests)-1].Get("scope")
			cp.mu.Unlock()
			if !ok {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": cp.token, "token_type": "Bearer", "expires_in": 7776000, "scope": scope,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(cp.srv.Close)
	return cp
}

func (cp *fakeCP) lastRequest() url.Values {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	return cp.requests[len(cp.requests)-1]
}

func withFakeLoginBrowser(t *testing.T) {
	t.Helper()
	prev := loginOpener
	loginOpener = func(u string) error {
		go func() {
			if resp, err := http.Get(u); err == nil {
				_ = resp.Body.Close()
			}
		}()
		return nil
	}
	t.Cleanup(func() { loginOpener = prev })
}

func runRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	root := NewRootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// TestAuthLogin_WritesTheSharedFile is the contract of `reliant auth login`:
// PKCE against the control plane found by discovery from --server, as client
// reliant-cli asking reliant:api, stored in forge's shared file under THAT
// server, beside (not over) forge's own entry for it.
func TestAuthLogin_WritesTheSharedFile(t *testing.T) {
	isolateCLI(t)
	withFakeLoginBrowser(t)
	cp := newFakeCP(t, "rlat_RELIANTCLI00000000")

	// forge already logged in to the same origin: it must survive.
	path, _ := cliauth.CredentialsPath()
	if err := credentials.Store(path, cp.srv.URL, "forge-cli", credentials.Credential{Token: "rlat_FORGE"}); err != nil {
		t.Fatal(err)
	}

	out, err := runRoot(t, "auth", "login", "--server", cp.srv.URL)
	if err != nil {
		t.Fatalf("auth login: %v\n%s", err, out)
	}
	req := cp.lastRequest()
	if req.Get("client_id") != cliauth.ClientID || req.Get("scope") != cliauth.ScopeAPI || req.Get("code_challenge_method") != "S256" {
		t.Errorf("authorize request: %v", req)
	}
	got, err := credentials.Lookup(path, cp.srv.URL, cliauth.ClientID)
	if err != nil || got.Token != "rlat_RELIANTCLI00000000" || got.ExpiresAt == nil {
		t.Fatalf("stored credential: %+v %v", got, err)
	}
	if f, _ := credentials.Lookup(path, cp.srv.URL, "forge-cli"); f.Token != "rlat_FORGE" {
		t.Fatal("reliant's login clobbered forge's entry for the same server")
	}

	// A later command against that server uses it; status reports it.
	if out, err := runRoot(t, "auth", "status", "--server", cp.srv.URL); err != nil || !strings.Contains(out, "rlat_RELIANTC") {
		t.Fatalf("status after login: %v\n%s", err, out)
	}
	// logout forgets reliant's entry only.
	if _, err := runRoot(t, "auth", "logout", "--server", cp.srv.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := credentials.Lookup(path, cp.srv.URL, cliauth.ClientID); err == nil {
		t.Fatal("logout must remove reliant's entry")
	}
	if f, _ := credentials.Lookup(path, cp.srv.URL, "forge-cli"); f.Token != "rlat_FORGE" {
		t.Fatal("reliant logout removed forge's entry")
	}
}

// TestAuthTokenCreate_MintsANamedTokenAndDoesNotStoreIt: `auth token create`
// is a consented login under the given name, printed once, never saved over
// the CLI's own login.
func TestAuthTokenCreate_MintsANamedTokenAndDoesNotStoreIt(t *testing.T) {
	isolateCLI(t)
	withFakeLoginBrowser(t)
	cp := newFakeCP(t, "rlat_CITOKEN0000000000")
	loginFor(t, cp.srv.URL, "rlat_MYLOGIN")
	path, _ := cliauth.CredentialsPath()

	out, err := runRoot(t, "auth", "token", "create", "--name", "ci-deploy", "--server", cp.srv.URL)
	if err != nil {
		t.Fatalf("token create: %v\n%s", err, out)
	}
	if !strings.Contains(out, "rlat_CITOKEN0000000000") {
		t.Fatalf("the token must be printed once:\n%s", out)
	}
	if req := cp.lastRequest(); req.Get("device") != "ci-deploy" || req.Get("scope") != cliauth.ScopeAPI {
		t.Errorf("authorize request: %v", req)
	}
	if c, _ := credentials.Lookup(path, cp.srv.URL, cliauth.ClientID); c.Token != "rlat_MYLOGIN" {
		t.Fatalf("token create must not replace the CLI's own login; got %q", c.Token)
	}
}

// TestAuthLogin_ServerWithoutDiscoveryFailsClearly: a self-hosted server with
// no control plane offers no browser login and the CLI says what to do.
func TestAuthLogin_ServerWithoutDiscoveryFailsClearly(t *testing.T) {
	isolateCLI(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	_, err := runRoot(t, "auth", "login", "--server", srv.URL)
	if err == nil || !strings.Contains(err.Error(), "RELIANT_TOKEN") {
		t.Fatalf("want a no-browser-login error pointing at RELIANT_TOKEN; got %v", err)
	}
}

// TestContextCommandIsGone: `reliant context` and --context are deleted, with
// no compatibility shim.
func TestContextCommandIsGone(t *testing.T) {
	isolateCLI(t)
	root := NewRootCmd()
	for _, c := range root.Commands() {
		if c.Name() == "context" {
			t.Fatal("`reliant context` must not exist")
		}
	}
	if root.PersistentFlags().Lookup("context") != nil {
		t.Fatal("--context must not exist")
	}
	if _, err := runRoot(t, "project", "list", "--context", "prod"); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("--context must be an unknown flag; got %v", err)
	}
}
