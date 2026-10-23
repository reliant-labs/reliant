package identity

// The docker smoke: the REAL, unmodified Zitadel image plus Postgres.
//
// The httptest fake proves the library's logic in milliseconds; this proves the
// fake. Every endpoint, payload shape and "no change" error id the library
// relies on is exercised against the pinned release, so a Zitadel upgrade that
// moves one fails here instead of in a reconciler at 3am.
//
// Gated by -short (it pulls and boots two containers). Run it alone, on a
// machine with memory to spare:
//
//	go test -count=1 -run TestSmoke -v ./pkg/identity/

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/devidp"
)

// zitadelImage is the version the managed-identity deployment pins (FI9 / CI0).
const zitadelImage = "ghcr.io/zitadel/zitadel:v4.19.5"

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// smokeEnv is the shared network and Postgres.
type smokeEnv struct {
	prefix, network, pg string
}

func startSmokeEnv(t *testing.T) *smokeEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: boots Zitadel and Postgres containers; runs in task test")
	}
	if exec.Command("docker", "version").Run() != nil {
		t.Skip("docker is not available")
	}
	if exec.Command("docker", "image", "inspect", zitadelImage).Run() != nil {
		if out, err := exec.Command("docker", "pull", zitadelImage).CombinedOutput(); err != nil {
			t.Skipf("cannot pull %s: %v\n%s", zitadelImage, err, out)
		}
	}
	const pgImage = "postgres:16-alpine"
	if exec.Command("docker", "image", "inspect", pgImage).Run() != nil {
		if out, err := exec.Command("docker", "pull", pgImage).CombinedOutput(); err != nil {
			t.Skipf("cannot pull %s: %v\n%s", pgImage, err, out)
		}
	}
	e := &smokeEnv{prefix: fmt.Sprintf("identity-smoke-%d", time.Now().UnixNano())}
	e.network, e.pg = e.prefix+"-net", e.prefix+"-pg"
	docker(t, "network", "create", e.network)
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", e.network).Run() })
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-fv", e.pg).Run() })
	docker(t, "run", "-d", "--name", e.pg, "--network", e.network, "-e", "POSTGRES_PASSWORD=postgres",
		pgImage, "-c", "fsync=off", "-c", "synchronous_commit=off", "-c", "full_page_writes=off")
	// The image's init phase runs a throwaway server with no TCP listener, so
	// only a TCP probe says the real one is up; Zitadel exits at once if it
	// dials before that.
	deadline := time.Now().Add(60 * time.Second)
	for exec.Command("docker", "exec", e.pg, "pg_isready", "-h", "127.0.0.1", "-U", "postgres").Run() != nil {
		if time.Now().After(deadline) {
			t.Fatal("postgres never accepted TCP connections")
		}
		time.Sleep(500 * time.Millisecond)
	}
	return e
}

// smokeInstance is one booted Zitadel.
type smokeInstance struct {
	name    string
	base    string // http://localhost:<port>, which is also the issuer
	patPath string
	pat     string
	org     string
}

const smokeSteps = `FirstInstance:
  InstanceName: smoke
  Org:
    Name: smoke
    Human:
      UserName: admin@smoke.localhost
      Password: 'Password1!'
      FirstName: Dev
      LastName: Admin
      Email:
        Address: admin@smoke.localhost
        Verified: true
      PasswordChangeRequired: false
    Machine:
      Machine:
        Username: forge-setup
        Name: forge setup client
      Pat:
        ExpirationDate: '2100-01-01T00:00:00Z'
`

