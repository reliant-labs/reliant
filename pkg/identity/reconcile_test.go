package identity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// noSleep skips backoff delays but, like the real sleep, ends when ctx does:
// a mock that ignored cancellation would turn a bounded wait into a hang.
func noSleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }

func newRec(t *testing.T, f *fakeZitadel, mod func(*Options)) (*Reconciler, *MemKeyStore) {
	t.Helper()
	keys := &MemKeyStore{}
	o := Options{
		BaseURL:     f.srv.URL,
		Credential:  PAT(f.pat),
		Keys:        keys,
		Now:         func() time.Time { return f.now() },
		MaxAttempts: 3,
	}
	if mod != nil {
		mod(&o)
	}
	r, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	r.c.sleep = noSleep
	return r, keys
}

func fullSpec() Spec {
	return Spec{
		Project: ProjectSpec{Name: "acme"},
		Roles: []Role{
			{Key: "admin", DisplayName: "Administrators", Declared: true, Members: []string{"Ada@Example.com"}},
			{Key: "member"},
		},
		Applications: []Application{
			{Name: "prod:api", Kind: AppNativeBroker, LoginURI: "https://acme.example/auth/sign-in",
				RedirectURIs: []string{"https://acme.example/auth/callback"}, PostLogoutURIs: []string{"https://acme.example/"}},
			{Name: "console", Kind: AppSPA, RedirectURIs: []string{"https://console.example/cb"}},
			{Name: "server", Kind: AppWeb, RedirectURIs: []string{"https://srv.example/cb"}},
		},
		MachineUsers: []MachineUser{{Username: "ops-automation", Roles: []string{"admin"}, Key: true}},
		Policies: Policies{
			Login:              &LoginPolicy{AllowUsernamePassword: true},
			PasswordComplexity: &PasswordComplexity{MinLength: 12, RequireUppercase: true, RequireLowercase: true, RequireNumber: true},
			Lockout:            &Lockout{MaxPasswordAttempts: 5, MaxOTPAttempts: 5},
		},
		SMTP: &SMTP{Host: "smtp.example:587", User: "u", Password: "pw", SenderAddress: "no-reply@example.com", SenderName: "Acme", TLS: true},
	}
}

