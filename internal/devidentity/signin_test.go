package devidentity

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/identity"
	"github.com/reliant-labs/forge/pkg/release"
)

// v2Input declares every sign-in capability on a dev env with a Mailpit.
func v2Input() Input {
	in := baseInput()
	id := in.Identity
	id.Roles = append(id.Roles, release.ShapeIdentityRole{Key: "owner", MembersFrom: release.IdentityMembersConsole})
	id.Owners = []string{"founder@acme.test"}
	id.OwnerRole = "owner"
	id.Signup = &release.ShapeIdentitySignup{Mode: release.IdentitySignupDomain, Domains: []string{"acme.test"}, Role: "member"}
	id.Methods = []string{release.IdentityMethodPassword, release.IdentityMethodPasskey, release.IdentityMethodMagicLink}
	id.PasskeyRPID = "acme.example" // the hosted envs' relying party
	id.Social = []release.ShapeIdentitySocial{
		{Type: release.IdentitySocialGoogle, ClientID: "g-client", ClientSecretSecret: "GOOGLE_CLIENT_SECRET", Scopes: []string{"email", "openid", "profile"}},
		{Type: release.IdentitySocialGitHub, ClientIDSecret: "GITHUB_CLIENT_ID", ClientSecretSecret: "GITHUB_CLIENT_SECRET", Scopes: []string{"read:user", "user:email"}},
	}
	id.Email = &release.ShapeIdentityEmail{Delivery: release.IdentityMailPlatform, FromName: "Acme", ReplyTo: "help@acme.test"}
	id.DevMail = "mail"
	in.MailpitPort = 1025
	secrets := map[string]string{"DEV_PASSWORD": "Password1!", "GOOGLE_CLIENT_SECRET": "g-secret", "GITHUB_CLIENT_ID": "gh-client", "GITHUB_CLIENT_SECRET": "gh-secret"}
	in.Secret = func(n string) (string, bool) { v, ok := secrets[n]; return v, ok }
	return in
}

func TestSpec_SignInDeclarationGetsTheHostedPolicies(t *testing.T) {
	in := v2Input()
	spec, warnings, err := Spec(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v", warnings)
	}
	// The same mapping the control plane uses.
	want := identity.LoginPolicyFor(*spec.SignIn, true)
	if spec.Policies.Login == nil || *spec.Policies.Login.OTPEmail != *want.OTPEmail || spec.Policies.Login.AllowRegister != want.AllowRegister ||
		spec.Policies.Login.PasswordlessType != "PASSWORDLESS_TYPE_ALLOWED" || !spec.Policies.Login.AllowExternalIDP {
		t.Errorf("login policy = %+v, want %+v", spec.Policies.Login, want)
	}
	si := spec.SignIn
	if si.Signup != identity.SignupDomain || si.SignupDomains[0] != "acme.test" || si.SignupRole != "member" || len(si.Methods) != 3 {
		t.Errorf("sign-in = %+v", si)
	}
	if si.PasskeyRPID != "localhost" {
		t.Errorf("dev passkeys are bound to the loopback host, got rp id %q", si.PasskeyRPID)
	}
	// Mail goes to Mailpit, addressed as the declaration says.
	m := spec.SMTP
	if m == nil || m.Host != "127.0.0.1:1025" || m.TLS || m.User != "" || m.SenderName != "Acme" || m.ReplyTo != "help@acme.test" ||
		m.SenderAddress != "no-reply@acme.localhost" {
		t.Errorf("smtp = %+v", m)
	}
	// Owners are invited into Mailpit, linking to the dev sign-in origin.
	if len(spec.Owners) != 1 || spec.Owners[0].Roles[0] != "owner" || spec.Owners[0].Invite == nil ||
		!strings.HasPrefix(spec.Owners[0].Invite.URLTemplate, "http://localhost:3000/auth/invite?userID={{.UserID}}&code={{.Code}}") ||
		spec.Owners[0].Invite.ApplicationName != "Acme" {
		t.Errorf("owners = %+v", spec.Owners)
	}
	// Social clients resolve from the store, a secret client id included.
	if len(spec.IdPs) != 2 || spec.IdPs[0].Name != "forge:google" || spec.IdPs[0].ClientSecret != "g-secret" ||
		spec.IdPs[1].ClientID != "gh-client" || spec.IdPs[1].ClientSecret != "gh-secret" {
		t.Errorf("idps = %+v", spec.IdPs)
	}
	if MailState(in) != identity.MailDev {
		t.Errorf("mail state = %q", MailState(in))
	}
}

