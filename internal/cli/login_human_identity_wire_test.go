package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/credentials"
)

// identityCP is a control plane on the wire, modelled on control-plane's
// cliauth (ParseAuthorizeRequest, the token reply) and its hosted-project
// resolution (hostedProjects.ResolveProject): what `forge login` and the first
// deploy actually talk to since control-plane #1431.
type identityCP struct {
	t   *testing.T
	srv *httptest.Server
	// refuseKind makes /oauth/authorize refuse credential_kind the way a
	// server that does not accept it would: invalid_request to the loopback.
	refuseKind bool
	// orgs are the signed-in person's memberships; projects maps
	// "<org>/<name>" to a hosted project id.
	orgs     []string
	projects map[string]string

	mu         sync.Mutex
	authorizes []url.Values
	tokens     int
	human      bool
	challenge  string
	effects    []string // Forge-Project-ID of each non-resolve call
}

const identityToken = "rlci_HUMANIDENTITYxxxxxxxx"

func newIdentityCP(t *testing.T, orgs []string, projects map[string]string) *identityCP {
	t.Helper()
	cp := &identityCP{t: t, orgs: orgs, projects: projects}
	cp.srv = httptest.NewServer(http.HandlerFunc(cp.serve))
	t.Cleanup(cp.srv.Close)
	return cp
}