// startZitadel boots a fresh instance on its own database and waits until it
// serves and has written the bootstrap PAT.
func (e *smokeEnv) startZitadel(t *testing.T, tag string) *smokeInstance {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "steps.yaml"), []byte(smokeSteps), 0o644); err != nil {
		t.Fatal(err)
	}
	patDir := filepath.Join(dir, "pat")
	if err := os.MkdirAll(patDir, 0o777); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(patDir, 0o777) // the container runs as a non-root uid

	port := freePort(t)
	in := &smokeInstance{name: e.prefix + "-" + tag, base: fmt.Sprintf("http://localhost:%d", port), patPath: filepath.Join(patDir, "pat.txt")}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-fv", in.name).Run() })
	args := []string{"run", "-d", "--name", in.name, "--network", e.network,
		"-p", fmt.Sprintf("127.0.0.1:%d:8080", port),
		"-v", filepath.Join(dir, "steps.yaml") + ":/steps.yaml:ro", "-v", patDir + ":/pat"}
	for k, v := range map[string]string{
		"ZITADEL_DATABASE_POSTGRES_HOST": e.pg, "ZITADEL_DATABASE_POSTGRES_PORT": "5432",
		"ZITADEL_DATABASE_POSTGRES_DATABASE":                "zit_" + tag,
		"ZITADEL_DATABASE_POSTGRES_USER_USERNAME":           "postgres",
		"ZITADEL_DATABASE_POSTGRES_USER_PASSWORD":           "postgres",
		"ZITADEL_DATABASE_POSTGRES_USER_SSL_MODE":           "disable",
		"ZITADEL_DATABASE_POSTGRES_ADMIN_USERNAME":          "postgres",
		"ZITADEL_DATABASE_POSTGRES_ADMIN_PASSWORD":          "postgres",
		"ZITADEL_DATABASE_POSTGRES_ADMIN_SSL_MODE":          "disable",
		"ZITADEL_EXTERNALSECURE":                            "false",
		"ZITADEL_EXTERNALDOMAIN":                            "localhost",
		"ZITADEL_EXTERNALPORT":                              fmt.Sprint(port),
		"ZITADEL_FIRSTINSTANCE_PATPATH":                     "/pat/pat.txt",
		"ZITADEL_DEFAULTINSTANCE_FEATURES_LOGINV2_REQUIRED": "false",
	} {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, zitadelImage, "start-from-init", "--masterkey", "MasterkeyNeedsToHave32Characters",
		"--tlsMode", "disabled", "--steps", "/steps.yaml")
	docker(t, args...)

	deadline := time.Now().Add(5 * time.Minute)
	for {
		resp, err := http.Get(in.base + "/debug/ready")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				if raw, _ := os.ReadFile(in.patPath); len(bytes.TrimSpace(raw)) > 0 {
					in.pat = strings.TrimSpace(string(raw))
					break
				}
			}
		}
		if time.Now().After(deadline) {
			logs, _ := exec.Command("docker", "logs", "--tail", "40", in.name).CombinedOutput()
			t.Fatalf("zitadel %s never became ready\n%s", tag, logs)
		}
		time.Sleep(time.Second)
	}
	var me struct {
		Org struct {
			ID string `json:"id"`
		} `json:"org"`
	}
	// The test's own door has no retry, and the gateway lags /debug/ready.
	for {
		if err := in.rawErr("GET", "/management/v1/orgs/me", nil, &me); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("zitadel %s: management API never answered: %v", tag, err)
		}
		time.Sleep(time.Second)
	}
	in.org = me.Org.ID
	return in
}

// raw is the test's own door into the instance, independent of the library, for
// seeding state and tampering with it.
func (in *smokeInstance) raw(t *testing.T, method, path string, body, out any) {
	t.Helper()
	if err := in.rawErr(method, path, body, out); err != nil {
		t.Fatalf("raw %s %s: %v", method, path, err)
	}
}

func (in *smokeInstance) rawErr(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, in.base+path, rd)
	req.Header.Set("Authorization", "Bearer "+in.pat)
	req.Header.Set("Content-Type", "application/json")
	if in.org != "" {
		req.Header.Set("x-zitadel-orgid", in.org)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, raw)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// tokenWorks reports whether the instance accepts token as a bearer credential.
func (in *smokeInstance) tokenWorks(token string) bool {
	req, _ := http.NewRequest("GET", in.base+"/auth/v1/users/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == 200
}

// wireCounter counts WRITES on the wire. A search is a POST in Zitadel's API, so
// "no write" cannot be read off the HTTP method alone.
type wireCounter struct {
	mu     sync.Mutex
	writes []string
	inner  http.RoundTripper
}

func (w *wireCounter) RoundTrip(r *http.Request) (*http.Response, error) {
	if isWrite(r.Method, r.URL.Path) {
		w.mu.Lock()
		w.writes = append(w.writes, r.Method+" "+r.URL.Path)
		w.mu.Unlock()
	}
	return w.inner.RoundTrip(r)
}

func isWrite(method, path string) bool {
	switch {
	case method == http.MethodGet || method == http.MethodHead:
		return false
	case strings.HasSuffix(path, "/_search"), path == "/v2/users", path == "/oauth/v2/token":
		return false
	}
	return true
}

func (w *wireCounter) take() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.writes
	w.writes = nil
	return out
}

func smokeSpec() Spec {
	return Spec{
		Project: ProjectSpec{Name: "acme smoke"},
		Roles: []Role{
			{Key: "admin", DisplayName: "Administrators", Group: "staff", Declared: true, Members: []string{"ops@acme.test"}},
			{Key: "member", DisplayName: "Members"},
		},
		Applications: []Application{
			{Name: "smoke:api", Kind: AppNativeBroker, LoginURI: "https://acme.test/auth/sign-in",
				RedirectURIs: []string{"https://acme.test/auth/callback"}, PostLogoutURIs: []string{"https://acme.test/"}},
			{Name: "smoke:spa", Kind: AppSPA, DevMode: true,
				RedirectURIs: []string{"http://localhost:*/auth/callback"}, PostLogoutURIs: []string{"http://localhost:*/"}},
			{Name: "smoke:server", Kind: AppWeb, RedirectURIs: []string{"https://acme.test/srv/callback"}},
			{Name: "smoke:mobile", Kind: AppNative, RedirectURIs: []string{"test.acme.app:/callback"}},
		},
		MachineUsers: []MachineUser{
			{Username: "ops-automation", Roles: []string{"admin"}, Key: true},
			{Username: "svc-reader", Roles: []string{"member"}, Token: true},
		},
		Policies: Policies{
			Login:              &LoginPolicy{AllowUsernamePassword: true},
			PasswordComplexity: &PasswordComplexity{MinLength: 12, RequireUppercase: true, RequireLowercase: true, RequireNumber: true},
			Lockout:            &Lockout{MaxPasswordAttempts: 5, MaxOTPAttempts: 5},
		},
		SMTP:      &SMTP{Host: "smtp.acme.test:587", User: "mailer", Password: "smtp-secret", SenderAddress: "no-reply@acme.test", SenderName: "Acme", TLS: true},
		SeedUsers: []SeedUser{{Email: "dev@acme.test", Password: "Seeded-Passw0rd-1", Roles: []string{"admin"}}},
	}
}