func TestValidateNamesEveryObject(t *testing.T) {
	err := Spec{
		Project: ProjectSpec{},
		Roles:   []Role{{Key: "Bad Key"}, {Key: "ok"}, {Key: "ok"}},
		Applications: []Application{
			{Name: "a", Kind: "bogus", RedirectURIs: []string{"x"}},
			{Name: "b", Kind: AppNativeBroker, RedirectURIs: []string{"https://x/cb"}},
		},
		MachineUsers: []MachineUser{{Username: "BAD"}, {Username: "svc", Roles: []string{"nope"}}},
		SeedUsers:    []SeedUser{{Email: "not-an-email", Password: "x"}},
	}.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	for _, want := range []string{`project ""`, `role "Bad Key"`, `role "ok": declared twice`, `application "a"`,
		`application "b"`, `LoginURI`, `machine user "BAD"`, `machine user "svc"`, `seed user "not-an-email"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s:\n%v", want, err)
		}
	}
}

func TestFirstConvergeThenNoOp(t *testing.T) {
	f := newFake(t)
	var published []Output
	r, keys := newRec(t, f, func(o *Options) {
		o.Outputs = OutputsFunc(func(_ context.Context, out Output) error { published = append(published, out); return nil })
	})
	ctx := context.Background()

	rep, err := r.Reconcile(ctx, fullSpec())
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if !rep.Changed() {
		t.Fatal("first pass reported no changes")
	}
	out := rep.Output
	proj := f.mustProject("acme")
	if out.ProjectID != proj.ID || out.Audience != proj.ID {
		t.Errorf("audience = %q, want project id %q", out.Audience, proj.ID)
	}
	if out.Issuer != f.srv.URL {
		t.Errorf("issuer = %q, want %q", out.Issuer, f.srv.URL)
	}
	if out.RolesClaim != "urn:zitadel:iam:org:project:"+proj.ID+":roles" {
		t.Errorf("roles claim = %q", out.RolesClaim)
	}
	if !strings.Contains(string(out.JWKS), `"keys"`) {
		t.Errorf("no JWKS snapshot: %s", out.JWKS)
	}
	broker := out.Apps["prod:api"]
	if broker.ClientID == "" || broker.BrokerToken == "" || broker.BrokerUser != "broker-prod-api" {
		t.Errorf("broker app output incomplete: %+v", broker)
	}
	if out.Apps["server"].ClientSecret == "" {
		t.Error("web app secret not surfaced")
	}
	for _, name := range []string{MachineKeyEntry("ops-automation"), BrokerTokenEntry("broker-prod-api"), ClientSecretEntry("server")} {
		if v, _ := keys.Load(ctx, name); len(v) == 0 {
			t.Errorf("key store has no %s", name)
		}
	}
	if len(published) != 1 {
		t.Fatalf("published %d times, want 1", len(published))
	}
	if g := f.members[f.user("broker-prod-api").ID]; len(g) != 1 || g[0] != "IAM_LOGIN_CLIENT" {
		t.Errorf("broker instance roles = %v", g)
	}
	if f.user("ada@example.com") == nil {
		t.Error("declared member was not created")
	}

	// A second pass against the converged instance writes nothing at all.
	before := f.writeCount()
	rep2, err := r.Reconcile(ctx, fullSpec())
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if rep2.Changed() {
		t.Errorf("second pass changed things: %v", rep2.Changes)
	}
	if w := f.writeCount(); w != before {
		t.Errorf("second pass wrote %d times: %v", w-before, f.log[len(f.log)-(w-before):])
	}
	if rep2.Output.Apps["prod:api"].BrokerToken != broker.BrokerToken {
		t.Error("broker token changed on a no-op pass")
	}
	if rep2.Output.Apps["prod:api"].ClientID != broker.ClientID {
		t.Error("client id changed on a no-op pass")
	}
	if rep2.Output.Apps["server"].ClientSecret != out.Apps["server"].ClientSecret {
		t.Error("web client secret was not recovered from the key store")
	}
}

func TestDriftIsRepaired(t *testing.T) {
	f := newFake(t)
	r, _ := newRec(t, f, nil)
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, fullSpec()); err != nil {
		t.Fatal(err)
	}
	proj := f.mustProject("acme")

	f.mu.Lock()
	proj.Assert = false
	proj.Roles[0].DisplayName = "tampered"
	proj.Apps[1].Config["redirectUris"] = []any{"https://evil.example/cb"}
	proj.Apps[1].Config["accessTokenType"] = "OIDC_TOKEN_TYPE_BEARER"
	proj.Apps[0].Config["loginVersion"] = map[string]any{"loginV2": map[string]any{"baseUri": "https://other/signin"}}
	f.cmplx["minLength"] = "6"
	f.lockout = map[string]any{}
	f.login["allowRegister"] = true
	f.smtp[0].Host = "evil.example:25"
	f.mu.Unlock()

	rep, err := r.Reconcile(ctx, fullSpec())
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, c := range rep.Changes {
		joined += c.String() + "\n"
	}
	for _, want := range []string{`updated project "acme"`, `updated role "admin"`, `updated application "console"`,
		`updated application "prod:api"`, `updated password complexity policy`, `updated lockout policy`,
		`updated login policy`, `updated smtp`} {
		if !strings.Contains(joined, want) {
			t.Errorf("repair missing %q in:\n%s", want, joined)
		}
	}
	if !proj.Assert || proj.Roles[0].DisplayName != "Administrators" {
		t.Error("project / role drift not repaired")
	}
	if got := strs(proj.Apps[1].Config["redirectUris"]); len(got) != 1 || got[0] != "https://console.example/cb" {
		t.Errorf("redirect URIs not repaired: %v", got)
	}
	if proj.Apps[1].Config["accessTokenType"] != "OIDC_TOKEN_TYPE_JWT" {
		t.Error("opaque access tokens not repaired to JWT")
	}
	if f.smtp[0].Host != "smtp.example:587" {
		t.Errorf("smtp host not repaired: %s", f.smtp[0].Host)
	}

	before := f.writeCount()
	rep, err = r.Reconcile(ctx, fullSpec())
	if err != nil || rep.Changed() || f.writeCount() != before {
		t.Fatalf("after repair the instance must be quiet: err=%v changes=%v", err, rep.Changes)
	}
}

func TestSecondPassSurvivesAnEmptiedKeyStore(t *testing.T) {
	f := newFake(t)
	r, keys := newRec(t, f, nil)
	ctx := context.Background()
	rep1, err := r.Reconcile(ctx, fullSpec())
	if err != nil {
		t.Fatal(err)
	}
	keys.Delete(MachineKeyEntry("ops-automation"))
	keys.Delete(BrokerTokenEntry("broker-prod-api"))
	keys.Delete(ClientSecretEntry("server"))

	rep2, err := r.Reconcile(ctx, fullSpec())
	if err != nil {
		t.Fatalf("an emptied store must be recoverable by running again: %v", err)
	}
	if rep2.Output.Apps["prod:api"].BrokerToken == rep1.Output.Apps["prod:api"].BrokerToken {
		t.Error("a lost broker token was not re-issued")
	}
	if v, _ := keys.Load(ctx, MachineKeyEntry("ops-automation")); len(v) == 0 {
		t.Error("a lost machine key was not re-issued")
	}
	// The web secret cannot be re-shown and is NOT silently regenerated: that
	// would break the server still holding the old one. It is a warning.
	if v, _ := keys.Load(ctx, ClientSecretEntry("server")); len(v) != 0 {
		t.Error("client secret was regenerated without RegenerateSecret")
	}
	found := false
	for _, w := range rep2.Warnings {
		found = found || strings.Contains(w, "RegenerateSecret")
	}
	if !found {
		t.Errorf("no warning about the unrecoverable client secret: %v", rep2.Warnings)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	f := newFake(t)
	r, _ := newRec(t, f, func(o *Options) { o.DryRun = true })
	rep, err := r.Reconcile(context.Background(), fullSpec())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Changed() || !rep.DryRun {
		t.Fatal("a dry run against an empty instance must report planned changes")
	}
	if f.writeCount() != 0 {
		t.Errorf("dry run wrote %d times: %v", f.writeCount(), f.log)
	}
}

func TestDryRunOnConvergedInstanceReportsNothing(t *testing.T) {
	f := newFake(t)
	r, keys := newRec(t, f, nil)
	if _, err := r.Reconcile(context.Background(), fullSpec()); err != nil {
		t.Fatal(err)
	}
	dr, _ := newRec(t, f, func(o *Options) { o.DryRun = true; o.Keys = keys })
	rep, err := dr.Reconcile(context.Background(), fullSpec())
	if err != nil || rep.Changed() {
		t.Fatalf("err=%v changes=%v", err, rep.Changes)
	}
}

func TestSeedUsersNeedExplicitPermission(t *testing.T) {
	f := newFake(t)
	spec := Spec{Project: ProjectSpec{Name: "p"}, Roles: []Role{{Key: "member"}},
		SeedUsers: []SeedUser{{Email: "dev@example.com", Password: "Passw0rd!xyz12", Roles: []string{"member"}}}}
	r, _ := newRec(t, f, nil)
	if _, err := r.Reconcile(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "AllowSeedUsers") {
		t.Fatalf("seed users must be refused by default, got %v", err)
	}
	r, _ = newRec(t, f, func(o *Options) { o.AllowSeedUsers = true })
	if _, err := r.Reconcile(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	u := f.user("dev@example.com")
	if u == nil || u.PasswordSetBy != "Passw0rd!xyz12" {
		t.Fatalf("seed user not created with its password: %+v", u)
	}
	u.PasswordSetBy = "changed-by-the-user"
	if _, err := r.Reconcile(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if u.PasswordSetBy != "changed-by-the-user" {
		t.Error("a seed user's password was reset on a later pass")
	}
}

func TestSpecThatGeneratesCredentialsNeedsAKeyStore(t *testing.T) {
	f := newFake(t)
	r, err := New(Options{BaseURL: f.srv.URL, Credential: PAT(f.pat)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), fullSpec()); err == nil || !strings.Contains(err.Error(), "Keys") {
		t.Fatalf("got %v", err)
	}
	if f.writeCount() != 0 {
		t.Error("nothing may be written before the spec is known to be satisfiable")
	}
}

func TestOptionsAreRequired(t *testing.T) {
	if _, err := New(Options{Credential: PAT("x")}); err == nil {
		t.Error("BaseURL is required")
	}
	if _, err := New(Options{BaseURL: "http://x"}); err == nil {
		t.Error("Credential is required")
	}
}

func TestDeclaredMembersAreASet(t *testing.T) {
	f := newFake(t)
	r, _ := newRec(t, f, nil)
	ctx := context.Background()
	spec := Spec{
		Project: ProjectSpec{Name: "p"},
		Roles: []Role{
			{Key: "admin", Declared: true, Members: []string{"a@example.com", "b@example.com"}},
			{Key: "console"}, // not declared: console managed
			{Key: "other"},
		},
	}
	if _, err := r.Reconcile(ctx, spec); err != nil {
		t.Fatal(err)
	}
	proj := f.mustProject("p")
	// A person added by hand in a console to the console-managed role, who also
	// holds admin: dropping admin must keep the console role.
	byHand := &fUser{ID: "900", UserName: "carol@example.com", Email: "carol@example.com"}
	f.mu.Lock()
	f.users["900"] = byHand
	f.grants["g900"] = &fGrant{ID: "g900", UserID: "900", ProjectID: proj.ID, Roles: []string{"admin", "console"}}
	f.grants["g901"] = &fGrant{ID: "g901", UserID: "901", ProjectID: proj.ID, Roles: []string{"console"}}
	f.users["901"] = &fUser{ID: "901", UserName: "dave@example.com", Email: "dave@example.com"}
	f.mu.Unlock()

	rep, err := r.Reconcile(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.grants["g900"].Roles; len(got) != 1 || got[0] != "console" {
		t.Errorf("carol's roles = %v, want only console (admin revoked, console kept)", got)
	}
	if _, ok := f.grants["g901"]; !ok {
		t.Error("a console-managed role's holder was touched")
	}
	if !strings.Contains(changes(rep), `revoked role grant "carol@example.com"`) {
		t.Errorf("revocation not reported: %s", changes(rep))
	}
}

func changes(r *Report) string {
	var b strings.Builder
	for _, c := range r.Changes {
		b.WriteString(c.String() + "\n")
	}
	return b.String()
}

func TestMachineUserRolesAreFullyDeclared(t *testing.T) {
	f := newFake(t)
	r, _ := newRec(t, f, nil)
	ctx := context.Background()
	spec := Spec{
		Project:      ProjectSpec{Name: "p"},
		Roles:        []Role{{Key: "admin"}, {Key: "viewer"}},
		MachineUsers: []MachineUser{{Username: "svc-one", Roles: []string{"admin", "viewer"}}},
	}
	if _, err := r.Reconcile(ctx, spec); err != nil {
		t.Fatal(err)
	}
	spec.MachineUsers[0].Roles = []string{"viewer"}
	if _, err := r.Reconcile(ctx, spec); err != nil {
		t.Fatal(err)
	}
	for _, g := range f.grants {
		if g.UserID == f.user("svc-one").ID && (len(g.Roles) != 1 || g.Roles[0] != "viewer") {
			t.Errorf("svc-one roles = %v, want [viewer]", g.Roles)
		}
	}
}

func TestSignInCodeOnlyForCredentialLessMembers(t *testing.T) {
	f := newFake(t)
	r, _ := newRec(t, f, func(o *Options) { o.SignInCodes = true })
	spec := Spec{Project: ProjectSpec{Name: "p"}, Roles: []Role{{Key: "admin", Declared: true, Members: []string{"new@example.com", "has@example.com"}}}}
	f.mu.Lock()
	f.users["500"] = &fUser{ID: "500", UserName: "has@example.com", Email: "has@example.com", AuthMethods: []string{"AUTHENTICATION_METHOD_TYPE_PASSWORD"}}
	f.mu.Unlock()
	before := f.now()
	rep, err := r.Reconcile(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.SignInCodes) != 1 || rep.SignInCodes[0].Email != "new@example.com" {
		t.Fatalf("sign-in codes = %+v, want one for new@example.com only", rep.SignInCodes)
	}
	if got := rep.SignInCodes[0].ExpiresAt; got.Before(before.Add(SignInCodeLifetime)) || got.After(f.now().Add(SignInCodeLifetime)) {
		t.Errorf("ExpiresAt = %v, want minting time + %v", got, SignInCodeLifetime)
	}
	if len(rep.Credentialed) != 1 || rep.Credentialed[0] != "has@example.com" {
		t.Errorf("credentialed = %v, want [has@example.com]", rep.Credentialed)
	}
}

// A converge loop must not mint over a code the caller still holds: minting
// invalidates it, and a pass that mints is never quiet.
func TestHeldSignInCodeIsNotReplaced(t *testing.T) {
	f := newFake(t)
	spec := Spec{Project: ProjectSpec{Name: "p"}, Roles: []Role{{Key: "admin", Declared: true, Members: []string{"new@example.com"}}}}
	held := false
	r, _ := newRec(t, f, func(o *Options) {
		o.SignInCodes = true
		o.HeldSignInCode = func(email string) bool { return held && email == "new@example.com" }
	})
	first, err := r.Reconcile(context.Background(), spec)
	if err != nil || len(first.SignInCodes) != 1 {
		t.Fatalf("first pass: err=%v codes=%d", err, len(first.SignInCodes))
	}
	held = true
	second, err := r.Reconcile(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.SignInCodes) != 0 || second.Changed() {
		t.Errorf("a held code was replaced: codes=%d changes=%v", len(second.SignInCodes), second.Changes)
	}
	if len(second.Credentialed) != 0 {
		t.Errorf("credentialed = %v, want none", second.Credentialed)
	}
	held = false
	third, err := r.Reconcile(context.Background(), spec)
	if err != nil || len(third.SignInCodes) != 1 {
		t.Fatalf("an expired (no longer held) code was not re-issued: err=%v codes=%d", err, len(third.SignInCodes))
	}
}

func TestHumanNamedLikeAMachineUserIsNeverAdopted(t *testing.T) {
	f := newFake(t)
	f.mu.Lock()
	f.users["77"] = &fUser{ID: "77", UserName: "ops-automation", Email: "x@example.com"}
	f.mu.Unlock()
	r, _ := newRec(t, f, nil)
	_, err := r.Reconcile(context.Background(), Spec{Project: ProjectSpec{Name: "p"},
		MachineUsers: []MachineUser{{Username: "ops-automation"}}})
	var oe *ObjectError
	if !errors.As(err, &oe) || oe.Kind != "machine user" || oe.Name != "ops-automation" || !strings.Contains(err.Error(), "human") {
		t.Fatalf("got %v", err)
	}
}

func TestSeedUserHoldingADeclaredRoleIsNotRevokedEachPass(t *testing.T) {
	f := newFake(t)
	r, _ := newRec(t, f, func(o *Options) { o.AllowSeedUsers = true })
	spec := Spec{
		Project:   ProjectSpec{Name: "p"},
		Roles:     []Role{{Key: "admin", Declared: true, Members: []string{"ops@example.com"}}},
		SeedUsers: []SeedUser{{Email: "dev@example.com", Password: "Passw0rd!xyz12", Roles: []string{"admin"}}},
	}
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, spec); err != nil {
		t.Fatal(err)
	}
	before := f.writeCount()
	rep, err := r.Reconcile(ctx, spec)
	if err != nil || rep.Changed() || f.writeCount() != before {
		t.Fatalf("a seed user with a declared role must converge: err=%v changes=%v", err, rep.Changes)
	}
}

func TestReadinessNeedsTheManagementGatewayToo(t *testing.T) {
	f := newFake(t)
	var healthz int
	gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/management/v1/healthz" {
			if healthz++; healthz < 3 {
				http.Error(w, `{"code":14,"message":"connection refused"}`, http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusUnauthorized) // reachable, merely not allowed: the gateway is up
			return
		}
		f.srv.Config.Handler.ServeHTTP(w, r)
	}))
	defer gate.Close()
	r, _ := New(Options{BaseURL: gate.URL, Credential: PAT(f.pat), MaxAttempts: 1, Now: f.now})
	r.c.sleep = noSleep
	if _, err := r.Reconcile(context.Background(), Spec{Project: ProjectSpec{Name: "p"}}); err != nil {
		t.Fatal(err)
	}
	if healthz < 3 {
		t.Errorf("the pass started after %d gateway probes; it must wait for the gateway", healthz)
	}
}
