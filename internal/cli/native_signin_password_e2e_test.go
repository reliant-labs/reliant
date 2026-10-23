//go:build e2e

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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

// lockedBuffer is a bytes.Buffer safe to read while a child process writes it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestE2ENativeSignInPasswordPolicyAndResetAgainstZitadel is the gate for the
// password half of native sign-in, against the REAL identity provider.
//
// The owner signed up in a scaffolded app and was told "That password does not
// meet the requirements." with no word about what they were; and the reset
// links the control plane produces (`<app>/auth/reset?userID=…&code=…`) landed
// on a page the scaffold did not have. A stubbed issuer cannot show either is
// fixed, because both turn on what Zitadel actually answers: the instance's
// policy, which rule it names in a rejection, and whether a one-time code it
// minted really redeems. So this boots host Postgres and the pinned Zitadel the
// way `forge env up` does, converges a native-broker application, boots the
// SCAFFOLDED server against it, and drives the broker over HTTP:
//
//   - GET /auth/password-policy returns the instance's real policy;
//   - a too-short password at sign-up is refused with 400 and the rules it
//     breaks, never the generic sentence;
//   - forgot → reset round-trips with a real code, signs the person in, and the
//     old password stops working and the code stops working;
//   - the forgot endpoint answers an unknown address exactly as it answers a
//     real one, never returns the code, and is rate limited per address and per
//     client IP.
func TestE2ENativeSignInPasswordPolicyAndResetAgainstZitadel(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "pwapp", "--mod", "example.com/pwapp", "--service", "widgets")
	projectDir := filepath.Join(dir, "pwapp")
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

	serverBin := filepath.Join(projectDir, "pw-server")
	buildCorpusServer(t, projectDir, serverBin)

	// ── The real identity provider, the way `forge env up` starts it. ──
	idpDir := t.TempDir()
	steps, err := templates.ProjectTemplates().Render("idp-steps.yaml.tmpl", struct{ ProjectName string }{"pwapp"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(idpDir, "idp-steps.yaml"), steps, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	pgSpec := hostinfra.Spec{Name: "postgres", Engine: hostinfra.EnginePostgres, Port: freePortE2E(t), Database: "pwapp", User: "postgres", Password: "postgres"}
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
		Project: "pwapp",
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

	// ── The scaffolded server, wired to it. ──
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
		// development: a dev stack has no mail server, so forgot-password
		// mints the code and logs the link instead of asking the issuer to
		// send it.
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

	call := func(method, path string, body any) (int, http.Header, map[string]any, string) {
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
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		text, _ := io.ReadAll(resp.Body)
		var parsed map[string]any
		_ = json.Unmarshal(text, &parsed)
		return resp.StatusCode, resp.Header, parsed, string(text)
	}
	unmetOf := func(body map[string]any) []string {
		var got []string
		list, _ := body["unmet"].([]any)
		for _, v := range list {
			got = append(got, fmt.Sprint(v))
		}
		return got
	}

	// ── 1. The instance's real policy, served up front. ──
	code, hdr, policy, text := call(http.MethodGet, "/auth/password-policy", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /auth/password-policy = HTTP %d: %s", code, text)
	}
	if policy["min_length"] != float64(8) || policy["has_uppercase"] != true || policy["has_lowercase"] != true ||
		policy["has_number"] != true || policy["has_symbol"] != true {
		t.Errorf("policy = %s, want Zitadel's default instance policy (8, upper, lower, number, symbol)", text)
	}
	if !strings.Contains(hdr.Get("Cache-Control"), "max-age=") {
		t.Errorf("Cache-Control = %q, want a short public max-age on the policy", hdr.Get("Cache-Control"))
	}

	// ── 2. Sign-up rejections name the rules, never the generic sentence. ──
	const email = "reset.me@pwapp.test"
	for _, tc := range []struct {
		name, password string
		wantUnmet      []string
		wantInMessage  string
	}{
		{"short and lowercase only", "short", []string{"min_length", "uppercase", "number", "symbol"}, "at least 8 characters"},
		{"only too short", "Sh0rt!", []string{"min_length"}, "at least 8 characters"},
		{"no symbol", "NoSymbols1234", []string{"symbol"}, "a symbol"},
	} {
		code, _, body, text := call(http.MethodPost, "/auth/register", map[string]string{"email": "nobody@pwapp.test", "password": tc.password})
		if code != http.StatusBadRequest {
			fail("register %q: HTTP %d, want 400: %s", tc.password, code, text)
			continue
		}
		if strings.Contains(text, "does not meet the requirements") {
			fail("register %q still answers with the generic sentence: %s", tc.password, text)
		}
		if got := unmetOf(body); strings.Join(got, ",") != strings.Join(tc.wantUnmet, ",") {
			fail("register %q (%s): unmet = %v, want %v (body %s)", tc.password, tc.name, got, tc.wantUnmet, text)
		}
		if msg, _ := body["error"].(string); !strings.Contains(msg, tc.wantInMessage) {
			fail("register %q: error = %q, want it to name %q", tc.password, msg, tc.wantInMessage)
		}
		if body["code"] != "password_policy" {
			fail("register %q: code = %v, want password_policy", tc.password, body["code"])
		}
	}

	// A good password creates the account and signs in.
	code, hdr, _, text = call(http.MethodPost, "/auth/register", map[string]string{"email": email, "password": "Password1!"})
	if code != http.StatusOK || !strings.Contains(strings.Join(hdr.Values("Set-Cookie"), ";"), "pwapp_session=") {
		t.Fatalf("register with a valid password: HTTP %d cookie=%v: %s", code, hdr.Values("Set-Cookie"), text)
	}

	// ── 3. Forgot: same answer for a real and an unknown address. ──
	codeKnown, _, _, textKnown := call(http.MethodPost, "/auth/forgot", map[string]string{"email": email})
	codeUnknown, _, _, textUnknown := call(http.MethodPost, "/auth/forgot", map[string]string{"email": "ghost@pwapp.test"})
	if codeKnown != http.StatusAccepted || codeUnknown != http.StatusAccepted {
		t.Fatalf("forgot: known HTTP %d, unknown HTTP %d, want 202 for both", codeKnown, codeUnknown)
	}
	if textKnown != textUnknown {
		t.Errorf("forgot answers differ between a real and an unknown address (an account oracle):\n known:   %s\n unknown: %s", textKnown, textUnknown)
	}

	// The dev stack has no mail, so the link is in the server log, never in the response.
	var link string
	// slog's text handler quotes the value, so the character classes exclude "
	// or the code would carry a trailing quote.
	linkRE := regexp.MustCompile(`link="?([^"\s]+/auth/reset\?userID=([^&"\s]+)&code=([^&"\s]+))`)
	deadline := time.Now().Add(15 * time.Second)
	var userID, resetCode string
	for time.Now().Before(deadline) {
		if m := linkRE.FindStringSubmatch(serverOut.String()); m != nil {
			link, userID, resetCode = m[1], m[2], m[3]
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if link == "" {
		t.Fatalf("no reset link in the server log\nserver output:\n%s", serverOut.String())
	}
	if !strings.HasPrefix(link, frontend+"/auth/reset?") {
		t.Errorf("link = %s, want it rooted at the app's own origin %s", link, frontend)
	}
	if strings.Contains(textKnown, resetCode) {
		t.Errorf("the forgot response leaked the reset code: %s", textKnown)
	}

	// ── 4. Reset. ──
	if code, _, _, text := call(http.MethodPost, "/auth/reset", map[string]string{"userID": userID, "code": "WRONG1", "password": "NewPassw0rd!"}); code != http.StatusUnauthorized {
		fail("reset with a wrong code: HTTP %d, want 401: %s", code, text)
	}
	// Refused before it reaches the issuer: this is the hazard the broker's credential creates.
	if code, _, _, text := call(http.MethodPost, "/auth/reset", map[string]string{"userID": userID, "code": "", "password": "NewPassw0rd!"}); code != http.StatusBadRequest {
		fail("reset with NO code: HTTP %d, want 400: %s", code, text)
	}
	// A weak password with the RIGHT code: specific rules, and the code survives.
	code, _, body, text := call(http.MethodPost, "/auth/reset", map[string]string{"userID": userID, "code": resetCode, "password": "weak"})
	if code != http.StatusBadRequest || body["code"] != "password_policy" || len(unmetOf(body)) == 0 {
		fail("reset with a weak password: HTTP %d, want 400 with unmet rules: %s", code, text)
	}
	code, hdr, _, text = call(http.MethodPost, "/auth/reset", map[string]string{"userID": userID, "code": resetCode, "password": "NewPassw0rd!"})
	if code != http.StatusOK || !strings.Contains(strings.Join(hdr.Values("Set-Cookie"), ";"), "pwapp_session=") {
		fail("reset with the right code: HTTP %d cookie=%v, want 200 and a session (the code must survive the weak-password attempt): %s", code, hdr.Values("Set-Cookie"), text)
	}
	if code, _, _, text := call(http.MethodPost, "/auth/login", map[string]string{"email": email, "password": "NewPassw0rd!"}); code != http.StatusOK {
		fail("sign in with the NEW password: HTTP %d: %s", code, text)
	}
	if code, _, _, text := call(http.MethodPost, "/auth/login", map[string]string{"email": email, "password": "Password1!"}); code != http.StatusUnauthorized {
		fail("sign in with the OLD password: HTTP %d, want 401: %s", code, text)
	}
	if code, _, _, text := call(http.MethodPost, "/auth/reset", map[string]string{"userID": userID, "code": resetCode, "password": "AnotherPassw0rd!"}); code != http.StatusUnauthorized {
		fail("reusing a spent code: HTTP %d, want 401: %s", code, text)
	}

	// ── 5. Limits: per address, then per client IP. ──
	// Used so far from this client: 2 forgot calls. The per-address budget is 3.
	const burst = "burst@pwapp.test"
	for i := 1; i <= 3; i++ {
		if code, _, _, text := call(http.MethodPost, "/auth/forgot", map[string]string{"email": burst}); code != http.StatusAccepted {
			fail("forgot #%d for one address: HTTP %d, want 202: %s", i, code, text)
		}
	}
	if code, _, _, text := call(http.MethodPost, "/auth/forgot", map[string]string{"email": burst}); code != http.StatusTooManyRequests {
		fail("forgot #4 for one address: HTTP %d, want 429: %s", code, text)
	}
	// 6 IP charges so far (the refused call charges the IP too); distinct addresses
	// until the per-IP budget of 10 is spent.
	for i := 0; i < 4; i++ {
		if code, _, _, text := call(http.MethodPost, "/auth/forgot", map[string]string{"email": fmt.Sprintf("sweep%d@pwapp.test", i)}); code != http.StatusAccepted {
			fail("forgot sweep %d: HTTP %d, want 202: %s", i, code, text)
		}
	}
	if code, _, _, text := call(http.MethodPost, "/auth/forgot", map[string]string{"email": "sweep-last@pwapp.test"}); code != http.StatusTooManyRequests {
		fail("forgot beyond the per-IP budget: HTTP %d, want 429: %s", code, text)
	}
}
