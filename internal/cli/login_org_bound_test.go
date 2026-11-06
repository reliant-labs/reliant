package cli

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/credentials"
)

// orgBoundLoginCP is a control plane that predates human identities: its
// /oauth/token answers the browser flow with an org-bound rlat_ token and no
// credential_kind, and it serves GetTenant but not ResolveHostedProject.
func orgBoundLoginCP(t *testing.T, token string) (*httptest.Server, func() []string) {
	t.Helper()
	var (
		mu        sync.Mutex
		challenge string
		calls     []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/oauth/authorize":
			q := r.URL.Query()
			mu.Lock()
			challenge = q.Get("code_challenge")
			mu.Unlock()
			back, _ := url.Parse(q.Get("redirect_uri"))
			back.RawQuery = url.Values{"code": {"c"}, "state": {q.Get("state")}}.Encode()
			http.Redirect(w, r, back.String(), http.StatusFound)
		case "/oauth/token":
			_ = r.ParseForm()
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			mu.Lock()
			ok := base64.RawURLEncoding.EncodeToString(sum[:]) == challenge
			mu.Unlock()
			if !ok {
				w.WriteHeader(400)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": token, "token_type": "Bearer", "expires_in": 7776000,
				"scope": "deploy:read deploy:write", "org_id": "org-A",
			})
		case "/controlplane.v1.DeployService/GetTenant":
			if r.Header.Get("Authorization") != "Bearer "+token {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(cloud.OrganizationReply("org-a"))
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"unimplemented","message":"404 page not found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
}

// TestForgeLogin_OrgBoundControlPlaneTakesTheDelegatedPath pins what a forge
// release does against a control plane that does not yet issue human
// identities: the login succeeds, is stored as a delegated org-login (never a
// human identity), resolves its organization through GetTenant without ever
// calling ResolveHostedProject, and logout forgets it.
func TestForgeLogin_OrgBoundControlPlaneTakesTheDelegatedPath(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	t.Setenv(cloud.DefaultTokenEnv, "")
	t.Setenv("FORGE_CREDENTIAL_HELPER", "")
	withFakeBrowser(t)
	const token = "rlat_ORGBOUNDxxxxxxxx"
	cp, calls := orgBoundLoginCP(t, token)

	if out, err := runLoginCmd(t, "login", "--endpoint", cp.URL); err != nil || !strings.Contains(out, "Logged in") {
		t.Fatalf("login against an org-bound control plane: %v\n%s", err, out)
	}
	path, err := cloud.CredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := credentials.Lookup(path, cp.URL, cloud.HumanClientID); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("an org-bound token must never be stored as a human identity; lookup err=%v", err)
	}
	stored, err := credentials.Lookup(path, cp.URL, cloud.ClientID)
	if err != nil || stored.Token != token || stored.Kind != cloud.KindOrgLogin {
		t.Fatalf("stored delegated entry: %+v %v", stored, err)
	}

	ep := cloud.Endpoint{URL: cp.URL, ProjectName: "launch-notes"}
	cred, err := cloud.ResolveCredential("", ep)
	if err != nil || cred.Kind != cloud.KindOrgLogin {
		t.Fatalf("resolved credential: %+v %v", cred, err)
	}
	org, err := cloud.ResolveOrganization(context.Background(), cloud.NewClient(ep, cred))
	if err != nil || org != "org-a" {
		t.Fatalf("organization via GetTenant: %q %v", org, err)
	}
	for _, c := range calls() {
		if strings.HasSuffix(c, "/ResolveHostedProject") {
			t.Fatalf("org-bound login called ResolveHostedProject, which this control plane does not serve: %v", calls())
		}
	}

	if out, err := runLoginCmd(t, "logout", "--endpoint", cp.URL); err != nil || !strings.Contains(out, "Logged out") {
		t.Fatalf("logout: %v\n%s", err, out)
	}
	if _, err := credentials.Lookup(path, cp.URL, cloud.ClientID); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("logout must forget the org-bound login; lookup err=%v", err)
	}
}

// TestForgeLogout_KeepsAPastedToken: logout forgets only what the browser
// flow stored. A pasted --token is a delegated capability the user manages.
func TestForgeLogout_KeepsAPastedToken(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	const endpoint = "https://cp.example.com"
	if _, err := runLoginCmd(t, "login", "--endpoint", endpoint, "--token", "rlat_CI", "--no-verify"); err != nil {
		t.Fatal(err)
	}
	if out, err := runLoginCmd(t, "logout", "--endpoint", endpoint); err != nil || !strings.Contains(out, "Not logged in") {
		t.Fatalf("logout: %v\n%s", err, out)
	}
	if c, err := lookupStored(t, endpoint); err != nil || c.Token != "rlat_CI" {
		t.Fatalf("pasted token removed: %+v %v", c, err)
	}
}
