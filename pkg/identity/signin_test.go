package identity

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// v2Spec declares every sign-in capability: owners, open signup, passkeys,
// magic links, Google and GitHub, and mail through an HTTP endpoint.
func v2Spec() Spec {
	si := SignIn{
		Signup:  SignupOpen,
		Methods: []Method{MethodPassword, MethodPasskey, MethodMagicLink},
	}
	idps := []IdP{
		{Name: IdPName(IdPGoogle), Type: IdPGoogle, ClientID: "g-client", ClientSecret: "g-secret", Scopes: []string{"openid", "profile", "email"}},
		{Name: IdPName(IdPGitHub), Type: IdPGitHub, ClientID: "gh-client", ClientSecret: "gh-secret", Scopes: []string{"read:user", "user:email"}},
	}
	login := LoginPolicyFor(si, len(idps) > 0)
	return Spec{
		Project: ProjectSpec{Name: "acme"},
		Roles:   []Role{{Key: "owner"}, {Key: "member"}},
		Policies: Policies{
			Login:              &login,
			PasswordComplexity: &PasswordComplexity{MinLength: 10, RequireUppercase: true, RequireLowercase: true, RequireNumber: true},
		},
		EmailHTTP: &HTTPEmail{Endpoint: "http://identity-mail.cp.svc.cluster.local:8090/v1/zitadel/inst-1"},
		IdPs:      idps,
		Owners: []Owner{{Email: "Founder@Acme.example", Roles: []string{"owner"}, Invite: &Invite{
			URLTemplate:     "https://app.acme.example/auth/invite?userID={{.UserID}}&code={{.Code}}&orgID={{.OrgID}}",
			ApplicationName: "Acme",
		}}},
		SignIn: &si,
	}
}

func quiet(t *testing.T, f *fakeZitadel, r *Reconciler, spec Spec) *Report {
	t.Helper()
	before := f.writeCount()
	rep, err := r.Reconcile(context.Background(), spec)
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	if rep.Changed() {
		t.Errorf("a converged instance reported changes: %v", rep.Changes)
	}
	if w := f.writeCount(); w != before {
		t.Errorf("a converged instance was written %d times: %v", w-before, f.log[len(f.log)-(w-before):])
	}
	return rep
}

