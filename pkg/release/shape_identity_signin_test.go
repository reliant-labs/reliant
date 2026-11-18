package release

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Identity v2: the sign-in fields of ShapeIdentity (owners, signup, methods,
// passkeys, social, mail addressing, dev mail).

// HASH STABILITY. The bytes below are the encoding of hostedIdentity() (a v1
// declaration using every v1 field) produced at forge origin/main e0b2ac43f,
// BEFORE the sign-in fields existed. They must not move.
func TestShape_IdentitySignInFieldsLeaveV1ShapesByteIdentical(t *testing.T) {
	const pinned = `{"kind":"self_managed","workloads":[],"secrets":[],"domains":[],"clusters":[],"objects":[],"control_plane_secrets":[{"name":"forge-identity-identity","namespace":"acme","cluster":"gke_acme_prod","connected_cluster":"acme-prod","kind":"identity","keys":["AUDIENCE","ISSUER"],"required":true}],"identity":{"name":"identity","runtime":"hosted","project_name":"acme","login":{"mode":"native"},"roles":[{"key":"admin","display_name":"Administrators","members_from":"declared","members":["a@acme.test","b@acme.test"]},{"key":"member","members_from":"console"}],"applications":[{"name":"prod:api","workload":"api","kind":"native_broker"},{"name":"prod:web","workload":"web","kind":"oidc_web","redirect_uris":["https://a/cb","https://b/cb"]}],"consumers":[{"workload":"api","application":"prod:api","fields":{"issuer":"zitadel_issuer"}},{"workload":"worker"}],"machine_users":[{"name":"ops","roles":["admin"],"key_secret":{"name":"acme-ops-key","namespace":"acme","cluster":"gke_acme_prod","connected_cluster":"acme-prod"}}],"smtp":{"host":"smtp.acme.test","port":587,"user":"postmaster","sender":"no-reply@acme.test","password_secret":"SMTP_PASSWORD"},"deliveries":[{"kind":"identity","name":"forge-identity-identity","namespace":"acme","cluster":"gke_acme_prod","connected_cluster":"acme-prod"}],"managed_secrets":["RELIANT_IDENTITY_IDENTITY_AUDIENCE","RELIANT_IDENTITY_IDENTITY_ISSUER"]}}`
	b, err := shapeWithIdentity(hostedIdentity()).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != pinned {
		t.Errorf("the encoding of a v1 identity moved:\n got %s\nwant %s", b, pinned)
	}
	// Empty (non-nil) v2 lists are the same as none.
	id := hostedIdentity()
	id.Owners, id.Methods, id.Social = []string{}, []string{}, []ShapeIdentitySocial{}
	e2, err := shapeWithIdentity(id).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if string(e2) != pinned {
		t.Errorf("empty v2 lists changed the encoding:\n%s", e2)
	}
	var out Shape
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Identity.DeclaresLoginPolicy() || !reflect.DeepEqual(out.Identity.SignInMethods(), []string{IdentityMethodPassword}) || out.Identity.SignupMode() != IdentitySignupInvite {
		t.Errorf("a v1 declaration reads as password, invite-only, policy left alone: %+v", out.Identity)
	}
}

// signInHosted is a valid hosted v2 declaration, in non-canonical order.
func signInHosted() *ShapeIdentity {
	return &ShapeIdentity{
		Name: "identity", Runtime: IdentityRuntimeHosted, Login: ShapeIdentityLogin{Mode: IdentityLoginNative},
		Roles: []ShapeIdentityRole{
			{Key: "owner", MembersFrom: IdentityMembersConsole},
			{Key: "member", MembersFrom: IdentityMembersConsole},
			{Key: "ops", MembersFrom: IdentityMembersDeclared, Members: []string{"ops@acme.test"}},
		},
		Owners:      []string{"Founder@Acme.test", "cto@acme.test"},
		OwnerRole:   "owner",
		Signup:      &ShapeIdentitySignup{Mode: IdentitySignupDomain, Domains: []string{"b.acme.test", "acme.test"}, Role: "member"},
		Methods:     []string{IdentityMethodPassword, IdentityMethodPasskey, IdentityMethodMagicLink},
		PasskeyRPID: "acme.test",
		Social: []ShapeIdentitySocial{
			{Type: IdentitySocialGoogle, ClientID: "1.apps.googleusercontent.com", ClientSecretSecret: "GOOGLE_CLIENT_SECRET", Scopes: []string{"openid", "email"}},
			{Type: IdentitySocialGitHub, ClientIDSecret: "GITHUB_CLIENT_ID", ClientSecretSecret: "GITHUB_CLIENT_SECRET", Scopes: []string{"user:email"}},
		},
		Email: &ShapeIdentityEmail{Delivery: IdentityMailPlatform, FromName: "Acme", FromLocal: "no-reply", ReplyTo: "help@acme.test"},
	}
}

func signInHost() *ShapeIdentity {
	return &ShapeIdentity{
		Name: "identity", Runtime: IdentityRuntimeHost, Login: ShapeIdentityLogin{Mode: IdentityLoginNative},
		Roles:  []ShapeIdentityRole{{Key: "owner", MembersFrom: IdentityMembersConsole}},
		Owners: []string{"dev@acme.test"}, OwnerRole: "owner",
		Signup:  &ShapeIdentitySignup{Mode: IdentitySignupOpen},
		Methods: []string{IdentityMethodMagicLink},
		DevMail: "mail",
	}
}

func TestShape_IdentitySignInRoundTripsCanonically(t *testing.T) {
	in := shapeWithIdentity(signInHosted())
	b, err := in.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"owners":["cto@acme.test","founder@acme.test"]`, `"owner_role":"owner"`,
		`"signup":{"mode":"domain","domains":["acme.test","b.acme.test"],"role":"member"}`,
		`"methods":["magic_link","passkey","password"]`, `"passkey_rp_id":"acme.test"`,
		`"social":[{"type":"github","client_id_secret":"GITHUB_CLIENT_ID","client_secret_secret":"GITHUB_CLIENT_SECRET","scopes":["user:email"]},{"type":"google","client_id":"1.apps.googleusercontent.com","client_secret_secret":"GOOGLE_CLIENT_SECRET","scopes":["email","openid"]}]`,
		`"email":{"delivery":"platform","from_name":"Acme","from_local":"no-reply","reply_to":"help@acme.test"}`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("encoded shape lacks %s:\n%s", want, b)
		}
	}
	var out Shape
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if err := out.Validate(); err != nil {
		t.Fatalf("a round-tripped v2 shape is invalid: %v", err)
	}
	if again, _ := out.Encode(); string(again) != string(b) {
		t.Errorf("encode(decode(encode(x))) != encode(x):\n%s\n%s", again, b)
	}
	// Social sign-in only: methods are declared empty, which is NOT the default.
	only := signInHosted()
	only.Methods, only.PasskeyRPID, only.Signup = nil, "", nil
	if !only.DeclaresLoginPolicy() || len(only.SignInMethods()) != 0 {
		t.Errorf("social only: declares=%t methods=%v", only.DeclaresLoginPolicy(), only.SignInMethods())
	}
	if err := shapeWithIdentity(only).Canonical().Validate(); err != nil {
		t.Errorf("social-only sign-in is valid: %v", err)
	}
}

// One table row per v2 rule (V1-V15), each a mutation of a valid declaration.
func TestShape_IdentitySignInValidate(t *testing.T) {
	if err := shapeWithIdentity(signInHosted()).Canonical().Validate(); err != nil {
		t.Fatalf("the hosted reference is valid: %v", err)
	}
	if err := (Shape{Kind: EnvLocal, Identity: signInHost()}).Canonical().Validate(); err != nil {
		t.Fatalf("the host reference is valid: %v", err)
	}
	hosted := map[string]struct {
		mutate func(i *ShapeIdentity)
		want   string
	}{
		"V1 domain mode without domains": {func(i *ShapeIdentity) { i.Signup.Domains = nil }, "needs domains"},
		"V1 domains without domain mode": {func(i *ShapeIdentity) { i.Signup.Mode = IdentitySignupOpen }, "needs domains"},
		"V1 unknown signup mode":         {func(i *ShapeIdentity) { i.Signup.Mode = "anyone" }, "signup mode"},
		"V2 wildcard domain":             {func(i *ShapeIdentity) { i.Signup.Domains = []string{"*.acme.test"} }, "not a lower-case DNS name"},
		"V2 upper-case domain":           {func(i *ShapeIdentity) { i.Signup.Domains = []string{"Acme.test"} }, "not a lower-case DNS name"},
		"V2 domain twice":                {func(i *ShapeIdentity) { i.Signup.Domains = []string{"acme.test", "acme.test"} }, "listed twice"},
		"V3 signup role undeclared":      {func(i *ShapeIdentity) { i.Signup.Role = "nobody" }, "signup role"},
		"V3 signup role declared set":    {func(i *ShapeIdentity) { i.Signup.Role = "ops" }, "converged set"},
		"V4 unknown method":              {func(i *ShapeIdentity) { i.Methods = []string{"sms"} }, `method "sms"`},
		"V4 method twice":                {func(i *ShapeIdentity) { i.Methods = []string{IdentityMethodPassword, IdentityMethodPassword} }, "listed twice"},
		"V4 no way in":                   {func(i *ShapeIdentity) { i.Methods, i.Social, i.PasskeyRPID = nil, nil, "" }, "someone must be able to sign in"},
		"V5 magic link without mail":     {func(i *ShapeIdentity) { i.Email.Delivery = IdentityMailNone; i.Signup = nil }, "is an email"},
		"V6 open signup without mail": {func(i *ShapeIdentity) {
			i.Email.Delivery = IdentityMailNone
			i.Methods = []string{IdentityMethodPasskey}
		}, "verifies every new address"},
		"V7 owner not an email":             {func(i *ShapeIdentity) { i.Owners = []string{"boss"} }, "not an email address"},
		"V7 owner twice":                    {func(i *ShapeIdentity) { i.Owners = []string{"A@acme.test", "a@acme.test"} }, "case-insensitively"},
		"V8 owner role undeclared":          {func(i *ShapeIdentity) { i.OwnerRole = "boss" }, "owner_role"},
		"V8 owner role declared set":        {func(i *ShapeIdentity) { i.OwnerRole = "ops" }, "never demoted"},
		"V8 owner role without owners":      {func(i *ShapeIdentity) { i.Owners = nil }, "without owners"},
		"V9 unknown social type":            {func(i *ShapeIdentity) { i.Social[0].Type = "facebook" }, "social type"},
		"V9 social twice":                   {func(i *ShapeIdentity) { i.Social[1].Type = IdentitySocialGoogle }, "declared twice"},
		"V10 no client id":                  {func(i *ShapeIdentity) { i.Social[0].ClientID = "" }, "exactly one of client_id"},
		"V10 both client ids":               {func(i *ShapeIdentity) { i.Social[0].ClientIDSecret = "GOOGLE_CLIENT_ID" }, "exactly one of client_id"},
		"V10 client id secret not a name":   {func(i *ShapeIdentity) { i.Social[1].ClientIDSecret = "not a name" }, "client_id_secret"},
		"V10 client secret not a name":      {func(i *ShapeIdentity) { i.Social[0].ClientSecretSecret = "s3cr3t-value!" }, "never the secret"},
		"V10 no scopes":                     {func(i *ShapeIdentity) { i.Social[0].Scopes = nil }, "no scopes"},
		"V11 rp id not a DNS name":          {func(i *ShapeIdentity) { i.PasskeyRPID = "https://acme.test" }, "passkey_rp_id"},
		"V11 rp id without passkeys":        {func(i *ShapeIdentity) { i.Methods = []string{IdentityMethodPassword} }, "do not offer passkey"},
		"V12 unknown delivery":              {func(i *ShapeIdentity) { i.Email.Delivery = "pigeon" }, "email delivery"},
		"V12 bad from_local":                {func(i *ShapeIdentity) { i.Email.FromLocal = "No Reply" }, "from_local"},
		"V12 header injection in from_name": {func(i *ShapeIdentity) { i.Email.FromName = "Acme\r\nBcc: x@y.test" }, "from_name"},
		"V12 from_name too long":            {func(i *ShapeIdentity) { i.Email.FromName = strings.Repeat("a", 65) }, "from_name"},
		"V12 reply_to not an email":         {func(i *ShapeIdentity) { i.Email.ReplyTo = "help" }, "reply_to"},
		"V13 from_local with smtp": {func(i *ShapeIdentity) {
			i.SMTP = &ShapeIdentitySMTP{Host: "h", Port: 587, Sender: "a@b.test", PasswordSecret: "P"}
		}, "whose sender is the From"},
		"V14 sharer declares sign-in": {func(i *ShapeIdentity) {
			i.ShareWith = "staging"
			i.Roles = nil
			i.Owners, i.OwnerRole, i.Signup = nil, "", nil
		}, "belong to the owner"},
		"V15 dev mail on a hosted instance": {func(i *ShapeIdentity) { i.DevMail = "mail" }, "dev_mail"},
	}
	for name, c := range hosted {
		t.Run(name, func(t *testing.T) {
			id := signInHosted()
			c.mutate(id)
			err := shapeWithIdentity(id).Canonical().Validate()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want one naming %q", err, c.want)
			}
		})
	}
	host := map[string]struct {
		mutate func(i *ShapeIdentity)
		want   string
	}{
		"V5 magic link with no mailpit": {func(i *ShapeIdentity) { i.DevMail = ""; i.Signup = nil }, "is an email"},
		"V6 open signup with no mailpit": {func(i *ShapeIdentity) {
			i.DevMail = ""
			i.Methods = []string{IdentityMethodPassword}
		}, "verifies every new address"},
		"V15 dev mail not a name": {func(i *ShapeIdentity) { i.DevMail = "Mail Pit" }, "dev_mail"},
	}
	for name, c := range host {
		t.Run(name, func(t *testing.T) {
			id := signInHost()
			c.mutate(id)
			err := (Shape{Kind: EnvLocal, Identity: id}).Canonical().Validate()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want one naming %q", err, c.want)
			}
		})
	}
	// smtp is mail on either runtime.
	withSMTP := signInHost()
	withSMTP.DevMail = ""
	withSMTP.SMTP = &ShapeIdentitySMTP{Host: "h", Port: 587, Sender: "a@b.test", PasswordSecret: "P"}
	if err := (Shape{Kind: EnvLocal, Identity: withSMTP}).Canonical().Validate(); err != nil {
		t.Errorf("smtp makes mail available: %v", err)
	}
}

// A reader predating identity sign-in also predates the login_policy output:
// its validation rejects a consumer that maps or omits an output outside its
// lists (forge origin/main e0b2ac43f, pkg/release/identity.go). LimitShape
// removes both escape hatches, on a copy.
func TestLimitShape_IdentityConsumersLoseLoginPolicyForAnOldReader(t *testing.T) {
	oldOutputs := []string{"audience", "broker_token", "client_id", "idp_base", "issuer", "jwks", "login_uri", "redirect_uri", "roles_claim", "scopes"}
	oldOmittable := []string{"jwks", "login_uri", "roles_claim", "scopes"}
	old := &BundleReader{Features: []string{}}
	for name, c := range map[string]struct {
		consumer   ShapeIdentityConsumer
		wantFields map[string]string
		wantOmit   []string
	}{
		"mapped": {
			consumer:   ShapeIdentityConsumer{Workload: "api", Application: "prod:api", Fields: map[string]string{"login_policy": "policy", "issuer": "zitadel_issuer"}},
			wantFields: map[string]string{"issuer": "zitadel_issuer"},
		},
		"mapped alone": {
			consumer: ShapeIdentityConsumer{Workload: "api", Application: "prod:api", Fields: map[string]string{"login_policy": "policy"}},
		},
		"omitted": {
			consumer: ShapeIdentityConsumer{Workload: "api", Application: "prod:api", Omit: []string{"jwks", "login_policy"}},
			wantOmit: []string{"jwks"},
		},
		"omitted alone": {
			consumer: ShapeIdentityConsumer{Workload: "api", Application: "prod:api", Omit: []string{"login_policy"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			id := signInHosted()
			id.Applications = []ShapeIdentityApplication{{Name: "prod:api", Workload: "api", Kind: IdentityAppNativeBroker}}
			id.Consumers = []ShapeIdentityConsumer{{Workload: "worker"}, c.consumer}
			full := shapeWithIdentity(id).Canonical()
			if err := full.Validate(); err != nil {
				t.Fatalf("the v2 declaration is valid: %v", err)
			}
			before, _ := full.Encode()
			limited := old.LimitShape(full)
			if after, _ := full.Encode(); string(after) != string(before) {
				t.Fatalf("LimitShape modified the shape it was given:\n%s\n%s", after, before)
			}
			got := limited.Identity.Consumers
			var api ShapeIdentityConsumer
			for _, gc := range got {
				for output := range gc.Fields {
					if !slices.Contains(oldOutputs, output) {
						t.Errorf("consumer %q maps %q, which an old reader rejects", gc.Workload, output)
					}
				}
				for _, output := range gc.Omit {
					if !slices.Contains(oldOmittable, output) {
						t.Errorf("consumer %q omits %q, which an old reader rejects", gc.Workload, output)
					}
				}
				if gc.Workload == "api" {
					api = gc
				}
			}
			if !reflect.DeepEqual(api.Fields, c.wantFields) || !reflect.DeepEqual(api.Omit, c.wantOmit) {
				t.Errorf("limited consumer: fields %v omit %v, want %v %v", api.Fields, api.Omit, c.wantFields, c.wantOmit)
			}
			if err := limited.Canonical().Validate(); err != nil {
				t.Errorf("the limited shape is invalid: %v", err)
			}
			if signIn := (&BundleReader{Features: []string{BundleFeatureIdentitySignIn}}).LimitShape(full); !reflect.DeepEqual(signIn.Identity.Consumers, full.Identity.Consumers) {
				t.Errorf("a sign-in reader lost consumer outputs: %+v", signIn.Identity.Consumers)
			}
		})
	}
}
