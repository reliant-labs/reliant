package templates

import (
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
)

// The broker's identity-v2 half: the routes a LOGIN_POLICY turns on. Their
// behaviour is pkg/devidp's (signin_routes_test.go runs every route against a
// fake issuer); what this file holds is the scaffold's part of the contract —
// each route mounted only when the policy offers it, the legacy policy kept
// when no document is delivered, and the rendered file compiling and
// mounting exactly that.

func renderBroker(t *testing.T, data map[string]any) string {
	t.Helper()
	out, err := ProjectTemplates().Render("app-login-broker.go.tmpl", data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := format.Source(out); err != nil {
		t.Fatalf("the rendered broker does not format: %v", err)
	}
	return string(out)
}

func brokerData(extra map[string]any) map[string]any {
	d := map[string]any{"Module": "example.com/app", "Name": "app", "MiddlewareDeclaresSessionCookie": true, "SelfSignupField": true}
	for k, v := range extra {
		d[k] = v
	}
	return d
}

func TestLoginBrokerScaffoldMountsEachMethodOnlyWhenOffered(t *testing.T) {
	src := renderBroker(t, brokerData(map[string]any{"LoginPolicyField": true, "AudienceField": true}))
	for _, c := range []string{"PasskeyBeginPath", "MagicRequestPath", "SocialStartPath", "InviteAcceptPath", "VerifyPath", "VerifyResendPath"} {
		if !strings.Contains(src, c+" ") || !strings.Contains(src, "= devidp."+c) {
			t.Errorf("the broker does not declare %s from devidp", c)
		}
	}
	for offer, routes := range map[string][]string{
		`if b.policy.Offers(devidp.MethodPasskey) {`:   {"PasskeyBeginPath", "PasskeyFinishPath", "PasskeyRegisterBeginPath", "PasskeyRegisterFinishPath", "PasskeysPath"},
		`if b.policy.Offers(devidp.MethodMagicLink) {`: {"MagicRequestPath", "MagicConsumePath"},
		`if len(b.policy.Social) > 0 {`:                {"SocialStartPath", "SocialCallbackPath"},
	} {
		at := strings.Index(src, offer)
		if at < 0 {
			t.Fatalf("no %s", offer)
		}
		block := src[at : at+strings.Index(src[at:], "\n\t}")]
		for _, r := range routes {
			if !strings.Contains(block, "mux.Handle("+r+",") {
				t.Errorf("%s is not mounted inside %s", r, offer)
			}
			if strings.Count(src, "mux.Handle("+r+",") != 1 {
				t.Errorf("%s is mounted more than once", r)
			}
		}
	}
	// Nothing v2 is served under the legacy policy.
	if !regexp.MustCompile(`if !b\.policy\.Present \{\s*return\s*\}`).MatchString(src) {
		t.Error("mountSignInMethods does not return early under the legacy policy")
	}
	for _, want := range []string{
		"devidp.ParseLoginPolicy(cfg.IdpLoginPolicy)",
		`logger.Error("idp_login_policy is unusable; sign-up is OFF and every session needs a verified address until it is fixed"`,
		// An unusable policy fails closed: the gate on, sign-up off.
		"RequireVerifiedEmail: policy.RequireVerifiedEmail || policyErr != nil",
		"if policyErr != nil {\n\t\tbroker.selfSignup = false\n\t}",
		"broker.selfSignup = policy.RegisterServed()",
		"b.policy, b.selfSignup = devidp.LegacyLoginPolicy(), false\n\t\tb.flow.RequireVerifiedEmail = true",
		"errors.Is(err, devidp.ErrEmailUnverified)",
		`"code":  "email_unverified"`,
		"ProjectID: cfg.JwtAudience",
		"b.methods.HandleRegister(w, r)",
		"devidp.OptionsFor(b.policy, b.selfSignup)",
		// §5.7: the delivered password rules come before the Settings API.
		"if p, ok := b.policy.PasswordPolicy(); ok {",
		// A policy without passwords refuses them at the endpoint too.
		"if !b.policy.Present || b.policy.Offers(devidp.MethodPassword) {",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the broker lacks %q", want)
		}
	}
	// A policy that cannot be served never falls back to the legacy
	// switches: that would reopen sign-up and drop the verified-email gate.
	for _, never := range []string{"b.flow.RequireVerifiedEmail = false",
		"b.policy, b.selfSignup = devidp.LegacyLoginPolicy(), b.legacySelfSignup"} {
		if strings.Contains(src, never) {
			t.Errorf("the broker fails open: %q", never)
		}
	}
}