func (cp *identityCP) serve(w http.ResponseWriter, r *http.Request) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	switch r.URL.Path {
	case "/oauth/authorize":
		q := r.URL.Query()
		cp.authorizes = append(cp.authorizes, q)
		back, _ := url.Parse(q.Get("redirect_uri"))
		reply := url.Values{"state": {q.Get("state")}}
		switch kind := q.Get("credential_kind"); {
		case cp.refuseKind && kind != "":
			reply.Set("error", "invalid_request")
			reply.Set("error_description", `credential_kind must be "human" or absent`)
		case kind == "" || kind == "human":
			cp.human = kind == "human"
			cp.challenge = q.Get("code_challenge")
			reply.Set("code", "c1")
		default:
			reply.Set("error", "invalid_request")
			reply.Set("error_description", `credential_kind must be "human" or absent`)
		}
		back.RawQuery = reply.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound)
	case "/oauth/token":
		cp.tokens++
		_ = r.ParseForm()
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != cp.challenge {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if !cp.human {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "rlat_ORGBOUNDxxxxxxxx", "token_type": "Bearer", "scope": "deploy:write"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": identityToken, "token_type": "Bearer", "credential_kind": "human", "expires_in": 7776000,
		})
	case "/controlplane.v1.DeployService/ResolveHostedProject":
		if r.Header.Get("Authorization") != "Bearer "+identityToken {
			cp.connectErr(w, 401, "unauthenticated", "missing Authorization header")
			return
		}
		var req struct {
			ProjectID string `json:"projectId"`
			Name      string `json:"name"`
			OrgID     string `json:"orgId"`
			Create    bool   `json:"create"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		cp.resolve(w, req.ProjectID, req.Name, req.OrgID, req.Create)
	default:
		if r.Header.Get("Authorization") != "Bearer "+identityToken {
			cp.connectErr(w, 401, "unauthenticated", "missing Authorization header")
			return
		}
		cp.effects = append(cp.effects, r.Header.Get("Forge-Project-ID"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}
}

// resolve follows hostedProjects.ResolveProject for a human caller: an id is
// looked up; a name alone resolves an existing unique project across the
// person's orgs; creating one needs exactly one membership or an explicit org.
func (cp *identityCP) resolve(w http.ResponseWriter, id, name, org string, create bool) {
	reply := func(org, name, id string) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"projectId": id, "orgId": org, "name": name})
	}
	member := func(org string) bool {
		for _, o := range cp.orgs {
			if o == org {
				return true
			}
		}
		return false
	}
	if id != "" {
		for key, pid := range cp.projects {
			if pid == id {
				o, n, _ := strings.Cut(key, "/")
				if !member(o) {
					cp.connectErr(w, 403, "permission_denied", "project not available to this caller")
					return
				}
				reply(o, n, pid)
				return
			}
		}
		cp.connectErr(w, 404, "not_found", "project not found")
		return
	}
	if org == "" {
		var matches []string
		for _, o := range cp.orgs {
			if _, ok := cp.projects[o+"/"+name]; ok {
				matches = append(matches, o)
			}
		}
		switch {
		case len(matches) == 1:
			reply(matches[0], name, cp.projects[matches[0]+"/"+name])
			return
		case len(matches) > 1:
			cp.connectErr(w, 400, "failed_precondition", "project name is ambiguous; use its bound project id")
			return
		case !create:
			cp.connectErr(w, 404, "not_found", "project not found")
			return
		case len(cp.orgs) != 1:
			cp.connectErr(w, 400, "failed_precondition", "first deployment requires --org <org_id>: select the project's owner explicitly")
			return
		}
		org = cp.orgs[0]
	}
	if !member(org) {
		cp.connectErr(w, 403, "permission_denied", "project not available to this caller")
		return
	}
	if pid, ok := cp.projects[org+"/"+name]; ok {
		reply(org, name, pid)
		return
	}
	if !create {
		cp.connectErr(w, 404, "not_found", "project not found")
		return
	}
	pid := "project-" + org + "-" + name
	cp.projects[org+"/"+name] = pid
	reply(org, name, pid)
}

func (cp *identityCP) connectErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": msg})
}

// humanLoginProject is a forge project directory named name, as the cwd.
func humanLoginProject(t *testing.T, name string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: "+name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	withProjectName(t, name)
	return root
}

// loginAgainst runs `forge login`'s browser path (loginOne) against cp.
func loginAgainst(t *testing.T, cp *identityCP, path string) (string, error) {
	t.Helper()
	withFakeBrowser(t)
	ep, err := cloud.ResolveEndpoint("prod", &cloud.Declaration{Endpoint: cp.srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = loginOne(context.Background(), declaredControlPlane{Endpoint: ep, Envs: []string{"prod"}}, "", false, path, &out)
	return out.String(), err
}

// The whole human identity path: `forge login` asks for credential_kind=human,
// stores the rlci_ identity in the human namespace, and a deploy then targets
// a server-owned project. Every case is a multi-org or first-deploy shape the
// server resolves differently.
func TestHumanLogin_WireIdentityPath(t *testing.T) {
	for _, tc := range []struct {
		name     string
		orgs     []string
		projects map[string]string
		// org is `forge env deploy --org`.
		org       string
		wantErr   string
		wantBound string
	}{
		{
			name: "multi-org user, existing project: resolved by name, no --org",
			orgs: []string{"A", "B"}, projects: map[string]string{"B/shop": "project-B-shop", "A/other": "project-A-other"},
			wantBound: "project-B-shop",
		},
		{
			name: "multi-org user, first deploy without --org: refused, nothing bound",
			orgs: []string{"A", "B"}, projects: map[string]string{},
			wantErr: "first deployment requires --org",
		},
		{
			name: "multi-org user, first deploy with --org: created in that org",
			orgs: []string{"A", "B"}, projects: map[string]string{}, org: "B",
			wantBound: "project-B-shop",
		},
		{
			name: "single-org user, first deploy: created in the only org",
			orgs: []string{"A"}, projects: map[string]string{},
			wantBound: "project-A-shop",
		},
		{
			name: "--org naming an org the person is not in: refused",
			orgs: []string{"A"}, projects: map[string]string{}, org: "Z",
			wantErr: "not available",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := isolatedCredentials(t)
			t.Setenv("FORGE_CREDENTIAL_HELPER", "")
			root := humanLoginProject(t, "shop")
			cp := newIdentityCP(t, tc.orgs, tc.projects)

			out, err := loginAgainst(t, cp, path)
			if err != nil {
				t.Fatalf("login: %v\n%s", err, out)
			}
			if len(cp.authorizes) != 1 || cp.authorizes[0].Get("credential_kind") != "human" || cp.authorizes[0].Get("client_id") != cloud.ClientID {
				t.Fatalf("authorize requests: %v", cp.authorizes)
			}
			stored, err := credentials.Lookup(path, cp.srv.URL, cloud.HumanClientID)
			if err != nil || stored.Kind != cloud.KindHuman || stored.Token != identityToken {
				t.Fatalf("human identity not stored in the human namespace: %+v %v", stored, err)
			}
			if _, err := credentials.Lookup(path, cp.srv.URL, cloud.ClientID); err == nil {
				t.Fatal("a human login also wrote a delegated forge-cli entry")
			}

			decl := &cloud.Declaration{Endpoint: cp.srv.URL}
			ep, err := bindHostedProject(context.Background(), "prod", decl, tc.org)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("bind: err = %v, want %q", err, tc.wantErr)
				}
				if _, statErr := os.Stat(filepath.Join(root, hostedProjectBindingFile)); !os.IsNotExist(statErr) {
					t.Fatalf("a refused first deploy wrote a binding: %v", statErr)
				}
				return
			}
			if err != nil || ep.ProjectID != tc.wantBound {
				t.Fatalf("bind = %+v, %v; want project %s", ep, err, tc.wantBound)
			}
			// The binding persists; the next command resolves it from disk.
			ep, err = projectEndpoint("prod", decl)
			if err != nil || ep.ProjectID != tc.wantBound {
				t.Fatalf("binding not persisted: %+v %v", ep, err)
			}
			// An effect carries the server project identity with the same
			// org-less identity, and asks the server, not a local org, whose
			// project it is.
			cred, err := cloud.ResolveCredential("", ep)
			if err != nil || cred.Kind != cloud.KindHuman {
				t.Fatalf("resolve credential: %+v %v", cred, err)
			}
			if err := cloud.NewClient(ep, cred).Call(context.Background(), "controlplane.v1.DeployService/EnsureEnvironment", map[string]any{}, nil); err != nil {
				t.Fatalf("effect: %v", err)
			}
			if org, err := cloud.ResolveOrganization(context.Background(), cloud.NewClient(ep, cred)); err != nil || !strings.HasPrefix(tc.wantBound, "project-"+org+"-") {
				t.Fatalf("organization from the project target = %q, %v", org, err)
			}
			if len(cp.effects) != 1 || cp.effects[0] != tc.wantBound {
				t.Fatalf("effects carried Forge-Project-ID %v, want [%s]", cp.effects, tc.wantBound)
			}
		})
	}
}

// A control plane that refuses credential_kind fails the login with a clear
// message, and forge never retries without the parameter: no org-bound token
// is requested, minted or stored.
func TestHumanLogin_ServerRefusesCredentialKind_NoFallback(t *testing.T) {
	path := isolatedCredentials(t)
	cp := newIdentityCP(t, []string{"A"}, map[string]string{})
	cp.refuseKind = true

	out, err := loginAgainst(t, cp, path)
	if err == nil {
		t.Fatalf("login succeeded against a server that refused credential_kind:\n%s", out)
	}
	for _, want := range []string{"refused forge's request for a human identity login", "credential_kind must be", "does not fall back"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if len(cp.authorizes) != 1 {
		t.Fatalf("authorize called %d times; a refusal must not be retried without credential_kind", len(cp.authorizes))
	}
	if cp.tokens != 0 {
		t.Fatalf("token endpoint called %d times after a refusal", cp.tokens)
	}
	for _, client := range []string{cloud.HumanClientID, cloud.ClientID} {
		if c, err := credentials.Lookup(path, cp.srv.URL, client); err == nil {
			t.Fatalf("a refused login stored a %s credential: %+v", client, c)
		}
	}
}
