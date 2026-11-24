//go:build e2e

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/devidentity"
	"github.com/reliant-labs/forge/internal/hostinfra"
	"github.com/reliant-labs/forge/internal/secrets"
	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/pkg/identity"
	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/release"
)

// TestE2EAccountSettingsAgainstZitadel is the gate for the login broker's
// account settings (devidp.AccountRoutes), against the REAL identity
// provider at forge's pinned version: what Zitadel answers decides whether
// each verb works — a password checked by user id for re-authentication, an
// address set unverified with a returned code and then verified, a username
// renamed, a password set by the broker. So this boots host Postgres and the
// pinned Zitadel the way `forge env up` does, converges a native-broker
// application, boots the SCAFFOLDED server (server-side sessions) against it,
// and drives the account routes over HTTP with two browsers of one person:
//
//   - the account page answers for the cookie session, never for a bearer;
//   - re-authentication refuses a wrong password and accepts the right one;
//   - an address change mails (in development: logs) a code for the NEW
//     address, and only confirming it verifies it; the old address stops
//     naming the account;
//   - a password change signs the other browser out, keeps this one, and the
//     old password stops working;
//   - sessions are listed with the current one flagged and revoked by handle.
func TestE2EAccountSettingsAgainstZitadel(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "acctapp", "--mod", "example.com/acctapp", "--service", "widgets")
	projectDir := filepath.Join(dir, "acctapp")
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
	serverBin := filepath.Join(projectDir, "acct-server")
	buildCorpusServer(t, projectDir, serverBin)

	// ── The real identity provider, the way `forge env up` starts it. ──
	idpDir := t.TempDir()
	steps, err := templates.ProjectTemplates().Render("idp-steps.yaml.tmpl", struct{ ProjectName string }{"acctapp"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(idpDir, "idp-steps.yaml"), steps, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	pgSpec := hostinfra.Spec{Name: "postgres", Engine: hostinfra.EnginePostgres, Port: freePortE2E(t), Database: "acctapp", User: "postgres", Password: "postgres"}
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
		Project: "acctapp",
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
	loginURI := out.Apps["dev:api"].LoginURI

	// ── The scaffolded server, with its own database (server-side sessions). ──
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("provision postgres: %v", err)
	}
	defer cleanup()
	applyProjectMigrationsPostgres(t, projectDir, dsn)
	pubPEM, _ := mintDevJWT(t)
	port := freePortE2E(t)
	cmd := exec.Command(serverBin, "server")
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PORT=%d", port),
		"DATABASE_URL="+dsn,
		// development: sign-up is on and no mail is sent, so the address
		// change's confirm link is in the server log.
		"ENVIRONMENT=development",
		"LOG_FORMAT=text",
		"JWT_SECRET="+pubPEM,
		"IDP_BASE="+idpBase,
		"IDP_BROKER_TOKEN="+vals["IDP_BROKER_TOKEN"],
		"OIDC_CLIENT_ID="+vals["OIDC_CLIENT_ID"],
		"OIDC_REDIRECT_URI="+vals["OIDC_REDIRECT_URI"],
		"IDP_LOGIN_URI="+loginURI,
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
	fail := func(format string, args ...any) {
		t.Helper()
		t.Errorf(format+"\nserver output:\n%s", append(args, serverOut.String())...)
	}

	// A browser is a cookie jar.
	newBrowser := func() *http.Client {
		jar, _ := cookiejar.New(nil)
		return &http.Client{Jar: jar}
	}
	call := func(c *http.Client, method, path string, body any, headers ...string) (int, map[string]any, string) {
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
		req.Header.Set("Content-Type", "application/json")
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		text, _ := io.ReadAll(resp.Body)
		var parsed map[string]any
		_ = json.Unmarshal(text, &parsed)
		return resp.StatusCode, parsed, string(text)
	}

	// ── Sign up, and sign in from a second browser. ──
	const email, password = "acct.owner@acctapp.test", "Password1!"
	desk := newBrowser()
	if code, _, text := call(desk, http.MethodPost, "/auth/register", map[string]string{"email": email, "password": password}); code != http.StatusOK {
		t.Fatalf("register: HTTP %d: %s\nserver output:\n%s", code, text, serverOut.String())
	}
	laptop := newBrowser()
	if code, _, text := call(laptop, http.MethodPost, "/auth/login", map[string]string{"email": email, "password": password}); code != http.StatusOK {
		t.Fatalf("second sign-in: HTTP %d: %s", code, text)
	}

	// ── 1. The account page: the cookie session's, never a bearer's. ──
	code, acct, text := call(desk, http.MethodGet, "/auth/account", nil)
	if code != http.StatusOK || acct["email"] != email || acct["hasPassword"] != true || acct["reauthRequired"] != false {
		t.Fatalf("GET /auth/account = HTTP %d: %s\nserver output:\n%s", code, text, serverOut.String())
	}
	sessions, _ := acct["sessions"].([]any)
	if len(sessions) != 2 {
		fail("sessions = %v, want both browsers", acct["sessions"])
	}
	var current, other string
	for _, s := range sessions {
		m := s.(map[string]any)
		if m["current"] == true {
			current = fmt.Sprint(m["handle"])
		} else {
			other = fmt.Sprint(m["handle"])
		}
	}
	if current == "" || other == "" {
		t.Fatalf("no current/other session flagged: %v", sessions)
	}
	var sessionID string
	baseURL, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range desk.Jar.Cookies(baseURL) {
		if c.Name == "acctapp_session" {
			sessionID = c.Value
		}
	}
	if code, _, text := call(http.DefaultClient, http.MethodGet, "/auth/account", nil, "Authorization", "Bearer "+sessionID); code != http.StatusUnauthorized {
		fail("the session id as a bearer token: HTTP %d, want 401: %s", code, text)
	}

	// ── 2. Re-authentication against the issuer. ──
	if code, body, text := call(desk, http.MethodPost, "/auth/reauth", map[string]string{"password": "Wrong-pass1!"}); code != http.StatusUnauthorized || body["code"] != "wrong_password" {
		fail("reauth with a wrong password: HTTP %d, want 401 wrong_password: %s", code, text)
	}
	if code, _, text := call(desk, http.MethodPost, "/auth/reauth", map[string]string{"password": password}); code != http.StatusOK {
		fail("reauth with the right password: HTTP %d: %s", code, text)
	}

	// ── 3. Address change: the code is for the NEW address, logged in dev. ──
	const newEmail = "acct.moved@acctapp.test"
	if code, _, text := call(desk, http.MethodPost, "/auth/account/email", map[string]string{"email": newEmail}); code != http.StatusAccepted {
		t.Fatalf("address change: HTTP %d: %s\nserver output:\n%s", code, text, serverOut.String())
	}
	linkRE := regexp.MustCompile(`link="?([^"\s]+/auth/account/email/confirm\?code=([^&"\s]+))`)
	var confirmCode string
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if m := linkRE.FindStringSubmatch(serverOut.String()); m != nil {
			if !strings.HasPrefix(m[1], frontend+"/auth/account/email/confirm?") {
				fail("confirm link = %s, want it rooted at the app's own origin", m[1])
			}
			confirmCode = m[2]
			break
		}
	}
	if confirmCode == "" {
		t.Fatalf("no confirm link in the server log\nserver output:\n%s", serverOut.String())
	}
	if _, acct, _ := call(desk, http.MethodGet, "/auth/account", nil); acct["email"] != newEmail || acct["emailVerified"] != false {
		fail("before confirming: email=%v verified=%v, want %s unverified (the issuer sets it at once)", acct["email"], acct["emailVerified"], newEmail)
	}
	if code, body, text := call(desk, http.MethodPost, "/auth/account/email/confirm", map[string]string{"code": "WRONG1"}); code != http.StatusUnauthorized || body["code"] != "invalid_code" {
		fail("confirm with a wrong code: HTTP %d: %s", code, text)
	}
	if code, _, text := call(desk, http.MethodPost, "/auth/account/email/confirm", map[string]string{"code": confirmCode}); code != http.StatusOK {
		fail("confirm with the right code: HTTP %d: %s", code, text)
	}
	if _, acct, _ := call(desk, http.MethodGet, "/auth/account", nil); acct["email"] != newEmail || acct["emailVerified"] != true {
		fail("after confirming: email=%v verified=%v", acct["email"], acct["emailVerified"])
	}
	if code, _, text := call(newBrowser(), http.MethodPost, "/auth/login", map[string]string{"email": newEmail, "password": password}); code != http.StatusOK {
		fail("sign in with the NEW address: HTTP %d: %s", code, text)
	}
	if code, _, text := call(newBrowser(), http.MethodPost, "/auth/login", map[string]string{"email": email, "password": password}); code != http.StatusUnauthorized {
		fail("sign in with the OLD address: HTTP %d, want 401 (the username was renamed): %s", code, text)
	}

	// ── 4. Password change: other sessions end, this one stays. ──
	const newPassword = "Changed-Passw0rd!"
	if code, body, text := call(desk, http.MethodPost, "/auth/account/password", map[string]string{"newPassword": "weak"}); code != http.StatusBadRequest || body["code"] != "password_policy" {
		fail("a weak new password: HTTP %d, want 400 password_policy: %s", code, text)
	}
	code, body, text := call(desk, http.MethodPost, "/auth/account/password", map[string]string{"currentPassword": password, "newPassword": newPassword})
	if code != http.StatusOK {
		t.Fatalf("password change: HTTP %d: %s\nserver output:\n%s", code, text, serverOut.String())
	}
	if n, _ := body["otherSessionsEnded"].(float64); n < 2 {
		// The laptop and the new-address sign-in above.
		fail("otherSessionsEnded = %v, want at least 2", body["otherSessionsEnded"])
	}
	if code, _, text := call(laptop, http.MethodGet, "/auth/account", nil); code != http.StatusUnauthorized {
		fail("the other browser after the password change: HTTP %d, want 401: %s", code, text)
	}
	if code, _, text := call(desk, http.MethodGet, "/auth/account", nil); code != http.StatusOK {
		fail("this browser after the password change: HTTP %d, want 200: %s", code, text)
	}
	if code, _, text := call(newBrowser(), http.MethodPost, "/auth/login", map[string]string{"email": newEmail, "password": password}); code != http.StatusUnauthorized {
		fail("sign in with the OLD password: HTTP %d, want 401: %s", code, text)
	}
	phone := newBrowser()
	if code, _, text := call(phone, http.MethodPost, "/auth/login", map[string]string{"email": newEmail, "password": newPassword}); code != http.StatusOK {
		fail("sign in with the NEW password: HTTP %d: %s", code, text)
	}

	// ── 5. Sessions: list, refuse the current one, revoke another. ──
	_, list, _ := call(desk, http.MethodGet, "/auth/account/sessions", nil)
	items, _ := list["sessions"].([]any)
	if len(items) != 2 {
		fail("sessions after the change = %v, want this one and the phone", list)
	}
	var phoneHandle string
	for _, s := range items {
		if m := s.(map[string]any); m["current"] != true {
			phoneHandle = fmt.Sprint(m["handle"])
		}
	}
	if code, body, text := call(desk, http.MethodDelete, "/auth/account/sessions/"+current, nil); code != http.StatusConflict || body["code"] != "current_session" {
		fail("revoking the current session: HTTP %d, want 409 current_session: %s", code, text)
	}
	if code, _, text := call(desk, http.MethodDelete, "/auth/account/sessions/"+phoneHandle, nil); code != http.StatusOK {
		fail("revoking the phone: HTTP %d: %s", code, text)
	}
	if code, _, text := call(phone, http.MethodGet, "/auth/account", nil); code != http.StatusUnauthorized {
		fail("the revoked phone: HTTP %d, want 401: %s", code, text)
	}
}