func TestSpec_WithoutMailOwnersAreNotInvited(t *testing.T) {
	in := v2Input()
	in.MailpitPort = 0
	in.Identity.DevMail = ""
	in.Identity.Methods = []string{release.IdentityMethodPassword}
	in.Identity.Signup = nil
	spec, _, err := Spec(in)
	if err != nil {
		t.Fatal(err)
	}
	if spec.SMTP != nil || spec.Owners[0].Invite != nil {
		t.Errorf("no mail: smtp = %+v invite = %+v", spec.SMTP, spec.Owners[0].Invite)
	}
	if MailState(in) != identity.MailNone {
		t.Errorf("mail state = %q", MailState(in))
	}
}

func TestSpec_ASocialProviderWithoutItsSecretIsLeftOut(t *testing.T) {
	in := v2Input()
	full := in.Secret
	in.Secret = func(n string) (string, bool) {
		if n == "GITHUB_CLIENT_SECRET" {
			return "", false
		}
		return full(n)
	}
	spec, warnings, err := Spec(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.IdPs) != 1 || spec.IdPs[0].Type != identity.IdPGoogle {
		t.Errorf("idps = %+v", spec.IdPs)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "GITHUB_CLIENT_SECRET") {
		t.Errorf("warnings = %v", warnings)
	}

	// Social-only, with no provider usable: nobody could sign in, said plainly.
	in.Identity.Methods = nil
	in.Identity.Social = in.Identity.Social[1:]
	if _, _, err := Spec(in); err == nil || !strings.Contains(err.Error(), "nobody could sign in") {
		t.Errorf("got %v", err)
	}
}

func TestSpec_APreV2DeclarationLeavesTheLoginPolicyAlone(t *testing.T) {
	spec, _, err := Spec(baseInput())
	if err != nil {
		t.Fatal(err)
	}
	if spec.Policies.Login != nil || spec.SignIn != nil || spec.IdPs != nil || spec.Owners != nil || spec.SMTP != nil {
		t.Errorf("a pre-v2 declaration got v2 content: %+v", spec)
	}
}

func TestOutputKeys_DeliverTheLoginPolicyDocument(t *testing.T) {
	doc := identity.LoginPolicyDoc{Version: 1, Signup: "invite", Methods: []string{"password"}}.WithMail(identity.MailDev)
	kv := OutputKeys(identity.Output{LoginPolicy: &doc})
	var back identity.LoginPolicyDoc
	if err := json.Unmarshal([]byte(kv["LOGIN_POLICY"]), &back); err != nil {
		t.Fatalf("LOGIN_POLICY = %q: %v", kv["LOGIN_POLICY"], err)
	}
	if back.Mail != "dev" || !back.RequireVerifiedEmail {
		t.Errorf("doc = %+v", back)
	}
	in := baseInput()
	in.Refs = []Ref{{EnvName: "IDP_LOGIN_POLICY", SecretName: "forge-identity-identity", SecretKey: "LOGIN_POLICY"}}
	vals, err := StoreValues(in, identity.Output{LoginPolicy: &doc}, func(string) ([]byte, error) { return nil, nil })
	if err != nil || vals["IDP_LOGIN_POLICY"] != kv["LOGIN_POLICY"] {
		t.Errorf("store values = %v, %v", vals, err)
	}
	if _, ok := OutputKeys(identity.Output{})["LOGIN_POLICY"]; ok {
		t.Error("no document: no key (the broker keeps its legacy policy)")
	}
}