// signIn runs the product's native sign-in against the instance exactly as a
// scaffolded server does: the broker token, the app's login route, PKCE, no
// browser.
func signIn(t *testing.T, in *smokeInstance, brokerToken, clientID, redirect, login, password string) map[string]any {
	t.Helper()
	dc := &devidp.Client{Base: in.base, Token: brokerToken}
	toks, err := dc.SignIn(context.Background(), devidp.FlowConfig{
		ClientID: clientID, RedirectURI: redirect,
		Scopes: []string{"openid", "profile", "email", RolesScope},
	}, devidp.Credentials{LoginName: login, Password: password})
	if err != nil {
		t.Fatalf("sign in as %s: %v", login, err)
	}
	return jwtClaims(t, toks.AccessToken)
}

func jwtClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("the access token is not a JWT (it is opaque): %.20s...", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func audContains(claims map[string]any, want string) bool {
	switch a := claims["aud"].(type) {
	case []any:
		for _, x := range a {
			if x == want {
				return true
			}
		}
	case string:
		return a == want
	}
	return false
}

func wantRole(t *testing.T, claims map[string]any, claim, role string) {
	t.Helper()
	roles, ok := claims[claim].(map[string]any)
	if !ok {
		t.Fatalf("token has no roles claim %q (keys: %v)", claim, keysOf(claims))
	}
	if _, ok := roles[role]; !ok {
		t.Fatalf("token roles = %v, want %q", roles, role)
	}
}

func keysOf(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestSmokeRealZitadel(t *testing.T) {
	env := startSmokeEnv(t)
	t.Run("converge_rerun_drift_rotate", func(t *testing.T) { smokeConverge(t, env) })
	t.Run("adopt_preseeded_instance", func(t *testing.T) { smokeAdopt(t, env) })
}

func smokeConverge(t *testing.T, env *smokeEnv) {
	in := env.startZitadel(t, "a")
	ctx := context.Background()
	keyDir := t.TempDir()
	keys := DirKeyStore{Dir: keyDir}
	outFile := filepath.Join(t.TempDir(), "identity", "smoke.json")
	wire := &wireCounter{inner: http.DefaultTransport}

	newRec := func(mod func(*Options)) *Reconciler {
		o := Options{
			BaseURL: in.base, Credential: PATFile(in.patPath), Keys: keys,
			Outputs: FileOutputs{Path: outFile}, AllowSeedUsers: true,
			HTTPClient: &http.Client{Transport: wire},
		}
		if mod != nil {
			mod(&o)
		}
		r, err := New(o)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	rec := newRec(nil)

	// 2. converge a spec
	rep, err := rec.Reconcile(ctx, smokeSpec())
	if err != nil {
		t.Fatalf("converge: %v\n%s", err, changes(rep))
	}
	t.Logf("first pass made %d changes", len(rep.Changes))
	out := rep.Output
	if out.Issuer != in.base {
		t.Errorf("issuer = %q, want %q", out.Issuer, in.base)
	}
	if out.ProjectID == "" || out.Audience != out.ProjectID {
		t.Errorf("project/audience = %q/%q", out.ProjectID, out.Audience)
	}
	for _, app := range []string{"smoke:api", "smoke:spa", "smoke:server", "smoke:mobile"} {
		if out.Apps[app].ClientID == "" {
			t.Errorf("no client id for %s", app)
		}
	}
	api := out.Apps["smoke:api"]
	if api.BrokerToken == "" || api.BrokerUser != "broker-smoke-api" {
		t.Fatalf("broker account not provisioned: %+v", api)
	}
	if out.Apps["smoke:server"].ClientSecret == "" {
		t.Error("web app client secret not captured")
	}
	if !strings.Contains(string(out.JWKS), `"kid"`) {
		t.Errorf("no JWKS snapshot: %s", out.JWKS)
	}
	if raw, err := os.ReadFile(outFile); err != nil || !strings.Contains(string(raw), out.ProjectID) {
		t.Errorf("outputs file: %v", err)
	} else if strings.Contains(string(raw), api.BrokerToken) {
		t.Error("the broker token leaked into the non-secret outputs file")
	}

	// The proof the content is RIGHT is that the product's own sign-in works:
	// broker token, per-app login route, PKCE, then a JWT access token whose
	// audience is the project and which carries the roles claim.
	claims := signIn(t, in, api.BrokerToken, api.ClientID, "https://acme.test/auth/callback", "dev@acme.test", "Seeded-Passw0rd-1")
	if claims["iss"] != in.base {
		t.Errorf("token iss = %v, want %s", claims["iss"], in.base)
	}
	if !audContains(claims, out.ProjectID) {
		t.Errorf("token aud = %v, want the project id %s", claims["aud"], out.ProjectID)
	}
	wantRole(t, claims, out.RolesClaim, "admin")

	// 3. re-run with no changes: nothing is written, on the wire.
	wire.take()
	rep2, err := rec.Reconcile(ctx, smokeSpec())
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if rep2.Changed() {
		t.Errorf("re-run reported changes:\n%s", changes(rep2))
	}
	if w := wire.take(); len(w) != 0 {
		t.Errorf("re-run wrote to the instance %d times: %v", len(w), w)
	}
	if rep2.Output.Apps["smoke:api"].BrokerToken != api.BrokerToken ||
		rep2.Output.Apps["smoke:api"].ClientID != api.ClientID ||
		rep2.Output.Apps["smoke:server"].ClientSecret != out.Apps["smoke:server"].ClientSecret {
		t.Error("credentials or ids changed across a no-op pass")
	}

	// 4. drift-repair: tamper through the raw API, then converge.
	smokeTamper(t, in, out)
	rep3, err := rec.Reconcile(ctx, smokeSpec())
	if err != nil {
		t.Fatalf("drift pass: %v\n%s", err, changes(rep3))
	}
	got := changes(rep3)
	for _, want := range []string{`updated project "acme smoke"`, `updated role "admin"`, `updated application "smoke:spa"`,
		`updated application "smoke:api"`, `updated password complexity policy`, `updated lockout policy`,
		`updated smtp`, `issued machine key "ops-automation"`, `revoked role grant "intruder@acme.test"`} {
		if !strings.Contains(got, want) {
			t.Errorf("drift pass did not report %q:\n%s", want, got)
		}
	}
	smokeVerifyRepaired(t, in, out)
	wire.take()
	rep4, err := rec.Reconcile(ctx, smokeSpec())
	if err != nil || rep4.Changed() {
		t.Fatalf("after repair the instance must be quiet: err=%v\n%s", err, changes(rep4))
	}
	if w := wire.take(); len(w) != 0 {
		t.Errorf("post-repair pass wrote %v", w)
	}
	api = rep4.Output.Apps["smoke:api"]

	// The credential the control plane uses: an instance-owner MACHINE KEY,
	// exchanged through the JWT-profile grant, not a PAT.
	ownerKey := smokeOwnerKey(t, in)
	cred, err := MachineKey(ownerKey)
	if err != nil {
		t.Fatal(err)
	}
	asOwner := newRec(func(o *Options) { o.Credential = cred })
	wire.take()
	rep5, err := asOwner.Reconcile(ctx, smokeSpec())
	if err != nil || rep5.Changed() {
		t.Fatalf("machine-key credential pass: err=%v\n%s", err, changes(rep5))
	}
	if w := wire.take(); len(w) != 0 {
		t.Errorf("machine-key pass wrote %v", w)
	}

	// A member with no credential gets a one-time code, which really sets a
	// first password, which really signs in.
	withCodes := newRec(func(o *Options) { o.SignInCodes = true })
	rep6, err := withCodes.Reconcile(ctx, smokeSpec())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep6.SignInCodes) != 1 || rep6.SignInCodes[0].Email != "ops@acme.test" {
		t.Fatalf("sign-in codes = %+v", rep6.SignInCodes)
	}
	var human struct {
		Result []struct {
			UserID string `json:"userId"`
		} `json:"result"`
	}
	in.raw(t, "POST", "/v2/users", map[string]any{"queries": []any{map[string]any{"emailQuery": map[string]any{
		"emailAddress": "ops@acme.test", "method": "TEXT_QUERY_METHOD_EQUALS"}}}}, &human)
	in.raw(t, "POST", "/v2/users/"+human.Result[0].UserID+"/password", map[string]any{
		"newPassword":      map[string]any{"password": "Ops-First-Passw0rd", "changeRequired": false},
		"verificationCode": rep6.SignInCodes[0].Code,
	}, nil)
	opsClaims := signIn(t, in, api.BrokerToken, api.ClientID, "https://acme.test/auth/callback", "ops@acme.test", "Ops-First-Passw0rd")
	wantRole(t, opsClaims, out.RolesClaim, "admin")
	if rep7, err := withCodes.Reconcile(ctx, smokeSpec()); err != nil || len(rep7.SignInCodes) != 0 {
		t.Fatalf("a member who set a password must not be issued another code: err=%v codes=%v", err, rep7.SignInCodes)
	} else if !slices.Contains(rep7.Credentialed, "ops@acme.test") {
		t.Fatalf("a member who set a password is not reported credentialed: %v", rep7.Credentialed)
	}

	// 6. rotate the broker credential with an overlap window.
	smokeRotate(t, in, keys, newRec, api.BrokerToken, api.ClientID)
}

// smokeTamper makes every kind of drift the library claims to repair.
func smokeTamper(t *testing.T, in *smokeInstance, out Output) {
	t.Helper()
	pid := out.ProjectID
	in.raw(t, "PUT", "/management/v1/projects/"+pid, map[string]any{"name": "acme smoke", "projectRoleAssertion": false}, nil)
	in.raw(t, "PUT", "/management/v1/projects/"+pid+"/roles/admin", map[string]any{"displayName": "tampered", "group": "x"}, nil)
	in.raw(t, "PUT", "/management/v1/projects/"+pid+"/apps/"+out.Apps["smoke:spa"].AppID+"/oidc_config", map[string]any{
		"redirectUris": []string{"http://localhost:*/evil"}, "responseTypes": []string{"OIDC_RESPONSE_TYPE_CODE"},
		"grantTypes": []string{"OIDC_GRANT_TYPE_AUTHORIZATION_CODE"}, "appType": "OIDC_APP_TYPE_USER_AGENT",
		"authMethodType": "OIDC_AUTH_METHOD_TYPE_NONE", "accessTokenType": "OIDC_TOKEN_TYPE_BEARER", "devMode": true,
	}, nil)
	in.raw(t, "PUT", "/management/v1/projects/"+pid+"/apps/"+out.Apps["smoke:api"].AppID+"/oidc_config", map[string]any{
		"redirectUris": []string{"https://acme.test/auth/callback"}, "postLogoutRedirectUris": []string{"https://acme.test/"},
		"responseTypes": []string{"OIDC_RESPONSE_TYPE_CODE"},
		"grantTypes":    []string{"OIDC_GRANT_TYPE_AUTHORIZATION_CODE", "OIDC_GRANT_TYPE_REFRESH_TOKEN"},
		"appType":       "OIDC_APP_TYPE_USER_AGENT", "authMethodType": "OIDC_AUTH_METHOD_TYPE_NONE",
		"accessTokenType": "OIDC_TOKEN_TYPE_JWT", "accessTokenRoleAssertion": true,
		"loginVersion": map[string]any{"loginV1": map[string]any{}},
	}, nil)
	in.raw(t, "PUT", "/admin/v1/policies/password/complexity", map[string]any{"minLength": "6", "hasUppercase": false, "hasLowercase": true, "hasNumber": false, "hasSymbol": false}, nil)
	in.raw(t, "PUT", "/admin/v1/policies/password/lockout", map[string]any{"maxPasswordAttempts": 99, "maxOtpAttempts": 99}, nil)
	var smtp struct {
		Result []struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	in.raw(t, "POST", "/admin/v1/smtp/_search", map[string]any{}, &smtp)
	in.raw(t, "PUT", "/admin/v1/smtp/"+smtp.Result[0].ID, map[string]any{
		"senderAddress": "no-reply@acme.test", "senderName": "Acme", "tls": true, "host": "evil.test:25",
		"user": "mailer", "description": smtpDescription}, nil)

	// A machine key deleted by hand: the stored copy is now useless.
	var u struct {
		Result []struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	in.raw(t, "POST", "/management/v1/users/_search", map[string]any{"queries": []any{map[string]any{"userNameQuery": map[string]any{
		"userName": "ops-automation", "method": "TEXT_QUERY_METHOD_EQUALS"}}}}, &u)
	var ks struct {
		Result []struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	in.raw(t, "POST", "/management/v1/users/"+u.Result[0].ID+"/keys/_search", map[string]any{}, &ks)
	for _, k := range ks.Result {
		in.raw(t, "DELETE", "/management/v1/users/"+u.Result[0].ID+"/keys/"+k.ID, nil, nil)
	}

	// Someone given admin in a console, who is not in the declared set.
	var h struct {
		UserID string `json:"userId"`
	}
	in.raw(t, "POST", "/v2/users/human", map[string]any{
		"username": "intruder@acme.test", "organization": map[string]any{"orgId": in.org},
		"profile": map[string]any{"givenName": "in", "familyName": "truder"},
		"email":   map[string]any{"email": "intruder@acme.test", "isVerified": true},
	}, &h)
	in.raw(t, "POST", "/management/v1/users/"+h.UserID+"/grants", map[string]any{"projectId": pid, "roleKeys": []string{"admin", "member"}}, nil)
}

func smokeVerifyRepaired(t *testing.T, in *smokeInstance, out Output) {
	t.Helper()
	pid := out.ProjectID
	var p struct {
		Project struct {
			ProjectRoleAssertion bool `json:"projectRoleAssertion"`
		} `json:"project"`
	}
	in.raw(t, "GET", "/management/v1/projects/"+pid, nil, &p)
	if !p.Project.ProjectRoleAssertion {
		t.Error("projectRoleAssertion not repaired")
	}
	var apps struct {
		Result []struct {
			Name       string          `json:"name"`
			OIDCConfig *observedConfig `json:"oidcConfig"`
		} `json:"result"`
	}
	in.raw(t, "POST", "/management/v1/projects/"+pid+"/apps/_search", map[string]any{}, &apps)
	for _, a := range apps.Result {
		switch a.Name {
		case "smoke:spa":
			if a.OIDCConfig.AccessTokenType != "OIDC_TOKEN_TYPE_JWT" || len(a.OIDCConfig.RedirectURIs) != 1 ||
				a.OIDCConfig.RedirectURIs[0] != "http://localhost:*/auth/callback" {
				t.Errorf("spa not repaired: %+v", a.OIDCConfig)
			}
		case "smoke:api":
			if a.OIDCConfig.LoginVersion.LoginV2 == nil || a.OIDCConfig.LoginVersion.LoginV2.BaseURI != "https://acme.test/auth/sign-in" {
				t.Errorf("login route not repaired: %+v", a.OIDCConfig.LoginVersion)
			}
		}
	}
	var grants struct {
		Result []struct {
			Email    string   `json:"email"`
			RoleKeys []string `json:"roleKeys"`
		} `json:"result"`
	}
	in.raw(t, "POST", "/management/v1/users/grants/_search", map[string]any{"queries": []any{map[string]any{"projectIdQuery": map[string]any{"projectId": pid}}}}, &grants)
	for _, g := range grants.Result {
		if g.Email == "intruder@acme.test" && (len(g.RoleKeys) != 1 || g.RoleKeys[0] != "member") {
			t.Errorf("intruder's roles = %v, want admin revoked and the console-managed role kept", g.RoleKeys)
		}
	}
	// And the account is NOT deleted: only the grant is withdrawn.
	var users struct {
		Result []struct {
			UserID string `json:"userId"`
		} `json:"result"`
	}
	in.raw(t, "POST", "/v2/users", map[string]any{"queries": []any{map[string]any{"emailQuery": map[string]any{
		"emailAddress": "intruder@acme.test", "method": "TEXT_QUERY_METHOD_EQUALS"}}}}, &users)
	if len(users.Result) != 1 {
		t.Error("a revoked member's account was deleted")
	}
}

// smokeOwnerKey creates an IAM_OWNER machine user and returns its key document,
// standing in for the owner key the control plane holds per instance.
func smokeOwnerKey(t *testing.T, in *smokeInstance) []byte {
	t.Helper()
	var u struct {
		UserID string `json:"userId"`
	}
	in.raw(t, "POST", "/management/v1/users/machine", map[string]any{"userName": "instance-owner", "name": "instance-owner", "accessTokenType": "ACCESS_TOKEN_TYPE_JWT"}, &u)
	in.raw(t, "POST", "/admin/v1/members", map[string]any{"userId": u.UserID, "roles": []string{"IAM_OWNER"}}, nil)
	var k struct {
		KeyDetails string `json:"keyDetails"`
	}
	in.raw(t, "POST", "/management/v1/users/"+u.UserID+"/keys", map[string]any{"type": "KEY_TYPE_JSON", "expirationDate": time.Now().AddDate(1, 0, 0).UTC().Format(time.RFC3339)}, &k)
	doc, err := base64.StdEncoding.DecodeString(k.KeyDetails)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func smokeRotate(t *testing.T, in *smokeInstance, keys DirKeyStore, newRec func(func(*Options)) *Reconciler, oldBroker, clientID string) {
	t.Helper()
	ctx := context.Background()
	spec := smokeSpec()
	rec := newRec(nil)

	oldKeyDoc, _ := keys.Load(ctx, MachineKeyEntry("ops-automation"))
	if !in.tokenWorks(oldBroker) {
		t.Fatal("the broker token does not work before rotation")
	}

	rep, err := rec.Rotate(ctx, spec, RotateTarget{Application: "smoke:api"}, RotateTarget{MachineUser: "ops-automation"})
	if err != nil {
		t.Fatalf("rotate: %v\n%s", err, changes(rep))
	}
	newBroker := rep.Output.Apps["smoke:api"].BrokerToken
	if newBroker == "" || newBroker == oldBroker {
		t.Fatalf("broker token not rotated")
	}
	if !in.tokenWorks(newBroker) {
		t.Error("the new broker token does not work")
	}
	// THE OVERLAP: the superseded credentials still work.
	if !in.tokenWorks(oldBroker) {
		t.Error("the old broker token stopped working inside its overlap window")
	}
	oldCred, err := MachineKey(oldKeyDoc)
	if err != nil {
		t.Fatal(err)
	}
	// A machine key is "live" if the instance still exchanges it for a token.
	// (ops-automation holds a project role, not IAM_OWNER, so it is probed at
	// the token endpoint rather than by running a pass as it.)
	probe := func(c Credential) error {
		doc := c.(*machineKeyCredential).doc
		cl := &client{base: in.base, issuer: in.base, http: http.DefaultClient, now: time.Now, timeout: 15 * time.Second, maxAttempts: 1, sleep: sleepCtx}
		_, _, err := exchangeKey(ctx, cl, doc, "openid")
		return err
	}
	if err := probe(oldCred); err != nil {
		t.Errorf("the old machine key stopped working inside its overlap: %v", err)
	}
	var retiring []string
	for _, r := range rep.Retiring {
		retiring = append(retiring, r.Kind+" "+r.Username)
		if d := time.Until(r.DeleteAfter); d < 6*day || d > 11*day {
			t.Errorf("%s %s is scheduled for deletion in %v, want ~7-10 days", r.Kind, r.Username, d)
		}
	}
	if len(retiring) != 2 {
		t.Errorf("retiring = %v, want the old broker token and the old machine key", retiring)
	}
	// And the new broker token signs a user in.
	claims := signIn(t, in, newBroker, clientID, "https://acme.test/auth/callback", "dev@acme.test", "Seeded-Passw0rd-1")
	if !audContains(claims, rep.Output.ProjectID) {
		t.Errorf("sign-in with the rotated broker token: aud = %v", claims["aud"])
	}

	// Once the overlap has elapsed the superseded credentials are removed.
	later := newRec(func(o *Options) { o.Now = func() time.Time { return time.Now().Add(11 * day) } })
	rep, err = later.Reconcile(ctx, spec)
	if err != nil {
		t.Fatalf("prune pass: %v\n%s", err, changes(rep))
	}
	if in.tokenWorks(oldBroker) {
		t.Error("the superseded broker token still works after its overlap")
	}
	if !in.tokenWorks(newBroker) {
		t.Error("pruning removed the CURRENT broker token")
	}
	if err := probe(oldCred); err == nil {
		t.Error("the superseded machine key still works after its overlap")
	}
	curDoc, _ := keys.Load(ctx, MachineKeyEntry("ops-automation"))
	cur, _ := MachineKey(curDoc)
	if err := probe(cur); err != nil {
		t.Errorf("the current machine key stopped working: %v", err)
	}
	if !strings.Contains(changes(rep), "pruned") {
		t.Errorf("prune not reported:\n%s", changes(rep))
	}
}

// ---------------------------------------------------------------------------
// adoption
// ---------------------------------------------------------------------------

func smokeAdopt(t *testing.T, env *smokeEnv) {
	in := env.startZitadel(t, "b")
	ctx := context.Background()

	// Pre-seed the instance the way the first prod instance was made: by hand and by the
	// previous provisioner, under ids a fresh converge would never produce, and
	// in a shape that has drifted from what the declaration wants.
	var proj struct {
		ID string `json:"id"`
	}
	in.raw(t, "POST", "/management/v1/projects", map[string]any{"name": "control-plane admin API"}, &proj)
	in.raw(t, "POST", "/management/v1/projects/"+proj.ID+"/roles", map[string]any{"roleKey": "admin", "displayName": "admin"}, nil)
	var app struct {
		AppID    string `json:"appId"`
		ClientID string `json:"clientId"`
	}
	in.raw(t, "POST", "/management/v1/projects/"+proj.ID+"/apps/oidc", map[string]any{
		"name": "control-plane internal-console", "redirectUris": []string{"http://localhost:8098/old"},
		"responseTypes": []string{"OIDC_RESPONSE_TYPE_CODE"}, "grantTypes": []string{"OIDC_GRANT_TYPE_AUTHORIZATION_CODE"},
		"appType": "OIDC_APP_TYPE_USER_AGENT", "authMethodType": "OIDC_AUTH_METHOD_TYPE_NONE", "devMode": true,
	}, &app)

	mkMachine := func(name string) string {
		var u struct {
			UserID string `json:"userId"`
		}
		in.raw(t, "POST", "/management/v1/users/machine", map[string]any{"userName": name, "name": name, "accessTokenType": "ACCESS_TOKEN_TYPE_BEARER"}, &u)
		return u.UserID
	}
	opsID := mkMachine("ops-automation")
	brokerID := mkMachine("control-plane-login-broker")
	in.raw(t, "POST", "/management/v1/users/"+opsID+"/grants", map[string]any{"projectId": proj.ID, "roleKeys": []string{"admin"}}, nil)
	in.raw(t, "POST", "/admin/v1/members", map[string]any{"userId": brokerID, "roles": []string{"IAM_LOGIN_CLIENT"}}, nil)
	var oldPAT struct {
		Token string `json:"token"`
	}
	in.raw(t, "POST", "/management/v1/users/"+brokerID+"/pats", map[string]any{}, &oldPAT) // the legacy, non-expiring token
	var oldKey struct {
		KeyID string `json:"keyId"`
	}
	in.raw(t, "POST", "/management/v1/users/"+opsID+"/keys", map[string]any{"type": "KEY_TYPE_JSON", "expirationDate": "2100-01-01T00:00:00Z"}, &oldKey)

	mkHuman := func(email, password string) string {
		body := map[string]any{
			"username": email, "organization": map[string]any{"orgId": in.org},
			"profile": map[string]any{"givenName": "x", "familyName": "y"},
			"email":   map[string]any{"email": email, "isVerified": true},
		}
		if password != "" {
			body["password"] = map[string]any{"password": password, "changeRequired": false}
		}
		var u struct {
			UserID string `json:"userId"`
		}
		in.raw(t, "POST", "/v2/users/human", body, &u)
		in.raw(t, "POST", "/management/v1/users/"+u.UserID+"/grants", map[string]any{"projectId": proj.ID, "roleKeys": []string{"admin"}}, nil)
		return u.UserID
	}
	adminID := mkHuman("admin@reliantlabs.test", "Password1!")
	formerID := mkHuman("former@reliantlabs.test", "Password1!")

	snapshot := func() map[string]string {
		ids := map[string]string{}
		var users struct {
			Result []struct {
				ID       string `json:"id"`
				UserName string `json:"userName"`
			} `json:"result"`
		}
		in.raw(t, "POST", "/management/v1/users/_search", map[string]any{}, &users)
		for _, u := range users.Result {
			ids["user:"+u.UserName] = u.ID
		}
		return ids
	}
	before := snapshot()

	spec := Spec{
		Project: ProjectSpec{Name: "control-plane admin API", AdoptID: proj.ID},
		Roles:   []Role{{Key: "admin", DisplayName: "admin", Declared: true, Members: []string{"admin@reliantlabs.test"}}},
		Applications: []Application{{
			Name: "control-plane internal-console", Kind: AppNativeBroker, AdoptClientID: app.ClientID,
			RedirectURIs: []string{"https://console.acme.test/auth/callback"}, LoginURI: "https://console.acme.test/auth/sign-in",
			Broker: &BrokerAccount{Username: "control-plane-login-broker"},
		}},
		MachineUsers: []MachineUser{{Username: "ops-automation", Roles: []string{"admin"}, Key: true}},
	}
	wire := &wireCounter{inner: http.DefaultTransport}
	keys := DirKeyStore{Dir: t.TempDir()}
	rec, err := New(Options{BaseURL: in.base, Credential: PAT(in.pat), Keys: keys, Adopt: true, HTTPClient: &http.Client{Transport: wire}})
	if err != nil {
		t.Fatal(err)
	}

	// A wrong pin is an error, and nothing was written for it.
	bad := spec
	bad.Applications = []Application{spec.Applications[0]}
	bad.Applications[0].AdoptClientID = "1"
	wire.take()
	if _, err := rec.Reconcile(ctx, bad); err == nil || !strings.Contains(err.Error(), "pinned client id 1") {
		t.Fatalf("a wrong client-id pin must fail: %v", err)
	}
	if w := wire.take(); len(w) != 0 {
		t.Errorf("a refused adoption wrote: %v", w)
	}

	rep, err := rec.Reconcile(ctx, spec)
	if err != nil {
		t.Fatalf("adopt: %v\n%s", err, changes(rep))
	}
	got := changes(rep)
	t.Logf("adoption changes:\n%s", got)
	for _, banned := range []string{"created project", "created application", `created machine user "ops-automation"`,
		`created machine user "control-plane-login-broker"`, "created user", "created role"} {
		if strings.Contains(got, banned) {
			t.Errorf("adoption recreated an existing object (%q):\n%s", banned, got)
		}
	}

	// The ids that other systems hold are preserved.
	if rep.Output.ProjectID != proj.ID || rep.Output.Audience != proj.ID {
		t.Errorf("project id %s became %s", proj.ID, rep.Output.ProjectID)
	}
	adopted := rep.Output.Apps["control-plane internal-console"]
	if adopted.ClientID != app.ClientID || adopted.AppID != app.AppID {
		t.Errorf("application ids changed: client %s -> %s, app %s -> %s", app.ClientID, adopted.ClientID, app.AppID, adopted.AppID)
	}
	after := snapshot()
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s: id %s became %s", k, v, after[k])
		}
	}
	if rep.Output.Machine["ops-automation"].UserID != opsID {
		t.Errorf("ops-automation id changed")
	}
	if adopted.BrokerUser != "control-plane-login-broker" || adopted.BrokerToken == "" {
		t.Fatalf("broker not issued a token: %+v", adopted)
	}

	// Existing passwords survive: the pre-existing human signs in with the
	// password they already had, on the adopted application.
	claims := signIn(t, in, adopted.BrokerToken, adopted.ClientID, "https://console.acme.test/auth/callback", "admin@reliantlabs.test", "Password1!")
	if !audContains(claims, proj.ID) {
		t.Errorf("aud = %v, want the preserved project id %s", claims["aud"], proj.ID)
	}
	wantRole(t, claims, rep.Output.RolesClaim, "admin")

	// The undeclared admin lost the GRANT, kept the ACCOUNT.
	if after["user:former@reliantlabs.test"] != formerID || after["user:admin@reliantlabs.test"] != adminID {
		t.Error("a human account was recreated or removed")
	}
	var grants struct {
		Result []struct {
			Email string `json:"email"`
		} `json:"result"`
	}
	in.raw(t, "POST", "/management/v1/users/grants/_search", map[string]any{"queries": []any{map[string]any{"projectIdQuery": map[string]any{"projectId": proj.ID}}}}, &grants)
	for _, g := range grants.Result {
		if g.Email == "former@reliantlabs.test" {
			t.Error("the undeclared admin kept the admin grant")
		}
	}

	// The legacy credentials are not yanked out from under whoever uses them.
	if !in.tokenWorks(oldPAT.Token) {
		t.Error("adoption revoked the legacy broker token immediately; it must overlap")
	}
	var retiringKinds []string
	for _, r := range rep.Retiring {
		retiringKinds = append(retiringKinds, r.Kind+" "+r.Username)
	}
	if len(retiringKinds) != 2 {
		t.Errorf("retiring = %v, want the legacy token and the legacy key", retiringKinds)
	}

	// Converged: a second pass writes nothing.
	wire.take()
	rep2, err := rec.Reconcile(ctx, spec)
	if err != nil || rep2.Changed() {
		t.Fatalf("second adoption pass: err=%v\n%s", err, changes(rep2))
	}
	if w := wire.take(); len(w) != 0 {
		t.Errorf("second adoption pass wrote %v", w)
	}
}
