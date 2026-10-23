package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/credentials"
)

// loginTestCP is an httptest control plane serving the two OAuth endpoints
// the way control-plane's internal/cliauth does (approval is immediate).
func loginTestCP(t *testing.T, token string) *httptest.Server {
	t.Helper()
	var challenge string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/authorize":
			q := r.URL.Query()
			challenge = q.Get("code_challenge")
			back, _ := url.Parse(q.Get("redirect_uri"))
			back.RawQuery = url.Values{"code": {"c"}, "state": {q.Get("state")}}.Encode()
			http.Redirect(w, r, back.String(), http.StatusFound)
		case "/oauth/token":
			_ = r.ParseForm()
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
				w.WriteHeader(400)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": token, "token_type": "Bearer", "expires_in": 7776000, "scope": "deploy:write",
			})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func lookupStored(t *testing.T, endpoint string) (credentials.Credential, error) {
	t.Helper()
	path, err := cloud.CredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	return credentials.Lookup(path, endpoint, cloud.ClientID)
}

func withFakeBrowser(t *testing.T) {
	t.Helper()
	prev := openLoginBrowser
	openLoginBrowser = func(u string) error {
		go func() {
			if resp, err := http.Get(u); err == nil {
				_ = resp.Body.Close()
			}
		}()
		return nil
	}
	t.Cleanup(func() { openLoginBrowser = prev })
}

func runLoginCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newLoginCmd()
	if args[0] == "logout" {
		root = newLogoutCmd()
	}
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args[1:])
	err := root.Execute()
	return out.String(), err
}

// TestForgeLogin_WritesAKeyedEntry_TwoEndpointsCoexist_LogoutRemovesOne is
// the CLI-level contract of the shared file: each login lands under its own
// endpoint, a second login does not disturb the first, and logout forgets
// exactly one.
func TestForgeLogin_WritesAKeyedEntry_TwoEndpointsCoexist_LogoutRemovesOne(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	withFakeBrowser(t)
	cpA := loginTestCP(t, "rlat_AAAAAAAAAAAAAAAA")
	cpB := loginTestCP(t, "rlat_BBBBBBBBBBBBBBBB")

	out, err := runLoginCmd(t, "login", "--endpoint", cpA.URL+"/")
	if err != nil {
		t.Fatalf("login A: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Logged in to "+cpA.URL) || !strings.Contains(out, "deploy:write") {
		t.Errorf("login output: %s", out)
	}
	if _, err := runLoginCmd(t, "login", "--endpoint", cpB.URL); err != nil {
		t.Fatalf("login B: %v", err)
	}

	a, err := lookupStored(t, cpA.URL)
	if err != nil || a.Token != "rlat_AAAAAAAAAAAAAAAA" || a.ExpiresAt == nil {
		t.Fatalf("entry A: %+v %v", a, err)
	}
	if b, err := lookupStored(t, cpB.URL); err != nil || b.Token != "rlat_BBBBBBBBBBBBBBBB" {
		t.Fatalf("entry B: %+v %v", b, err)
	}

	if out, err := runLoginCmd(t, "logout", "--endpoint", cpA.URL); err != nil || !strings.Contains(out, "Logged out") {
		t.Fatalf("logout A: %v %s", err, out)
	}
	if _, err := lookupStored(t, cpA.URL); err == nil {
		t.Fatal("A must be gone after logout")
	}
	if b, _ := lookupStored(t, cpB.URL); b.Token != "rlat_BBBBBBBBBBBBBBBB" {
		t.Fatal("logout of A must leave B")
	}
}

// TestForgeLogin_TakesNoEnv: login is about WHO you are, never WHERE you
// deploy — an env positional is refused outright (it would read as "log into
// staging", the shape that makes acting on the wrong env easy).
func TestForgeLogin_TakesNoEnv(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	withDeclaredControlPlanes(t, nil)
	for _, args := range [][]string{
		{"login", "prod"},
		{"login", "prod", "--endpoint", "https://x.example"},
		{"logout", "prod"},
	} {
		if _, err := runLoginCmd(t, args...); err == nil {
			t.Fatalf("%v: an env positional must be refused", args)
		}
	}
}

func TestForgeLogin_TokenFlagStoresWithoutBrowser(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	if _, err := runLoginCmd(t, "login", "--endpoint", "https://cp.example.com", "--token", "rlat_CI", "--no-verify"); err != nil {
		t.Fatal(err)
	}
	if c, err := lookupStored(t, "https://cp.example.com"); err != nil || c.Token != "rlat_CI" {
		t.Fatalf("got %+v %v", c, err)
	}
}