func TestLoginBrokerScaffoldWithoutLoginPolicyFieldServesTheLegacyPolicy(t *testing.T) {
	src := renderBroker(t, brokerData(nil))
	if strings.Contains(src, "cfg.IdpLoginPolicy") || strings.Contains(src, "cfg.JwtAudience") {
		t.Error("the broker reads a field its config does not declare")
	}
	if !strings.Contains(src, "policy := devidp.LegacyLoginPolicy()") {
		t.Error("a config without idp_login_policy does not get the legacy policy")
	}
}

// TestLoginBrokerScaffoldCompilesAndMountsPerPolicy builds the rendered broker
// against this checkout of pkg/devidp and runs it: which routes answer for an
// absent, a partial, a full and a malformed LOGIN_POLICY.
func TestLoginBrokerScaffoldCompilesAndMountsPerPolicy(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a module with the go tool; runs in task test")
	}
	wd, err := os.Getwd() // the package directory
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	goVersion := regexp.MustCompile(`(?m)^go (\S+)`).FindStringSubmatch(string(gomod))[1]
	write("go.mod", "module example.com/app\n\ngo "+goVersion+"\n\nrequire github.com/reliant-labs/forge v0.0.0\n\nreplace github.com/reliant-labs/forge => "+root+"\n")
	sum, _ := os.ReadFile(filepath.Join(root, "go.sum"))
	write("go.sum", string(sum))
	write("pkg/config/config.go", `package config

type Config struct {
	Environment, IdpBase, IdpHost, IdpBrokerToken, IdpBrokerTokenFile string
	OidcClientId, OidcRedirectUri, IdpLoginUri, IdpLoginPolicy, JwtAudience string
	AuthSelfSignup bool
	TrustedProxyHops int32
}
`)
	write("pkg/middleware/middleware.go", "package middleware\n\nconst SessionCookieName = \"app_session\"\n\nconst SecureSessionCookieName = \"__Host-\" + SessionCookieName\n")
	write("internal/app/login_broker.go", renderBroker(t, brokerData(map[string]any{"LoginPolicyField": true, "AudienceField": true, "TrustedProxyHopsField": true,
		"MiddlewareDeclaresSecureSessionCookie": true})))
	write("internal/app/login_broker_mount_test.go", `package app

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/app/pkg/config"
)

func mounted(t *testing.T, policy string) (*http.ServeMux, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	RegisterLoginBroker(mux, &config.Config{Environment: "production", IdpBase: "http://127.0.0.1:1",
		IdpBrokerToken: "t", OidcClientId: "c", OidcRedirectUri: "https://app.test/cb",
		IdpLoginUri: "https://app.test/auth/sign-in", IdpLoginPolicy: policy, AuthSelfSignup: true},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/auth/options", nil))
	var opts map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &opts)
	return mux, opts
}

func serves(mux *http.ServeMux, pattern string) bool {
	var method, path string
	for i := range pattern {
		if pattern[i] == ' ' {
			method, path = pattern[:i], pattern[i+1:]
			break
		}
	}
	_, p := mux.Handler(httptest.NewRequest(method, path, nil))
	return p == pattern
}

func TestMountsPerPolicy(t *testing.T) {
	all := []string{PasskeyBeginPath, PasskeyFinishPath, PasskeyRegisterBeginPath, PasskeyRegisterFinishPath,
		PasskeysPath, MagicRequestPath, MagicConsumePath, SocialStartPath, SocialCallbackPath,
		InviteAcceptPath, VerifyPath, VerifyResendPath}
	for name, tc := range map[string]struct {
		policy   string
		want     []string
		register bool
		signup   string
	}{
		"absent: legacy, auth_self_signup decides": {"", nil, true, "open"},
		// Present but unusable fails CLOSED even with auth_self_signup on:
		// no sign-up, nothing v2 mounted.
		"malformed: closed":      {"{", nil, false, "invite"},
		"future version: closed": {`+"`"+`{"version":9,"signup":"open","methods":["passkey"]}`+"`"+`, nil, false, "invite"},
		"unservable (foreign passkey_rp_id): closed": {`+"`"+`{"version":1,"signup":"open","methods":["password","passkey"],"passkey_rp_id":"other.test","mail":"platform","require_verified_email":true}`+"`"+`,
			nil, false, "invite"},
		// No mail and no verified-email gate: the form signs in at once.
		"open, no mail, password": {`+"`"+`{"version":1,"signup":"open","methods":["password"],"mail":"none","require_verified_email":false}`+"`"+`,
			[]string{InviteAcceptPath, VerifyPath, VerifyResendPath}, true, "open"},
		// No mail, passwordless: the form could never complete, so it is not offered.
		"open, no mail, passkey only": {`+"`"+`{"version":1,"signup":"open","methods":["passkey"],"mail":"none","require_verified_email":false}`+"`"+`,
			[]string{PasskeyBeginPath, PasskeyFinishPath, PasskeyRegisterBeginPath, PasskeyRegisterFinishPath, PasskeysPath, InviteAcceptPath, VerifyPath, VerifyResendPath}, false, "open"},
		"invite, passkey": {`+"`"+`{"version":1,"signup":"invite","methods":["password","passkey"],"mail":"platform","require_verified_email":true}`+"`"+`,
			[]string{PasskeyBeginPath, PasskeyFinishPath, PasskeyRegisterBeginPath, PasskeyRegisterFinishPath, PasskeysPath, InviteAcceptPath, VerifyPath, VerifyResendPath}, false, "invite"},
		"open, everything": {`+"`"+`{"version":1,"signup":"open","methods":["password","passkey","magic_link"],"social":[{"type":"google","idp_id":"1","label":"Google"}],"mail":"platform","require_verified_email":true}`+"`"+`,
			all, true, "open"},
	} {
		mux, opts := mounted(t, tc.policy)
		want := map[string]bool{}
		for _, p := range tc.want {
			want[p] = true
		}
		for _, p := range all {
			if serves(mux, p) != want[p] {
				t.Errorf("%s: %s served = %v, want %v", name, p, !want[p], want[p])
			}
		}
		if serves(mux, RegisterPath) != tc.register {
			t.Errorf("%s: register served = %v, want %v", name, !tc.register, tc.register)
		}
		if opts["signup"] != tc.signup || opts["selfSignup"] != tc.register {
			t.Errorf("%s: options %v", name, opts)
		}
	}
}

// A passkey-only policy refuses a password at /auth/login before the issuer
// is asked; the legacy policy still takes one (the issuer here is
// unreachable, so it answers 401, not the policy's 403).
func TestPasswordLoginFollowsThePolicy(t *testing.T) {
	login := func(policy string) (int, string) {
		mux, _ := mounted(t, policy)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/auth/login",
			strings.NewReader(`+"`"+`{"email":"pw@app.test","password":"a-long-password"}`+"`"+`)))
		var body map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body["code"]
	}
	if status, code := login(`+"`"+`{"version":1,"signup":"invite","methods":["passkey"],"mail":"platform","require_verified_email":true}`+"`"+`); status != 403 || code != "method_not_offered" {
		t.Errorf("passkey-only policy: password login answered %d %q, want 403 method_not_offered", status, code)
	}
	if status, code := login(""); status == 403 || code == "method_not_offered" {
		t.Errorf("legacy policy: password login refused as not offered (%d %q)", status, code)
	}
}

// A reset sets a password and signs in with it, so a policy without
// passwords refuses /auth/reset (and /auth/forgot) before the issuer is
// asked: no password-authenticated session through the back door.
func TestPasswordResetFollowsThePolicy(t *testing.T) {
	post := func(policy, path, body string) (int, string, bool) {
		mux, _ := mounted(t, policy)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		var got map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		return rec.Code, got["code"], len(rec.Result().Cookies()) > 0
	}
	reset := `+"`"+`{"userID":"u1","code":"ABCDEF","password":"a-long-password"}`+"`"+`
	forgot := `+"`"+`{"email":"pw@app.test"}`+"`"+`
	for _, policy := range []string{
		`+"`"+`{"version":1,"signup":"invite","methods":["passkey"],"mail":"platform","require_verified_email":true}`+"`"+`,
		`+"`"+`{"version":1,"signup":"invite","methods":["magic_link"],"social":[{"type":"google","idp_id":"1","label":"Google"}],"mail":"platform","require_verified_email":true}`+"`"+`,
	} {
		for path, body := range map[string]string{"/auth/reset": reset, "/auth/forgot": forgot} {
			if status, code, cookie := post(policy, path, body); status != 403 || code != "method_not_offered" || cookie {
				t.Errorf("%s under %s answered %d %q (cookie %v), want 403 method_not_offered and no session", path, policy, status, code, cookie)
			}
		}
	}
	// The legacy policy still serves reset: the unreachable issuer answers
	// 502, not the policy's 403.
	if status, code, _ := post("", "/auth/reset", reset); status == 403 || code == "method_not_offered" {
		t.Errorf("legacy policy: reset refused as not offered (%d %q)", status, code)
	}
}

// A Secure deployment reads only the __Host- session cookie: a plain-named
// one could have been planted by a sibling site under a shared parent domain.
func TestSecureSessionCookieIsHostPrefixed(t *testing.T) {
	mux, _ := mounted(t, "")
	claims := "e30." + "eyJzdWIiOiJ1MSJ9" + ".s" // {} . {"sub":"u1"}
	for name, want := range map[string]bool{"__Host-app_session": true, "app_session": false} {
		req := httptest.NewRequest("GET", "/auth/session", nil)
		req.AddCookie(&http.Cookie{Name: name, Value: claims})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var id map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &id)
		if id["authenticated"] != want {
			t.Errorf("cookie %s: authenticated = %v, want %v", name, id["authenticated"], want)
		}
	}
}

// Every broker route refuses a cross-site POST: a text/plain form from
// another site needs no preflight, and would otherwise create an account.
func TestEveryRouteRefusesCrossSite(t *testing.T) {
	mux, _ := mounted(t, `+"`"+`{"version":1,"signup":"open","methods":["password","passkey","magic_link"],"social":[{"type":"google","idp_id":"1","label":"Google"}],"mail":"platform","require_verified_email":true}`+"`"+`)
	for _, p := range []string{LoginPath, RegisterPath, LogoutPath, ForgotPath, ResetPath, PasskeyBeginPath, MagicRequestPath,
		SocialStartPath, InviteAcceptPath, VerifyPath, VerifyResendPath} {
		req := httptest.NewRequest("POST", p[len("POST "):], strings.NewReader("{}"))
		req.Header.Set("Content-Type", "text/plain")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Origin", "https://evil.test")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 403 {
			t.Errorf("%s answered a cross-site POST with %d, want 403", p, rec.Code)
		}
	}
}
`)
	cmd := exec.Command("go", "test", "-count=1", "./internal/app/")
	cmd.Dir = dir
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off", "GOPROXY=off"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the rendered broker does not build or mount as its policy says:\n%s", out)
	}
}