func TestSignInDeclarationConvergesThenWritesNothing(t *testing.T) {
	f := newFake(t)
	r, keys := newRec(t, f, nil)
	ctx := context.Background()

	rep, err := r.Reconcile(ctx, v2Spec())
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}

	// Login policy and the magic-link factor.
	if f.login["allowRegister"] != true || f.login["allowExternalIdp"] != true || f.login["passwordlessType"] != "PASSWORDLESS_TYPE_ALLOWED" {
		t.Errorf("login policy = %v", f.login)
	}
	if !contains(f.secondFactors, secondFactorOTPEmail) {
		t.Errorf("OTP email factor missing: %v", f.secondFactors)
	}
	g := f.generators["SECRET_GENERATOR_TYPE_OTP_EMAIL"]
	if num(g["length"]) != OTPEmailCodeLength || g["expiry"] != "600s" || g["includeDigits"] != true {
		t.Errorf("OTP email generator = %v", g)
	}

	// The HTTP mail provider, active, with its signing key remembered.
	if len(f.smtp) != 1 || !f.smtp[0].HTTP || f.smtp[0].State != "SMTP_CONFIG_ACTIVE" {
		t.Fatalf("mail providers = %+v", f.smtp)
	}
	if k, _ := keys.Load(ctx, KeyMailSigning); string(k) != f.smtp[0].SigningKey {
		t.Errorf("signing key in store = %q, instance has %q", k, f.smtp[0].SigningKey)
	}

	// IdPs: created by name, with Zitadel's own linking and creation off,
	// linked into the login policy.
	google, github := f.idp("forge:google"), f.idp("forge:github")
	if google == nil || github == nil {
		t.Fatalf("idps = %+v", f.idps)
	}
	if google.Secret != "g-secret" || google.ClientID != "g-client" || github.Type != "github" {
		t.Errorf("google = %+v github = %+v", google, github)
	}
	if google.Options["isCreationAllowed"] != false || google.Options["isLinkingAllowed"] != false {
		t.Errorf("idp options = %v", google.Options)
	}
	if !contains(f.loginIDPs, google.ID) || !contains(f.loginIDPs, github.ID) {
		t.Errorf("login policy idps = %v", f.loginIDPs)
	}
	if rep.Output.IdPIDs["forge:google"] != google.ID {
		t.Errorf("output idp ids = %v", rep.Output.IdPIDs)
	}

	// The owner: created verified, granted, invited exactly once.
	owner := f.user("founder@acme.example")
	if owner == nil {
		t.Fatal("owner was not created")
	}
	if f.invites[owner.ID] != 1 || len(rep.OwnersInvited) != 1 {
		t.Errorf("invites = %d, report = %v", f.invites[owner.ID], rep.OwnersInvited)
	}
	granted := false
	for _, gr := range f.grants {
		granted = granted || (gr.UserID == owner.ID && contains(gr.Roles, "owner"))
	}
	if !granted {
		t.Error("owner was not granted the owner role")
	}

	// The LOGIN_POLICY document.
	doc := rep.Output.LoginPolicy
	if doc == nil {
		t.Fatal("no login policy document")
	}
	if doc.Version != 1 || doc.Signup != "open" || strings.Join(doc.Methods, ",") != "password,passkey,magic_link" {
		t.Errorf("doc = %+v", doc)
	}
	if len(doc.Social) != 2 || doc.Social[0].IdPID != google.ID || doc.Social[0].Label != "Google" || doc.Social[1].Type != "github" {
		t.Errorf("doc social = %+v", doc.Social)
	}
	if doc.Password == nil || doc.Password.MinLength != 10 || !doc.Password.Uppercase || doc.Password.Symbol {
		t.Errorf("doc password = %+v", doc.Password)
	}
	raw, _ := json.Marshal(doc.WithMail(MailPlatform))
	for _, want := range []string{`"signup_domains":[]`, `"mail":"platform"`, `"require_verified_email":true`, `"passkey_rp_id":""`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("document %s lacks %s", raw, want)
		}
	}
	if d := doc.WithMail(MailNone); d.RequireVerifiedEmail {
		t.Error("with no mail, a verified address cannot be required")
	}

	rep2 := quiet(t, f, r, v2Spec())
	if f.invites[owner.ID] != 1 {
		t.Errorf("the owner was invited again: %d", f.invites[owner.ID])
	}
	if len(rep2.OwnersPending) != 1 {
		t.Errorf("an invited, credential-less owner is pending: %v", rep2.OwnersPending)
	}
}

func TestOwnersAreInvitedOnceThenAfterTheIntervalAndCapped(t *testing.T) {
	f := newFake(t)
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	f.now = func() time.Time { return now }
	r, _ := newRec(t, f, nil)
	ctx := context.Background()
	spec := v2Spec()

	pass := func() *Report {
		t.Helper()
		rep, err := r.Reconcile(ctx, spec)
		if err != nil {
			t.Fatal(err)
		}
		return rep
	}
	pass()
	owner := f.user("founder@acme.example")
	for i := 0; i < 3; i++ {
		pass()
	}
	if f.invites[owner.ID] != 1 {
		t.Fatalf("four passes in a day sent %d invitations", f.invites[owner.ID])
	}
	now = now.Add(OwnerInviteInterval + time.Minute)
	pass()
	now = now.Add(OwnerInviteInterval + time.Minute)
	pass()
	if f.invites[owner.ID] != 3 {
		t.Fatalf("after two intervals: %d invitations, want 3", f.invites[owner.ID])
	}
	now = now.Add(OwnerInviteInterval + time.Minute)
	rep := pass()
	if f.invites[owner.ID] != 3 {
		t.Errorf("the cap was exceeded: %d invitations", f.invites[owner.ID])
	}
	if len(rep.OwnersPending) != 1 || len(rep.Warnings) == 0 {
		t.Errorf("a capped owner is pending with a warning: pending=%v warnings=%v", rep.OwnersPending, rep.Warnings)
	}

	// Once they hold a credential, nothing more is sent or pending.
	owner.AuthMethods = []string{"AUTHENTICATION_METHOD_TYPE_PASSKEY"}
	if rep := pass(); len(rep.OwnersPending) != 0 || len(rep.OwnersInvited) != 0 {
		t.Errorf("a credentialed owner: pending=%v invited=%v", rep.OwnersPending, rep.OwnersInvited)
	}
}

