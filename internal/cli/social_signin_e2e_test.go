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
	"slices"
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

// TestE2ESocialSignInGitHubAndGoogleAgainstZitadel is the gate for social
// sign-in on a forge.ManagedIdentity, against the REAL pinned identity
// provider and the SCAFFOLDED login broker.
//
// It declares both providers the way KCL lowers them — GitHub with its
// client id AND secret as managed-secret names, Google with a literal client
// id and a secret name — converges the host Zitadel the way `forge env up`
// does, and then proves, hop by hop:
//
//   - Zitadel holds both IdPs with the declared client ids and scopes, both
//     linked into the login policy, with Zitadel's own creation and linking
//     off; and the LOGIN_POLICY document names each with its issuer id;
//   - the scaffolded broker's GET /auth/options lists both buttons;
//   - POST /auth/social/start for each answers the provider's real authorize
//     endpoint (github.com, accounts.google.com) with the declared client_id
//     and scope and redirect_uri == <issuer>/idps/callback — the callback
//     `forge env up` prints — and binds the intent to the browser;
//   - `forge cloud identity status --check-social`'s verifier accepts those
//     URLs, and its --app starter reaches them through this broker;
//   - the callback leg as far as no real OAuth app is needed: the issuer's
//     /idps/callback resolves the intent and sends a provider refusal back to
//     the broker's failure URL; the broker's callback refuses an intent not
//     bound to this browser, and a bound intent whose token the issuer does
//     not recognise;
//   - a rotated secret and a widened scope are each pushed exactly once, the
//     new scope reaches the authorize URL, and a converged pass writes nothing.
//
// NOT proven here: a successful round trip through github.com or Google (it
// needs a real OAuth app and a person at the provider), and so the account
// creation / linking that follows a successful intent, which
// pkg/devidp's handler tests cover against a fake issuer.
func TestE2ESocialSignInGitHubAndGoogleAgainstZitadel(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	// Zitadel's Google provider reads Google's OIDC discovery document to
	// build the authorize URL, so starting a Google sign-in needs the network.
	if !reachable("https://accounts.google.com/.well-known/openid-configuration") {
		t.Skip("needs outbound HTTPS to accounts.google.com: the pinned Zitadel's Google provider reads its discovery document to start a sign-in")
	}
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "socialapp", "--mod", "example.com/socialapp", "--service", "widgets")
	projectDir := filepath.Join(dir, "socialapp")
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
	serverBin := filepath.Join(projectDir, "social-server")
	buildCorpusServer(t, projectDir, serverBin)

	// ── The real identity provider, the way `forge env up` starts it. ──
	idpDir := t.TempDir()
	steps, err := templates.ProjectTemplates().Render("idp-steps.yaml.tmpl", struct{ ProjectName string }{"socialapp"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(idpDir, "idp-steps.yaml"), steps, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	pgSpec := hostinfra.Spec{Name: "postgres", Engine: hostinfra.EnginePostgres, Port: freePortE2E(t), Database: "socialapp", User: "postgres", Password: "postgres"}
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

	// ── Both providers, as KCL lowers them: secrets are store NAMES. ──
	const (
		frontend       = "http://localhost:3000"
		githubClientID = "Iv1.forge-e2e-github"
		googleClientID = "forge-e2e.apps.googleusercontent.com"
	)
	githubScopes := []string{"read:user", "user:email"}
	googleScopes := []string{"openid", "profile", "email"}
	store := map[string]string{
		"GITHUB_CLIENT_ID":     githubClientID,
		"GITHUB_CLIENT_SECRET": "dummy-github-secret",
		"GOOGLE_CLIENT_SECRET": "dummy-google-secret",
	}
	shape := &release.ShapeIdentity{
		Name:         "identity",
		Runtime:      release.IdentityRuntimeHost,
		Login:        release.ShapeIdentityLogin{Mode: release.IdentityLoginNative},
		Roles:        []release.ShapeIdentityRole{{Key: "member", MembersFrom: release.IdentityMembersConsole}},
		Applications: []release.ShapeIdentityApplication{{Name: "dev:api", Workload: "api", Kind: release.IdentityAppNativeBroker}},
		Signup:       &release.ShapeIdentitySignup{Mode: release.IdentitySignupInvite},
		Methods:      []string{release.IdentityMethodPassword},
		Social: []release.ShapeIdentitySocial{
			{Type: release.IdentitySocialGitHub, ClientIDSecret: "GITHUB_CLIENT_ID", ClientSecretSecret: "GITHUB_CLIENT_SECRET", Scopes: githubScopes},
			{Type: release.IdentitySocialGoogle, ClientID: googleClientID, ClientSecretSecret: "GOOGLE_CLIENT_SECRET", Scopes: googleScopes},
		},
	}
	idpBase := fmt.Sprintf("http://localhost:%d", idpSpec.Port)
	callbackURL := idpBase + "/idps/callback"
	storePath := filepath.Join(idpDir, "secrets", "dev.yaml")
	in := devidentity.Input{
		Project:  "socialapp",
		Env:      "dev",
		Identity: shape,
		BaseURL:  idpBase,
		PATPath:  idpSpec.PATPath(idpDir),
		// The broker's success and failure URLs hang off the app origin.
		FrontendOrigin: frontend,
		Secret:         func(n string) (string, bool) { v, ok := store[n]; return v, ok },
		Refs: []devidentity.Ref{
			{EnvName: "OIDC_CLIENT_ID", SecretName: "forge-identity-identity", SecretKey: "DEV_API_CLIENT_ID"},
			{EnvName: "OIDC_REDIRECT_URI", SecretName: "forge-identity-identity", SecretKey: "DEV_API_REDIRECT_URI"},
			{EnvName: "IDP_BROKER_TOKEN", SecretName: "forge-identity-identity", SecretKey: "DEV_API_BROKER_TOKEN"},
			{EnvName: "IDP_LOGIN_POLICY", SecretName: "forge-identity-identity", SecretKey: "LOGIN_POLICY"},
		},
		StorePath:   storePath,
		OutputsPath: filepath.Join(idpDir, ".forge", "identity", "identity.json"),
		KeysDir:     filepath.Join(idpDir, ".forge", "identity", "identity.keys"),
	}
	first, err := devidentity.Converge(ctx, in)
	if err != nil {
		t.Fatalf("converge the identity: %v", err)
	}
	if first.SocialCallbackURL != callbackURL {
		t.Errorf("the callback `forge env up` prints = %q, want %q", first.SocialCallbackURL, callbackURL)
	}
	if len(first.Warnings) != 0 {
		t.Errorf("warnings with every provider secret in the store: %v", first.Warnings)
	}

	// ── 1. Zitadel holds both, as declared, linked into the login policy. ──
	pat := strings.TrimSpace(readFileE2E(t, in.PATPath))
	admin := func(method, path string, body any, out any) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequestWithContext(ctx, method, idpBase+path, rd)
		req.Header.Set("Authorization", "Bearer "+pat)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, raw)
		}
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, raw)
		}
	}
	type idpClient struct {
		ClientID string   `json:"clientId"`
		Scopes   []string `json:"scopes"`
	}
	var idps struct {
		Result []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Type   string `json:"type"`
			Config struct {
				Options map[string]any `json:"options"`
				GitHub  *idpClient     `json:"github"`
				Google  *idpClient     `json:"google"`
			} `json:"config"`
		} `json:"result"`
	}
	admin(http.MethodPost, "/admin/v1/idps/templates/_search", map[string]any{}, &idps)
	idpIDs := map[string]string{}
	for _, want := range []struct {
		name, typ, clientID string
		scopes              []string
	}{
		{"forge:github", "PROVIDER_TYPE_GITHUB", githubClientID, githubScopes},
		{"forge:google", "PROVIDER_TYPE_GOOGLE", googleClientID, googleScopes},
	} {
		found := false
		for _, got := range idps.Result {
			if got.Name != want.name {
				continue
			}
			found = true
			idpIDs[want.name] = got.ID
			c := got.Config.GitHub
			if got.Config.Google != nil {
				c = got.Config.Google
			}
			if got.Type != want.typ || c == nil || c.ClientID != want.clientID || !sameScopes(c.Scopes, want.scopes) {
				t.Errorf("%s in Zitadel = type %s client %+v, want %s with client id %s and scopes %v", want.name, got.Type, c, want.typ, want.clientID, want.scopes)
			}
			for _, opt := range []string{"isCreationAllowed", "isLinkingAllowed", "isAutoCreation", "isAutoUpdate"} {
				if v, _ := got.Config.Options[opt].(bool); v {
					t.Errorf("%s: Zitadel option %s is on; the broker alone decides who signs up and links", want.name, opt)
				}
			}
		}
		if !found {
			t.Errorf("Zitadel has no IdP %s; it has %+v", want.name, idps.Result)
		}
	}
	var lp struct {
		Policy struct {
			AllowExternalIdp bool `json:"allowExternalIdp"`
			Idps             []struct {
				IdpID string `json:"idpId"`
			} `json:"idps"`
		} `json:"policy"`
	}
	admin(http.MethodGet, "/admin/v1/policies/login", nil, &lp)
	var linked []string
	for _, l := range lp.Policy.Idps {
		linked = append(linked, l.IdpID)
	}
	if !lp.Policy.AllowExternalIdp || !slices.Contains(linked, idpIDs["forge:github"]) || !slices.Contains(linked, idpIDs["forge:google"]) {
		t.Errorf("login policy: allowExternalIdp=%v linked %v, want both of %v", lp.Policy.AllowExternalIdp, linked, idpIDs)
	}

	// The LOGIN_POLICY document names each with its issuer id.
	vals, err := secrets.ReadSecretFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	var doc identity.LoginPolicyDoc
	if err := json.Unmarshal([]byte(vals["IDP_LOGIN_POLICY"]), &doc); err != nil {
		t.Fatalf("IDP_LOGIN_POLICY = %q: %v", vals["IDP_LOGIN_POLICY"], err)
	}
	gotSocial := map[string]string{}
	for _, s := range doc.Social {
		gotSocial[s.Type] = s.IdPID
	}
	if gotSocial["github"] != idpIDs["forge:github"] || gotSocial["google"] != idpIDs["forge:google"] || len(gotSocial) != 2 {
		t.Errorf("LOGIN_POLICY social = %+v, want github=%s google=%s", doc.Social, idpIDs["forge:github"], idpIDs["forge:google"])
	}
	// No client secret reaches the store a workload reads.
	for k, v := range vals {
		if strings.Contains(v, "dummy-github-secret") || strings.Contains(v, "dummy-google-secret") {
			t.Errorf("store key %s carries a social client secret", k)
		}
	}
	raw, err := os.ReadFile(in.OutputsPath)
	if err != nil {
		t.Fatal(err)
	}
	var out identity.Output
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}

	// ── 2. The scaffolded server, wired to it. ──
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
		"ENVIRONMENT=development",
		"LOG_FORMAT=text",
		"JWT_SECRET="+pubPEM,
		"IDP_BASE="+idpBase,
		"IDP_BROKER_TOKEN="+vals["IDP_BROKER_TOKEN"],
		"OIDC_CLIENT_ID="+vals["OIDC_CLIENT_ID"],
		"OIDC_REDIRECT_URI="+vals["OIDC_REDIRECT_URI"],
		"IDP_LOGIN_URI="+out.Apps["dev:api"].LoginURI,
		"IDP_LOGIN_POLICY="+vals["IDP_LOGIN_POLICY"],
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
	noRedirect := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	call := func(method, path string, body any, cookies []*http.Cookie) (int, []*http.Cookie, map[string]any, string) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequestWithContext(ctx, method, base+path, rd)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for _, c := range cookies {
			req.AddCookie(c)
		}
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		text, _ := io.ReadAll(resp.Body)
		var parsed map[string]any
		_ = json.Unmarshal(text, &parsed)
		return resp.StatusCode, resp.Cookies(), parsed, string(text)
	}

	// ── 3. GET /auth/options lists both buttons. ──
	code, _, opts, text := call(http.MethodGet, "/auth/options", nil, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /auth/options = HTTP %d: %s", code, text)
	}
	buttons := map[string]string{}
	list, _ := opts["social"].([]any)
	for _, b := range list {
		m, _ := b.(map[string]any)
		buttons[fmt.Sprint(m["type"])] = fmt.Sprint(m["label"])
	}
	if buttons["github"] != "GitHub" || buttons["google"] != "Google" || len(buttons) != 2 {
		fail("GET /auth/options social = %s, want GitHub and Google buttons", text)
	}
	if strings.Contains(text, idpIDs["forge:github"]) || strings.Contains(text, idpIDs["forge:google"]) {
		fail("GET /auth/options leaks an issuer IdP id: %s", text)
	}

	// ── 4. Start each: the provider's real authorize URL, bound to this browser. ──
	type started struct {
		authURL  string
		intentID string
		cookies  []*http.Cookie
	}
	start := func(provider string) started {
		t.Helper()
		code, cookies, body, text := call(http.MethodPost, "/auth/social/start", map[string]string{"provider": provider, "returnTo": "/widgets"}, nil)
		redirect, _ := body["redirect"].(string)
		if code != http.StatusOK || redirect == "" {
			fail("POST /auth/social/start %s = HTTP %d: %s", provider, code, text)
			return started{}
		}
		if len(cookies) == 0 {
			fail("POST /auth/social/start %s set no binding cookie", provider)
		}
		u, _ := url.Parse(redirect)
		return started{authURL: redirect, intentID: u.Query().Get("state"), cookies: cookies}
	}
	for _, want := range []struct {
		provider, endpoint, clientID string
		scopes                       []string
	}{
		{"github", "https://github.com/login/oauth/authorize", githubClientID, githubScopes},
		{"google", "https://accounts.google.com/o/oauth2/v2/auth", googleClientID, googleScopes},
	} {
		s := start(want.provider)
		if s.authURL == "" {
			continue
		}
		u, _ := url.Parse(s.authURL)
		q := u.Query()
		if got := u.Scheme + "://" + u.Host + u.Path; got != want.endpoint {
			fail("%s authorize endpoint = %s, want %s (%s)", want.provider, got, want.endpoint, s.authURL)
		}
		if q.Get("client_id") != want.clientID {
			fail("%s client_id = %q, want %q", want.provider, q.Get("client_id"), want.clientID)
		}
		if !sameScopes(strings.Fields(q.Get("scope")), want.scopes) {
			fail("%s scope = %q, want %v", want.provider, q.Get("scope"), want.scopes)
		}
		if q.Get("redirect_uri") != callbackURL {
			fail("%s redirect_uri = %q, want the identity's callback %q", want.provider, q.Get("redirect_uri"), callbackURL)
		}
		if q.Get("response_type") != "code" || s.intentID == "" {
			fail("%s authorize URL: response_type=%q state=%q", want.provider, q.Get("response_type"), s.intentID)
		}
		// `forge cloud identity status --check-social` accepts exactly this,
		if c := verifySocialAuthURL(want.provider, s.authURL, callbackURL); !c.OK {
			fail("--check-social refuses the real %s authorize URL: %v", want.provider, c.Problems)
		}
		// and refuses it against a callback registered for another issuer.
		if c := verifySocialAuthURL(want.provider, s.authURL, "https://other.example/idps/callback"); c.OK {
			fail("--check-social accepts %s against a callback the issuer does not send", want.provider)
		}
	}

	// --check-social --app: the same check, started through this broker.
	statusDoc := identityStatusDocument{HasInstance: true, Issuer: idpBase, SocialCallbackURL: callbackURL,
		Social: []identitySocialStatusWire{{Type: "github", State: "applied"}, {Type: "google", State: "applied"}}}
	checks, err := checkSocial(ctx, statusDoc, brokerSocialStarter(socialCheckHTTPClient(), base))
	if err != nil || socialCheckErr(checks) != nil {
		fail("--check-social --app %s: %v %+v", base, err, checks)
	}

	// An undeclared provider is refused.
	if code, _, _, text := call(http.MethodPost, "/auth/social/start", map[string]string{"provider": "gitlab"}, nil); code != http.StatusNotFound {
		fail("POST /auth/social/start gitlab = HTTP %d, want 404: %s", code, text)
	}

	// ── 5. The callback leg, as far as no real OAuth app is needed. ──
	gh := start("github")
	if gh.intentID != "" {
		// The provider sends the browser back to the ISSUER's callback; a
		// refusal there (the person pressed Cancel) is resolved against the
		// intent and lands on the broker's failure URL.
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, callbackURL+"?state="+url.QueryEscape(gh.intentID)+
			"&error=access_denied&error_description=The+user+has+denied+your+application+access.", nil)
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatalf("GET the issuer's callback: %v", err)
		}
		loc := resp.Header.Get("Location")
		_ = resp.Body.Close()
		// The issuer REPLACES the failure URL's error with the provider's
		// (error=access_denied), so the page's marker is social=failed.
		lu, _ := url.Parse(loc)
		if resp.StatusCode/100 != 3 || lu == nil || !strings.HasPrefix(loc, frontend+"/auth/sign-in?") ||
			lu.Query().Get("social") != "failed" || lu.Query().Get("id") != gh.intentID {
			fail("the issuer's callback with a provider refusal = HTTP %d Location %q, want a redirect to %s/auth/sign-in carrying social=failed and the intent id", resp.StatusCode, loc, frontend)
		}
		// The broker's callback: an intent not bound to this browser is
		// refused before the issuer is asked,
		if code, _, body, text := call(http.MethodPost, "/auth/social/callback", map[string]string{"id": gh.intentID, "token": "forged"}, nil); code != http.StatusUnauthorized || body["code"] != "social_failed" {
			fail("social callback with no binding cookie = HTTP %d, want 401 social_failed: %s", code, text)
		}
		// and a bound one is refused by the issuer when its token is not
		// the one the issuer appended to the success URL.
		if code, _, body, text := call(http.MethodPost, "/auth/social/callback", map[string]string{"id": gh.intentID, "token": "forged"}, gh.cookies); code != http.StatusUnauthorized || body["code"] != "social_failed" {
			fail("social callback with a forged token = HTTP %d, want 401 social_failed: %s", code, text)
		}
	}
	// The binding is single-use per intent: another start's cookie does not
	// carry this intent.
	gg := start("google")
	if gg.intentID != "" && gh.intentID != "" {
		if code, _, _, text := call(http.MethodPost, "/auth/social/callback", map[string]string{"id": gh.intentID, "token": "forged"}, gg.cookies); code != http.StatusUnauthorized {
			fail("social callback for an intent bound to another sign-in = HTTP %d, want 401: %s", code, text)
		}
	}

	// ── 6. A converged pass writes nothing; a rotation is pushed once. ──
	if again, err := devidentity.Converge(ctx, in); err != nil || again.Report.Changed() {
		t.Fatalf("second converge: %v, changes %v", err, again.Report.Changes)
	}
	store["GOOGLE_CLIENT_SECRET"] = "rotated-google-secret"
	shape.Social[0].Scopes = append(slices.Clone(githubScopes), "read:org")
	rotated, err := devidentity.Converge(ctx, in)
	if err != nil {
		t.Fatalf("converge the rotation: %v", err)
	}
	var changes []string
	for _, c := range rotated.Report.Changes {
		changes = append(changes, c.String())
	}
	joined := strings.Join(changes, "\n")
	if !strings.Contains(joined, `"forge:github" (settings)`) || !strings.Contains(joined, `"forge:google" (client secret)`) || len(changes) != 2 {
		t.Errorf("rotation changes = %v, want forge:github settings and forge:google client secret, nothing else", changes)
	}
	if s := start("github"); s.authURL != "" {
		u, _ := url.Parse(s.authURL)
		if !sameScopes(strings.Fields(u.Query().Get("scope")), shape.Social[0].Scopes) {
			fail("after widening, github scope = %q, want %v", u.Query().Get("scope"), shape.Social[0].Scopes)
		}
	}
	if again, err := devidentity.Converge(ctx, in); err != nil || again.Report.Changed() {
		t.Errorf("converge after the rotation: %v, changes %v", err, again.Report.Changes)
	}
}

func sameScopes(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// reachable reports whether a GET of u answers at all within 15s.
func reachable(u string) bool {
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Get(u)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}
