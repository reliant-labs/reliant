//go:build e2e

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/devidentity"
	"github.com/reliant-labs/forge/internal/hostinfra"
	"github.com/reliant-labs/forge/internal/secrets"
	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/pkg/identity"
	"github.com/reliant-labs/forge/pkg/oauth2"
	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/release"
)

// TestE2ESignInHandoffAgainstZitadel is the gate for the sign-in handoff
// (devidp.HandoffRoutes) against the REAL identity provider.
//
// The handoff mints the new client's tokens by finalizing a SECOND auth
// request with the issuer session the primary's sign-in finalized. Whether
// one Zitadel session may finalize several auth requests is the issuer's
// behaviour, which the fake in pkg/devidp only assumes, so this boots host
// Postgres and the pinned Zitadel the way `forge env up` does, converges a
// native-broker application, boots the SCAFFOLDED server (its broker, its
// auth_handoffs migration, its validator over the issuer's JWKS) and drives:
//
//   - sign up (a primary browser session), whose cookie authenticates an RPC;
//   - a desktop handoff through oauth2.HandoffLogin on a real loopback
//     listener, the consent made with the primary's cookie;
//   - the new bearer credential authenticates the same RPC;
//   - the one-time code cannot be redeemed twice;
//   - signing the new session out leaves the primary working, and signing the
//     primary out leaves a second handoff session working.
func TestE2ESignInHandoffAgainstZitadel(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "handoffapp", "--mod", "example.com/handoffapp", "--service", "widgets")
	projectDir := filepath.Join(dir, "handoffapp")
	addCorpusForgePkgReplace(t, projectDir)

	protoPath := filepath.Join(projectDir, "proto", "services", "widgets", "v1", "widgets.proto")
	proto := readFileE2E(t, protoPath) + `
// forge:entity
message Widget {
  string id = 1;
  string name = 2;
}
`
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("author widgets proto: %v", err)
	}
	runCmd(t, projectDir, forgeBin, "scaffold")
	if broker := readFileE2E(t, filepath.Join(projectDir, "internal", "app", "login_broker.go")); !strings.Contains(broker, "broker.mountHandoff(mux, cfg.HandoffRedirects)") {
		t.Fatal("the scaffolded broker does not mount the handoff from handoff_redirects")
	}

	serverBin := filepath.Join(projectDir, "handoff-server")
	buildCorpusServer(t, projectDir, serverBin)

	// ── The real identity provider, the way `forge env up` starts it. ──
	idpDir := t.TempDir()
	steps, err := templates.ProjectTemplates().Render("idp-steps.yaml.tmpl", struct{ ProjectName string }{"handoffapp"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(idpDir, "idp-steps.yaml"), steps, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	pgSpec := hostinfra.Spec{Name: "postgres", Engine: hostinfra.EnginePostgres, Port: freePortE2E(t), Database: "handoffapp", User: "postgres", Password: "postgres"}
	idpSpec := hostinfra.Spec{
		Name: "idp", Engine: hostinfra.EngineZitadel, Port: freePortE2E(t),
		IDPDatabase: "zitadel", IDPDatabasePort: pgSpec.Port, IDPMasterKey: "MasterkeyNeedsToHave32Characters",
		IDPStepsFile: "idp-steps.yaml", IDPPATPath: ".forge/idp/pat.txt",
		Database: "postgres", User: "postgres", Password: "postgres",
	}
	t.Cleanup(func() {
		_ = hostinfra.Stop(idpDir, idpSpec)
		_ = hostinfra.Stop(idpDir, pgSpec)
	})
	if err := hostinfra.Start(ctx, idpDir, pgSpec); err != nil {
		t.Skipf("host postgres is unavailable here: %v", err)
	}
	if err := hostinfra.Start(ctx, idpDir, idpSpec); err != nil {
		t.Skipf("host Zitadel is unavailable here: %v", err)
	}

	const frontend = "http://localhost:3000"
	idpBase := fmt.Sprintf("http://localhost:%d", idpSpec.Port)
	store := filepath.Join(idpDir, "secrets", "dev.yaml")
	in := devidentity.Input{
		Project: "handoffapp",
		Identity: &release.ShapeIdentity{
			Name:         "identity",
			Runtime:      release.IdentityRuntimeHost,
			Login:        release.ShapeIdentityLogin{Mode: release.IdentityLoginNative},
			Roles:        []release.ShapeIdentityRole{{Key: "member", MembersFrom: release.IdentityMembersConsole}},
			Applications: []release.ShapeIdentityApplication{{Name: "dev:api", Workload: "api", Kind: release.IdentityAppNativeBroker}},
		},
		BaseURL:        idpBase,
		PATPath:        idpSpec.PATPath(idpDir),
		FrontendOrigin: frontend,
		Secret:         func(string) (string, bool) { return "", false },
		Refs: []devidentity.Ref{
			{EnvName: "JWT_ISSUER", SecretName: "forge-identity-identity", SecretKey: "ISSUER"},
			{EnvName: "JWT_AUDIENCE", SecretName: "forge-identity-identity", SecretKey: "AUDIENCE"},
			{EnvName: "OIDC_CLIENT_ID", SecretName: "forge-identity-identity", SecretKey: "DEV_API_CLIENT_ID"},
			{EnvName: "OIDC_REDIRECT_URI", SecretName: "forge-identity-identity", SecretKey: "DEV_API_REDIRECT_URI"},
			{EnvName: "IDP_BASE", SecretName: "forge-identity-identity", SecretKey: "ISSUER"},
			{EnvName: "IDP_BROKER_TOKEN", SecretName: "forge-identity-identity", SecretKey: "DEV_API_BROKER_TOKEN"},
		},
		StorePath:   store,
		OutputsPath: filepath.Join(idpDir, ".forge", "identity", "identity.json"),
		KeysDir:     filepath.Join(idpDir, ".forge", "identity", "identity.keys"),
	}
	if _, err := devidentity.Converge(ctx, in); err != nil {
		t.Fatalf("converge the identity: %v", err)
	}
	vals, err := secrets.ReadSecretFile(store)
	if err != nil {
		t.Fatal(err)
	}
	var out identity.Output
	raw, err := os.ReadFile(in.OutputsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}

	// ── The scaffolded server, wired to it. ──
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("provision postgres: %v", err)
	}
	defer cleanup()
	applyProjectMigrationsPostgres(t, projectDir, dsn)

	port := freePortE2E(t)
	cmd := exec.Command(serverBin, "server")
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PORT=%d", port),
		"DATABASE_URL="+dsn,
		"ENVIRONMENT=development",
		"LOG_FORMAT=text",
		// The validator checks the issuer's own tokens, as a deployed env does.
		"JWT_ISSUER="+vals["JWT_ISSUER"],
		"JWT_AUDIENCE="+vals["JWT_AUDIENCE"],
		"JWT_JWKS_URL="+idpBase+"/oauth/v2/keys",
		"IDP_BASE="+idpBase,
		"IDP_BROKER_TOKEN="+vals["IDP_BROKER_TOKEN"],
		"OIDC_CLIENT_ID="+vals["OIDC_CLIENT_ID"],
		"OIDC_REDIRECT_URI="+vals["OIDC_REDIRECT_URI"],
		"IDP_LOGIN_URI="+out.Apps["dev:api"].LoginURI,
		"HANDOFF_REDIRECTS=http://127.0.0.1/callback, com.handoffapp.desktop:/cb",
	)
	serverOut := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = serverOut, serverOut
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
		}
	}()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !waitForServing(t, base, 60*time.Second) {
		t.Fatalf("server did not become ready\nserver output:\n%s", serverOut.String())
	}

	// call sends body as JSON with an optional session cookie or bearer.
	call := func(method, path string, body any, cookie, bearer string) (int, http.Header, map[string]any, string) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, base+path, rd)
		if err != nil {
			t.Fatal(err)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if cookie != "" {
			req.Header.Set("Cookie", "handoffapp_session="+cookie)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		text, _ := io.ReadAll(resp.Body)
		var parsed map[string]any
		_ = json.Unmarshal(text, &parsed)
		return resp.StatusCode, resp.Header, parsed, string(text)
	}
	rpc := func(cookie, bearer string) int {
		t.Helper()
		code, _, _, _ := call(http.MethodPost, "/services.widgets.v1.WidgetsService/ListWidgets", map[string]any{}, cookie, bearer)
		return code
	}
	fatal := func(format string, args ...any) {
		t.Helper()
		t.Fatalf(format+"\nserver output:\n%s", append(args, serverOut.String())...)
	}

	// ── 1. A primary browser session. ──
	code, hdr, _, text := call(http.MethodPost, "/auth/register", map[string]string{"email": "ada@handoffapp.test", "password": "Password1!"}, "", "")
	var primary string
	for _, c := range (&http.Response{Header: hdr}).Cookies() {
		if c.Name == "handoffapp_session" {
			primary = c.Value
		}
	}
	if code != http.StatusOK || !strings.HasPrefix(primary, "fs_") {
		fatal("register: HTTP %d, session cookie %q: %s", code, primary, text)
	}
	if got := rpc(primary, ""); got != http.StatusOK {
		fatal("an RPC with the primary's cookie = HTTP %d, want 200", got)
	}

	// ── 2. A desktop handoff, the consent made with the primary's cookie. ──
	handoff := func() (session, oneTimeCode, redirect string) {
		t.Helper()
		var consentErr error
		got, err := oauth2.HandoffLogin{
			Base:    base,
			Timeout: 30 * time.Second,
			// The system browser on the consent page: read who and where,
			// then approve, then follow the redirect to the loopback.
			OpenURL: func(authorize string) error {
				u, err := url.Parse(authorize)
				if err != nil || u.Path != "/auth/handoff" {
					consentErr = fmt.Errorf("authorize_url %q is not the consent page", authorize)
					return consentErr
				}
				req := u.Query().Get("req")
				code, _, page, text := call(http.MethodGet, "/auth/handoff/authorize?req="+url.QueryEscape(req), nil, primary, "")
				if code != http.StatusOK || page["kind"] != "native" {
					consentErr = fmt.Errorf("authorize: HTTP %d %s", code, text)
					return consentErr
				}
				code, _, approved, text := call(http.MethodPost, "/auth/handoff/authorize", map[string]string{"req": req}, primary, "")
				if code != http.StatusOK {
					consentErr = fmt.Errorf("consent: HTTP %d %s (does the issuer refuse a second finalize of one session?)", code, text)
					return consentErr
				}
				redirect, _ = approved["redirect"].(string)
				ru, _ := url.Parse(redirect)
				oneTimeCode = ru.Query().Get("code")
				resp, err := http.Get(redirect)
				if err != nil {
					consentErr = err
					return err
				}
				_ = resp.Body.Close()
				return nil
			},
		}.Run(ctx)
		if consentErr != nil {
			fatal("%v", consentErr)
		}
		if err != nil {
			fatal("the handoff failed: %v", err)
		}
		return got.Session, oneTimeCode, strings.SplitN(redirect, "?", 2)[0]
	}
	first, firstCode, firstRedirect := handoff()
	if !strings.HasPrefix(first, "fs_") || first == primary {
		fatal("the handoff returned %q", first)
	}
	if got := rpc("", first); got != http.StatusOK {
		fatal("an RPC with the handoff's bearer credential = HTTP %d, want 200", got)
	}
	// The code was spent by the redeem: a replay fails.
	if code, _, _, text := call(http.MethodPost, "/auth/handoff/redeem", map[string]string{"code": firstCode, "code_verifier": strings.Repeat("v", 43), "redirect_uri": firstRedirect}, "", ""); code != http.StatusBadRequest {
		fatal("a replayed code: HTTP %d, want 400: %s", code, text)
	}

	// ── 3. Independent sessions. ──
	if code, _, _, text := call(http.MethodPost, "/auth/logout", nil, "", first); code != http.StatusOK {
		fatal("sign out the handoff session: HTTP %d %s", code, text)
	}
	if got := rpc("", first); got != http.StatusUnauthorized {
		fatal("a signed-out handoff session still authenticates (HTTP %d)", got)
	}
	if got := rpc(primary, ""); got != http.StatusOK {
		fatal("signing the handoff session out ended the primary (HTTP %d)", got)
	}
	second, _, _ := handoff()
	if code, _, _, text := call(http.MethodPost, "/auth/logout", nil, primary, ""); code != http.StatusOK {
		fatal("sign out the primary: HTTP %d %s", code, text)
	}
	if got := rpc(primary, ""); got != http.StatusUnauthorized {
		fatal("a signed-out primary still authenticates (HTTP %d)", got)
	}
	if got := rpc("", second); got != http.StatusOK {
		fatal("signing the primary out ended the handoff session (HTTP %d)", got)
	}
}