func TestARemovedOwnerKeepsTheirAccountAndRoles(t *testing.T) {
	f := newFake(t)
	r, _ := newRec(t, f, nil)
	spec := v2Spec()
	spec.Owners = append(spec.Owners, Owner{Email: "cofounder@acme.example", Roles: []string{"owner", "member"}})
	if _, err := r.Reconcile(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	co := f.user("cofounder@acme.example")
	spec.Owners = spec.Owners[:1]
	quiet(t, f, r, spec)
	for _, gr := range f.grants {
		if gr.UserID == co.ID && (!contains(gr.Roles, "owner") || !contains(gr.Roles, "member")) {
			t.Errorf("a removed owner lost roles: %v", gr.Roles)
		}
	}
	if f.user("cofounder@acme.example") == nil {
		t.Error("a removed owner's account is gone")
	}
}

func TestAnOwnerWithNoMailIsCreatedAndPending(t *testing.T) {
	f := newFake(t)
	r, _ := newRec(t, f, nil)
	spec := Spec{Project: ProjectSpec{Name: "acme"}, Roles: []Role{{Key: "owner"}},
		Owners: []Owner{{Email: "solo@acme.example", Roles: []string{"owner"}}}}
	rep, err := r.Reconcile(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if u := f.user("solo@acme.example"); u == nil || f.invites[u.ID] != 0 {
		t.Fatalf("owner = %+v, invites = %v", u, f.invites)
	}
	if strings.Join(rep.OwnersPending, ",") != "solo@acme.example" {
		t.Errorf("pending = %v", rep.OwnersPending)
	}
}

func TestIdPSecretRotationAndRemoval(t *testing.T) {
	f := newFake(t)
	r, keys := newRec(t, f, nil)
	ctx := context.Background()
	spec := v2Spec()
	if _, err := r.Reconcile(ctx, spec); err != nil {
		t.Fatal(err)
	}
	google := f.idp("forge:google")

	// A rotated secret is pushed; the digest makes the next pass quiet.
	spec.IdPs[0].ClientSecret = "g-secret-2"
	rep, err := r.Reconcile(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if google.Secret != "g-secret-2" || !strings.Contains(changes(rep), `updated identity provider "forge:google" (client secret)`) {
		t.Errorf("secret = %q changes = %s", google.Secret, changes(rep))
	}
	quiet(t, f, r, spec)

	// A lost digest re-applies the secret once, then is quiet again.
	keys.Delete(keyIdPPrefix + "forge:google")
	if _, err := r.Reconcile(ctx, spec); err != nil {
		t.Fatal(err)
	}
	quiet(t, f, r, spec)

	// Drift in the settings is repaired.
	f.mu.Lock()
	google.Options["isCreationAllowed"] = true
	google.Scopes = []string{"openid"}
	f.mu.Unlock()
	rep, _ = r.Reconcile(ctx, spec)
	if google.Options["isCreationAllowed"] != false || len(google.Scopes) != 3 {
		t.Errorf("idp drift not repaired: %+v (%s)", google, changes(rep))
	}

	// A foreign IdP someone linked by hand is never touched.
	f.mu.Lock()
	f.idps = append(f.idps, &fIdP{ID: "foreign", Name: "Okta", Type: "google", ClientID: "x", Secret: "y"})
	f.loginIDPs = append(f.loginIDPs, "foreign")
	f.mu.Unlock()

	// Dropping GitHub unlinks it from the policy and keeps the IdP.
	github := f.idp("forge:github")
	spec.IdPs = spec.IdPs[:1]
	rep, err = r.Reconcile(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if contains(f.loginIDPs, github.ID) || f.idp("forge:github") == nil {
		t.Errorf("github: linked=%v present=%v", contains(f.loginIDPs, github.ID), f.idp("forge:github") != nil)
	}
	if !contains(f.loginIDPs, "foreign") || !contains(f.loginIDPs, google.ID) {
		t.Errorf("login policy idps = %v", f.loginIDPs)
	}
	if !strings.Contains(changes(rep), `revoked login policy identity provider "forge:github"`) {
		t.Errorf("changes = %s", changes(rep))
	}
	if len(rep.Output.LoginPolicy.Social) != 1 {
		t.Errorf("doc social = %+v", rep.Output.LoginPolicy.Social)
	}
	quiet(t, f, r, spec)
}

// A populated store with a provider list that hides the key is converged as
// it is: the first pass after the key was stored writes nothing.
func TestAHiddenSigningKeyTheStoreHoldsIsNotRotated(t *testing.T) {
	f := newFake(t)
	r, keys := newRec(t, f, nil)
	ctx := context.Background()
	spec := v2Spec()
	if _, err := r.Reconcile(ctx, spec); err != nil {
		t.Fatal(err)
	}
	key := f.smtp[0].SigningKey
	f.hideSigningKey = true
	quiet(t, f, r, spec)
	if f.smtp[0].SigningKey != key {
		t.Errorf("signing key rotated")
	}
	if k, _ := keys.Load(ctx, KeyMailSigning); string(k) != key {
		t.Errorf("stored key = %q, want %q", k, key)
	}
}

// failingKeys fails every Load of one name, as a store outage would.
type failingKeys struct {
	*MemKeyStore
	name string
}

func (k failingKeys) Load(ctx context.Context, name string) ([]byte, error) {
	if name == k.name {
		return nil, errors.New("store unavailable")
	}
	return k.MemKeyStore.Load(ctx, name)
}

// A declared, linked IdP whose reconciliation fails stays linked.
func TestAFailedIdPReconciliationKeepsItsLink(t *testing.T) {
	f := newFake(t)
	r, keys := newRec(t, f, nil)
	ctx := context.Background()
	spec := v2Spec()
	if _, err := r.Reconcile(ctx, spec); err != nil {
		t.Fatal(err)
	}
	google := f.idp("forge:google")

	broken, _ := newRec(t, f, func(o *Options) {
		o.Keys = failingKeys{MemKeyStore: keys, name: keyIdPPrefix + "forge:google"}
	})
	if _, err := broken.Reconcile(ctx, spec); err == nil || !strings.Contains(err.Error(), "store unavailable") {
		t.Fatalf("err = %v, want the store failure", err)
	}
	if !contains(f.loginIDPs, google.ID) {
		t.Errorf("a declared IdP was unlinked after a store failure: %v", f.loginIDPs)
	}
	for _, l := range f.log {
		if strings.Contains(l, "DELETE") && strings.Contains(l, "/policies/login/idps") {
			t.Errorf("unlink issued: %s", l)
		}
	}
}

func TestMailProviderSwitchesAndRepairsItsKey(t *testing.T) {
	f := newFake(t)
	r, keys := newRec(t, f, nil)
	ctx := context.Background()
	spec := v2Spec()
	if _, err := r.Reconcile(ctx, spec); err != nil {
		t.Fatal(err)
	}
	httpProvider := f.smtp[0]

	// A store that lost the key is repaired from the instance, with no write.
	keys.Delete(KeyMailSigning)
	quiet(t, f, r, spec)
	if k, _ := keys.Load(ctx, KeyMailSigning); string(k) != httpProvider.SigningKey {
		t.Errorf("signing key not recovered: %q", k)
	}
	// One the instance will not show is rotated in place.
	keys.Delete(KeyMailSigning)
	f.hideSigningKey = true
	old := httpProvider.SigningKey
	rep, err := r.Reconcile(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if httpProvider.SigningKey == old || !strings.Contains(changes(rep), "signing key rotated") {
		t.Errorf("key not rotated: %s", changes(rep))
	}
	if k, _ := keys.Load(ctx, KeyMailSigning); string(k) != httpProvider.SigningKey {
		t.Errorf("rotated key not stored: %q", k)
	}
	// Once recovered, a hidden key the store holds is kept: no rotation, no write.
	rotated := httpProvider.SigningKey
	quiet(t, f, r, spec)
	if httpProvider.SigningKey != rotated {
		t.Errorf("a stored key was rotated again")
	}
	if k, _ := keys.Load(ctx, KeyMailSigning); string(k) != rotated {
		t.Errorf("stored key changed: %q", k)
	}
	f.hideSigningKey = false

	// Switching to SMTP activates an SMTP relay; the HTTP provider goes idle.
	spec.EmailHTTP = nil
	spec.SMTP = &SMTP{Host: "127.0.0.1:1025", SenderAddress: "no-reply@acme.example", SenderName: "Acme", ReplyTo: "help@acme.example"}
	if _, err := r.Reconcile(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if len(f.smtp) != 2 || f.smtp[1].HTTP || f.smtp[1].State != "SMTP_CONFIG_ACTIVE" || httpProvider.State == "SMTP_CONFIG_ACTIVE" {
		t.Fatalf("providers = %+v %+v", *f.smtp[0], *f.smtp[1])
	}
	// The reply-to is read back, so a converged relay is quiet.
	quiet(t, f, r, spec)

	// None deactivates the managed relay and leaves a foreign one alone.
	f.mu.Lock()
	f.smtp = append(f.smtp, &fSMTP{ID: "foreign", Host: "other:25", Desc: "by hand", State: "SMTP_CONFIG_INACTIVE"})
	f.mu.Unlock()
	spec.SMTP, spec.EmailNone = nil, true
	if _, err := r.Reconcile(ctx, spec); err != nil {
		t.Fatal(err)
	}
	for _, s := range f.smtp {
		if s.State == "SMTP_CONFIG_ACTIVE" {
			t.Errorf("provider %s still active", s.ID)
		}
	}
	quiet(t, f, r, spec)
}

func TestNoSignInDeclarationReadsAndWritesNothingNew(t *testing.T) {
	f := newFake(t)
	r, _ := newRec(t, f, nil)
	spec := fullSpec()
	spec.SMTP = nil
	rep, err := r.Reconcile(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range f.log {
		for _, p := range []string{"/admin/v1/idps", "/policies/login/", "/secretgenerators", "/admin/v1/email", "invite_code"} {
			if strings.Contains(l, p) {
				t.Errorf("a pre-v2 spec touched %s: %s", p, l)
			}
		}
	}
	if rep.Output.LoginPolicy != nil || rep.Output.IdPIDs != nil {
		t.Errorf("a pre-v2 spec published v2 outputs: %+v %+v", rep.Output.LoginPolicy, rep.Output.IdPIDs)
	}
	if !contains(f.secondFactors, secondFactorOTPEmail) {
		t.Error("a login policy with OTPEmail nil removed a second factor")
	}
}

func TestSignInWithoutMagicLinkRemovesTheFactor(t *testing.T) {
	f := newFake(t)
	r, _ := newRec(t, f, nil)
	spec := v2Spec()
	spec.SignIn.Methods = []Method{MethodPassword}
	lp := LoginPolicyFor(*spec.SignIn, true)
	spec.Policies.Login = &lp
	if _, err := r.Reconcile(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if contains(f.secondFactors, secondFactorOTPEmail) {
		t.Errorf("second factors = %v", f.secondFactors)
	}
	quiet(t, f, r, spec)
}

func TestDryRunOfASignInDeclarationWritesNothing(t *testing.T) {
	f := newFake(t)
	r, _ := newRec(t, f, func(o *Options) { o.DryRun = true })
	rep, err := r.Reconcile(context.Background(), v2Spec())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Changed() || f.writeCount() != 0 {
		t.Errorf("changes=%d writes=%d %v", len(rep.Changes), f.writeCount(), f.log)
	}
}

func TestLoginPolicyFor(t *testing.T) {
	for _, tc := range []struct {
		in                                                 SignIn
		social                                             bool
		password, register, external, hideReset, otp, pkey bool
	}{
		{in: SignIn{Methods: []Method{MethodPassword}}, password: true},
		{in: SignIn{}, social: true, external: true, hideReset: true},
		{in: SignIn{Signup: SignupOpen, Methods: []Method{MethodPasskey}}, register: true, hideReset: true, pkey: true},
		{in: SignIn{Signup: SignupDomain, SignupDomains: []string{"acme.com"}, Methods: []Method{MethodMagicLink, MethodPassword}}, social: true,
			password: true, register: true, external: true, otp: true},
	} {
		lp := LoginPolicyFor(tc.in, tc.social)
		if lp.AllowUsernamePassword != tc.password || lp.AllowRegister != tc.register || lp.AllowExternalIDP != tc.external ||
			lp.HidePasswordReset != tc.hideReset || lp.OTPEmail == nil || *lp.OTPEmail != tc.otp ||
			(lp.PasswordlessType == "PASSWORDLESS_TYPE_ALLOWED") != tc.pkey {
			t.Errorf("LoginPolicyFor(%+v, %v) = %+v", tc.in, tc.social, lp)
		}
	}
}

func TestValidateSignInDeclarations(t *testing.T) {
	err := Spec{
		Project: ProjectSpec{Name: "p"},
		Roles:   []Role{{Key: "admin", Declared: true}, {Key: "owner"}},
		SMTP:    &SMTP{Host: "h:25", SenderAddress: "a@b.example"}, EmailNone: true,
		EmailHTTP: &HTTPEmail{Endpoint: "not a url"},
		IdPs: []IdP{
			{Name: "google", Type: IdPGoogle, ClientID: "c", ClientSecret: "s", Scopes: []string{"openid"}},
			{Name: "forge:x", Type: "okta", ClientID: "c", ClientSecret: "", Scopes: nil},
		},
		Owners: []Owner{
			{Email: "a@b.example", Roles: []string{"admin"}},
			{Email: "A@b.example", Roles: []string{"nope"}},
			{Email: "c@b.example", Invite: &Invite{URLTemplate: "/relative"}},
		},
		SignIn: &SignIn{Signup: SignupDomain, SignupDomains: []string{"*.acme.com"}, SignupRole: "admin",
			Methods: []Method{MethodPassword, MethodPassword, "sms"}, PasskeyRPID: "acme.com"},
	}.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	for _, want := range []string{
		"exclusive", "absolute http(s) URL", `identity provider "google"`, `type "okta"`, "client secret are required",
		"at least one scope", `owner "a@b.example": role "admin" is Declared`, `owner "A@b.example": declared twice`,
		`role "nope"`, "invitation URL template", `"*.acme.com"`, `sign-in "signup": role "admin" is Declared`,
		`method "password" is listed twice`, `method "sms"`, "passkey is not a method",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
	err = Spec{Project: ProjectSpec{Name: "p"}, SignIn: &SignIn{}}.Validate()
	if err == nil || !strings.Contains(err.Error(), "nobody could sign in") {
		t.Errorf("a sign-in with no way in: %v", err)
	}
	if err := v2Spec().Validate(); err != nil {
		t.Errorf("the full v2 spec is valid: %v", err)
	}
}
