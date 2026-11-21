package devidentity

// forge#2121 against the pinned Zitadel: an account holding credentials nobody
// proved is never proven into. Its address's owner signs up into a FRESH
// account (a second user with the same email and a username of its own,
// which Zitadel v4.19.5 accepts), and once that account is proven it takes
// the address over: the old one is renamed out of the way, nothing on it
// deleted. Driven through devidp.SignInRoutes with the broker's own token, so
// every call it makes (a duplicate-email create, the v2 username and email
// updates, the type-filtered user search) is proven to be accepted.
//
// Skipped under -short: it boots host postgres and the pinned Zitadel.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/hostinfra"
	"github.com/reliant-labs/forge/internal/secrets"
	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/pkg/devidp"
	"github.com/reliant-labs/forge/pkg/identity"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/testkit"
)

func TestFreshAccount_TakesAnAddressOverOnTheRealHostZitadel(t *testing.T) {
	testkit.Slow(t, "boots host postgres and the pinned Zitadel")
	if runtime.GOOS == "windows" {
		t.Skip("host Zitadel is exercised on unix")
	}
	dir := t.TempDir()
	steps, err := templates.ProjectTemplates().Render("idp-steps.yaml.tmpl", struct{ ProjectName string }{"acme"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "idp-steps.yaml"), steps, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pgSpec := hostinfra.Spec{Name: "postgres", Engine: hostinfra.EnginePostgres, Port: freeLoopbackPort(t), Database: "acme", User: "postgres", Password: "postgres"}
	idpSpec := hostinfra.Spec{
		Name: "idp", Engine: hostinfra.EngineZitadel, Port: freeLoopbackPort(t),
		IDPDatabase: "zitadel", IDPDatabasePort: pgSpec.Port, IDPMasterKey: "MasterkeyNeedsToHave32Characters",
		IDPStepsFile: "idp-steps.yaml", IDPPATPath: ".forge/idp/pat.txt",
		Database: "postgres", User: "postgres", Password: "postgres",
	}
	t.Cleanup(func() {
		_ = hostinfra.Stop(dir, idpSpec)
		_ = hostinfra.Stop(dir, pgSpec)
	})
	for _, s := range []hostinfra.Spec{pgSpec, idpSpec} {
		if err := hostinfra.Start(ctx, dir, s); err != nil {
			// In CI the issuer must start: a skip there would hide a
			// startup regression. Elsewhere, name what is missing.
			if os.Getenv("CI") != "" {
				t.Fatalf("host %s did not start: %v", s.Engine, err)
			}
			t.Skipf("skipped: host %s could not start on this machine (missing resource: %v); runs in CI", s.Engine, err)
		}
	}
	base := "http://localhost:" + strconv.Itoa(idpSpec.Port)
	store := filepath.Join(dir, "secrets", "dev.yaml")
	in := Input{
		Project: "acme",
		Identity: &release.ShapeIdentity{
			Name: "identity", Runtime: release.IdentityRuntimeHost,
			Login:        release.ShapeIdentityLogin{Mode: release.IdentityLoginNative},
			Roles:        []release.ShapeIdentityRole{{Key: "owner", MembersFrom: release.IdentityMembersConsole}},
			Applications: []release.ShapeIdentityApplication{{Name: "dev:api", Workload: "api", Kind: release.IdentityAppNativeBroker}},
			OwnerRole:    "owner",
			Signup:       &release.ShapeIdentitySignup{Mode: release.IdentitySignupOpen},
			Methods:      []string{release.IdentityMethodPassword},
		},
		BaseURL: base, PATPath: idpSpec.PATPath(dir), FrontendOrigin: "http://localhost:3000",
		Secret:      func(string) (string, bool) { return "", false },
		Refs:        []Ref{{EnvName: "IDP_BROKER_TOKEN", SecretName: "forge-identity-identity", SecretKey: "DEV_API_BROKER_TOKEN"}},
		StorePath:   store,
		OutputsPath: filepath.Join(dir, ".forge", "identity", "identity.json"),
		KeysDir:     filepath.Join(dir, ".forge", "identity", "identity.keys"),
	}
	if _, err := Converge(ctx, in); err != nil {
		t.Fatalf("converge: %v", err)
	}
	vals, err := secrets.ReadSecretFile(store)
	if err != nil {
		t.Fatal(err)
	}
	token := vals["IDP_BROKER_TOKEN"]
	client := &devidp.Client{Base: base, Token: token}
	call := func(method, path string, body any) map[string]any {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequestWithContext(ctx, method, base+path, rd)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 300 {
			t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, raw)
		}
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return out
	}
	user := func(id string) (username, email string, verified bool) {
		u, _ := call(http.MethodGet, "/v2/users/"+id, nil)["user"].(map[string]any)
		h, _ := u["human"].(map[string]any)
		e, _ := h["email"].(map[string]any)
		username, _ = u["username"].(string)
		email, _ = e["email"].(string)
		verified, _ = e["isVerified"].(bool)
		return
	}

	// The state an older broker could leave: an unverified account for the
	// owner's address holding a password the owner never set.
	const address = "owner@acme.test"
	squatted, err := client.CreateUser(ctx, devidp.NewUser{Email: address, Password: "Squatter-Passw0rd!", ContactOnly: true})
	if err != nil {
		t.Fatalf("plant the squatted account: %v", err)
	}
	// Zitadel's user search trails its writes (an eventually consistent
	// projection): wait until the new account is searchable.
	var unproven []devidp.UnprovenAccount
	testkit.Eventually(t, "the squatted account in the user search", func() bool {
		unproven, err = client.UnprovenAccounts(ctx)
		return err == nil && len(unproven) > 0
	}, testkit.Within(time.Minute))
	if len(unproven) != 1 || unproven[0].UserID != squatted || !unproven[0].ContactOnly ||
		len(unproven[0].Credentials) != 1 || unproven[0].Credentials[0] != "AUTHENTICATION_METHOD_TYPE_PASSWORD" {
		t.Fatalf("UnprovenAccounts = %+v, want the squatted account with its password", unproven)
	}

	routes, err := devidp.NewSignInRoutes(devidp.SignInRoutesConfig{
		Client: client,
		Policy: devidp.LoginPolicy{Present: true, Version: 1, Signup: devidp.SignupOpen, Methods: []string{devidp.MethodPassword},
			Mail: devidp.MailDev, RequireVerifiedEmail: true},
		AppBase: "http://localhost:3000", CookiePrefix: "acme", ResponseFloor: -1,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		SignedIn:     func(http.ResponseWriter, devidp.Tokens) any { return map[string]any{} },
		SessionToken: func(*http.Request) string { return "" },
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/register", routes.HandleRegister)
	mux.HandleFunc("POST /auth/verify", routes.HandleVerify)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	newBrowser := func() *http.Client {
		jar, _ := cookiejar.New(nil)
		return &http.Client{Jar: jar}
	}
	browser := newBrowser()
	postAs := func(browser *http.Client, path string, body any) int {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://localhost:3000")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		resp, err := browser.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	post := func(path string, body any) int {
		t.Helper()
		return postAs(browser, path, body)
	}

	// The owner signs up — twice at once, as two replicas would see it: the
	// issuer accepts a second user with the address under the username
	// derived from the squatted account, refuses the racing create of the
	// same username, and the loser adopts the winner. One fresh account.
	statuses := make(chan int, 2)
	other := newBrowser()
	for _, b := range []*http.Client{browser, other} {
		go func() {
			statuses <- postAs(b, "/auth/register", map[string]any{"email": address, "password": "Owner-Passw0rd!"})
		}()
	}
	for range 2 {
		if status := testkit.Receive(t, "a sign-up", statuses, testkit.Within(time.Minute)); status != http.StatusAccepted {
			t.Fatalf("register: %d", status)
		}
	}
	hits, _ := call(http.MethodPost, "/v2/users", map[string]any{"queries": []any{map[string]any{"emailQuery": map[string]any{
		"emailAddress": address, "method": "TEXT_QUERY_METHOD_EQUALS_IGNORE_CASE"}}}})["result"].([]any)
	fresh := ""
	for _, h := range hits {
		u, _ := h.(map[string]any)
		if name, _ := u["username"].(string); name == "forge-fresh-"+squatted {
			fresh, _ = u["userId"].(string)
		}
	}
	if len(hits) != 2 || fresh == "" {
		t.Fatalf("after sign-up the address has %d users (fresh %q), want the squatted one and a fresh one", len(hits), fresh)
	}
	if name, email, _ := user(squatted); name != address || email != address {
		t.Fatalf("an unproven sign-up changed the squatted account: %q <%s>", name, email)
	}

	// The owner proves the fresh account (the code is read back, as the
	// mail would carry it).
	code, _ := call(http.MethodPost, "/v2/users/"+fresh+"/email/resend", map[string]any{"returnCode": map[string]any{}})["verificationCode"].(string)
	if status := post("/auth/verify", map[string]any{"userID": fresh, "code": code}); status != http.StatusOK {
		t.Fatalf("verify: %d", status)
	}
	if name, email, verified := user(fresh); name != address || email != address || !verified {
		t.Fatalf("the fresh account: %q <%s> verified=%v, want the address as its username, verified", name, email, verified)
	}
	if name, email, verified := user(squatted); name != "forge-superseded-"+squatted || email != "superseded-"+squatted+"@forge.invalid" || verified {
		t.Fatalf("the squatted account: %q <%s> verified=%v, want renamed out of the way, unverified", name, email, verified)
	}
	methods, _ := call(http.MethodGet, "/v2/users/"+squatted+"/authentication_methods", nil)["authMethodTypes"].([]any)
	if len(methods) != 1 {
		t.Fatalf("the squatted account's credentials: %v, want its password kept (nothing deleted)", methods)
	}

	// The address names the fresh account, with the owner's password.
	if u, err := client.FindUserByEmail(ctx, address); err != nil || u.ID != fresh {
		t.Fatalf("FindUserByEmail = %+v, %v; want the fresh account", u, err)
	}
	s, err := client.CreateSession(ctx, devidp.Credentials{LoginName: address, Password: "Owner-Passw0rd!"})
	if err != nil {
		t.Fatalf("the owner's password by the address: %v", err)
	}
	if f, err := client.GetSessionFactors(ctx, s); err != nil || f.UserID != fresh {
		t.Fatalf("the owner's session is for %+v (%v), want the fresh account", f, err)
	}
	if _, err := client.CreateSession(ctx, devidp.Credentials{LoginName: address, Password: "Squatter-Passw0rd!"}); err == nil {
		t.Fatal("TAKEOVER: the squatter's password checks out by the owner's address")
	}

	// forge#2179 RC-10: a declared member whose account already exists,
	// unverified and credential-less, is adopted with its address marked
	// verified, so its sign-in code sets a password through the broker's
	// ProvenCredentialsOnly client.
	const member = "member@acme.test"
	pendingID, err := client.CreateUser(ctx, devidp.NewUser{Email: member})
	if err != nil {
		t.Fatalf("create the pending member: %v", err)
	}
	// Searchable first, or converge would not find it to adopt.
	testkit.Eventually(t, "the pending member in the user search", func() bool {
		u, err := client.FindUserByEmail(ctx, member)
		return err == nil && u.ID == pendingID
	}, testkit.Within(time.Minute))
	rec, err := identity.New(identity.Options{BaseURL: base, Issuer: base, Credential: identity.PATFile(in.PATPath),
		Keys: &identity.MemKeyStore{}, SignInCodes: true, ReadyTimeout: readyTimeout})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := rec.Reconcile(ctx, identity.Spec{Project: identity.ProjectSpec{Name: "acme"},
		Roles: []identity.Role{{Key: "owner", Declared: true, Members: []string{member}}}})
	if err != nil {
		t.Fatalf("converge the declared member: %v", err)
	}
	memberCode := ""
	for _, c := range rep.SignInCodes {
		if c.Email == member {
			memberCode = c.Code
		}
	}
	if memberCode == "" {
		t.Fatalf("no sign-in code for the adopted member: %+v", rep.SignInCodes)
	}
	if _, _, verified := user(pendingID); !verified {
		t.Fatal("the adopted member's address was not marked verified")
	}
	guarded := &devidp.Client{Base: base, Token: token, ProvenCredentialsOnly: true}
	// The issuer runs on this host, so its clock is this clock: the date it
	// reports is judged against the moment the password was set, not a
	// window around whenever the assertion happens to run.
	setAt := time.Now()
	if err := guarded.ResetPassword(ctx, devidp.PasswordReset{UserID: pendingID, Code: memberCode, NewPassword: "Member-Passw0rd!"}); err != nil {
		t.Fatalf("the adopted member's sign-in code through the broker: %v", err)
	}
	if _, err := client.CreateSession(ctx, devidp.Credentials{LoginName: member, Password: "Member-Passw0rd!"}); err != nil {
		t.Fatalf("the adopted member's new password: %v", err)
	}

	// The issuer reports when a password was set (human.passwordChanged),
	// which first-password attempts are judged against (forge#2179).
	if m, err := client.FindUserByID(ctx, pendingID); err != nil || m.PasswordChanged.IsZero() ||
		m.PasswordChanged.Before(setAt.Add(-time.Minute)) {
		t.Fatalf("passwordChanged of a password set at %v = %v (%v), want no earlier than that", setAt, m.PasswordChanged, err)
	}
}
