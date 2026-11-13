package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/pkg/release"
)

// The identity goldens (testdata/render_contract/identity-*.json) are the KCL
// render of a managed identity. These tests drive the production consumers from
// them: the strict decoder, the shape projection, and the shape's own
// validation, so a render the control plane could not read fails here.

func identityShapeOf(t *testing.T, golden string) release.Shape {
	t.Helper()
	e, _ := loadContract(t, golden)
	shape, err := bundle.ProjectShape(bundle.ShapeInput{
		Kind:                release.EnvSelfManaged,
		Workloads:           shapeWorkloadsOf(e),
		ControlPlaneSecrets: shapeControlPlaneSecretsOf(e),
		Identity:            shapeIdentityOf(e),
	})
	if err != nil {
		t.Fatalf("%s: ProjectShape: %v", golden, err)
	}
	return shape
}

func TestRenderContract_IdentityGoldensDecodeAndValidate(t *testing.T) {
	for _, name := range []string{"identity-hosted", "identity-cluster", "identity-share-with", "identity-host", "identity-v2-hosted", "identity-v2-host"} {
		t.Run(name, func(t *testing.T) {
			e, _ := loadContract(t, name)
			if e.Identity == nil {
				t.Fatal("the render has no identity")
			}
			shape := identityShapeOf(t, name)
			if err := shape.Validate(); err != nil {
				t.Fatalf("the projected shape is invalid: %v", err)
			}
			// The shape records every delivery as a control-plane secret, so the
			// existing delivery path sees it.
			byKey := map[string]release.ShapeControlPlaneSecret{}
			for _, c := range shape.ControlPlaneSecrets {
				byKey[c.Key()] = c
			}
			for _, d := range shape.Identity.Deliveries {
				c, ok := byKey[d.Cluster+"/"+d.Namespace+"/"+d.Name]
				if !ok || c.Kind != d.Kind {
					t.Errorf("delivery %+v is not among the shape's control-plane secrets", d)
				}
			}
		})
	}
}

func TestRenderContract_IdentityCluster(t *testing.T) {
	e, _ := loadContract(t, "identity-cluster")
	shape := identityShapeOf(t, "identity-cluster")
	id := shape.Identity
	if id.Runtime != release.IdentityRuntimeHosted || id.Login.Mode != release.IdentityLoginNative {
		t.Errorf("runtime/login = %s/%s, want hosted/native", id.Runtime, id.Login.Mode)
	}
	if len(id.Applications) != 1 || id.Applications[0].Name != "prod:api" || id.Applications[0].Workload != "api" {
		t.Errorf("applications = %+v", id.Applications)
	}
	if len(id.Consumers) != 1 || id.Consumers[0].Workload != "api" || id.Consumers[0].Application != "prod:api" {
		t.Errorf("consumers = %+v: the worker is not a consumer", id.Consumers)
	}
	kinds := map[string]bool{}
	for _, c := range shape.ControlPlaneSecrets {
		kinds[c.Kind] = true
		if c.Kind == release.ControlPlaneSecretIdentity && !c.Required {
			t.Errorf("%s must be required: the pods wait for the first delivery", c.Name)
		}
		if c.Kind == release.ControlPlaneSecretIdentityJWKS && c.Required {
			t.Errorf("%s must be optional: a snapshot is a fallback", c.Name)
		}
	}
	if !kinds[release.ControlPlaneSecretIdentity] || !kinds[release.ControlPlaneSecretIdentityJWKS] {
		t.Errorf("kinds = %v, want identity and identity_jwks", kinds)
	}

	// THE PROJECTION: the API's typed config is filled with no hand-written
	// line. The worker, which consumes nothing, is untouched.
	api := e.Workloads[0]
	if api.Name != "api" {
		api = e.Workloads[1]
	}
	env := map[string]string{}
	for _, ev := range api.Spec.Env {
		switch {
		case ev.SecretRef != nil:
			env[ev.Name] = "secret:" + ev.SecretRef.Name + "/" + ev.SecretRef.Key
		default:
			env[ev.Name] = "value:" + ev.Value
		}
	}
	for name, want := range map[string]string{
		"JWT_ISSUER":             "secret:forge-identity-identity/ISSUER",
		"JWT_AUDIENCE":           "secret:forge-identity-identity/AUDIENCE",
		"OIDC_CLIENT_ID":         "secret:forge-identity-identity/PROD_API_CLIENT_ID",
		"OIDC_REDIRECT_URI":      "secret:forge-identity-identity/PROD_API_REDIRECT_URI",
		"IDP_BASE":               "secret:forge-identity-identity/ISSUER",
		"JWT_ROLES_CLAIM":        "value:auto",
		"IDP_BROKER_TOKEN_FILE":  "value:/var/run/secrets/forge/identity/broker_token",
		"JWT_JWKS_SNAPSHOT_FILE": "value:/var/run/secrets/forge/identity-jwks/jwks.json",
	} {
		if env[name] != want {
			t.Errorf("%s = %q, want %q", name, env[name], want)
		}
	}
	if _, plain := env["IDP_BROKER_TOKEN"]; plain {
		t.Error("the broker token is a file on a cluster consumer; a plain env var beside it would pin a rotated token")
	}
}

func TestRenderContract_IdentityHostedLowersToReservedManagedSecrets(t *testing.T) {
	e, _ := loadContract(t, "identity-hosted")
	shape := identityShapeOf(t, "identity-hosted")
	managed := map[string]string{}
	for _, ev := range e.Workloads[0].Spec.Env {
		if ev.ManagedSecret != nil {
			managed[ev.Name] = ev.ManagedSecret.Name
		}
	}
	if managed["JWT_ISSUER"] != "RELIANT_IDENTITY_IDENTITY_ISSUER" {
		t.Errorf("JWT_ISSUER reads %q", managed["JWT_ISSUER"])
	}
	for _, n := range managed {
		if strings.HasPrefix(n, release.IdentityManagedSecretPrefix) {
			found := false
			for _, r := range shape.Identity.ManagedSecrets {
				found = found || r == n
			}
			if !found {
				t.Errorf("%s is read by the workload but not recorded in the shape's managed_secrets", n)
			}
		}
	}
	// Only the machine user's key is delivered to a cluster.
	if len(shape.ControlPlaneSecrets) != 1 || shape.ControlPlaneSecrets[0].Kind != release.ControlPlaneSecretIdentityMachineKey {
		t.Errorf("control-plane secrets = %+v, want only the machine user's key", shape.ControlPlaneSecrets)
	}
	if shape.Identity.SMTP == nil || shape.Identity.SMTP.PasswordSecret != "SMTP_PASSWORD" {
		t.Errorf("smtp = %+v: the password is a NAME", shape.Identity.SMTP)
	}
}

// Required-ness reaches `forge env shape`'s JSON and survives a round trip.
func TestRenderContract_IdentityShapeRoundTrips(t *testing.T) {
	shape := identityShapeOf(t, "identity-cluster")
	b, err := shape.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"required":true`) || !strings.Contains(string(b), `"identity":{`) {
		t.Errorf("encoded shape lacks required/identity: %s", b)
	}
}

// HASH STABILITY (identity v2). The v1 goldens (whose .json the KCL leg,
// TestKCLModule_RenderContract, requires the render to reproduce byte for
// byte) project to shapes whose digests are pinned below. The values were
// computed at forge origin/main e0b2ac43f, BEFORE the v2 fields existed: an
// env that declares none of them must hash exactly as it did, or every env
// reads as changed on the next ensure.
func TestRenderContract_IdentityV1ShapesHashAsBefore(t *testing.T) {
	for name, want := range map[string]string{
		"identity-hosted":     "b62f9caea988787bff96ee80a4e6119baf26ded6b3809351cbc56ac871687676",
		"identity-cluster":    "3bec2be7a357841f4a3c25bb1ba3d425f54913ec640506f256b120ae84dc89b1",
		"identity-share-with": "277e326792ce19136632ad05e3c8bafca1ca779609aa4aa8e46d692ded99f066",
		"identity-host":       "38a35b235cc5477623fbbdb401df7e37d4f91d3a851a169c6c6a3b720c79d972",
	} {
		t.Run(name, func(t *testing.T) {
			shape := identityShapeOf(t, name)
			b, err := shape.Encode()
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(b)
			if got := hex.EncodeToString(sum[:]); got != want {
				t.Errorf("the shape of a v1 identity declaration hashes %s, was %s:\n%s", got, want, b)
			}
			if shape.Identity.DeclaresLoginPolicy() {
				t.Error("a v1 declaration must leave the login policy alone")
			}
			for _, k := range []string{`"owners"`, `"owner_role"`, `"signup"`, `"methods"`, `"passkey_rp_id"`, `"social"`, `"email":{`, `"dev_mail"`, "LOGIN_POLICY"} {
				if strings.Contains(string(b), k) {
					t.Errorf("a v1 declaration's shape carries %s: %s", k, b)
				}
			}
		})
	}
}

// identityEnvRefs is every secret NAME a golden's workloads read: managed
// secrets by name, Secret keys as <secret>/<key>.
func identityEnvRefs(e *KCLEntities) map[string]string {
	out := map[string]string{}
	for _, w := range e.Workloads {
		for _, ev := range w.Spec.Env {
			switch {
			case ev.ManagedSecret != nil:
				out[w.Name+"/"+ev.Name] = ev.ManagedSecret.Name
			case ev.SecretRef != nil:
				out[w.Name+"/"+ev.Name] = ev.SecretRef.Name + "/" + ev.SecretRef.Key
			}
		}
	}
	return out
}

func TestRenderContract_IdentityV2Hosted(t *testing.T) {
	e, _ := loadContract(t, "identity-v2-hosted")
	shape := identityShapeOf(t, "identity-v2-hosted")
	id := shape.Identity
	if !id.DeclaresLoginPolicy() {
		t.Fatal("a declaration with methods, signup and social takes over the login policy")
	}
	if !reflect.DeepEqual(id.Owners, []string{"founder@acme.test"}) || id.OwnerRole != "owner" {
		t.Errorf("owners = %v (%q): lower-cased, with their role", id.Owners, id.OwnerRole)
	}
	if want := (&release.ShapeIdentitySignup{Mode: release.IdentitySignupDomain, Domains: []string{"acme.test"}, Role: "member"}); !reflect.DeepEqual(id.Signup, want) {
		t.Errorf("signup = %+v, want %+v", id.Signup, want)
	}
	if want := []string{release.IdentityMethodMagicLink, release.IdentityMethodPasskey, release.IdentityMethodPassword}; !reflect.DeepEqual(id.Methods, want) {
		t.Errorf("methods = %v, want the canonical (sorted) %v", id.Methods, want)
	}
	if id.PasskeyRPID != "acme.test" || id.DevMail != "" {
		t.Errorf("passkey_rp_id = %q, dev_mail = %q", id.PasskeyRPID, id.DevMail)
	}
	wantSocial := []release.ShapeIdentitySocial{
		{Type: release.IdentitySocialGitHub, ClientIDSecret: "GITHUB_CLIENT_ID", ClientSecretSecret: "GITHUB_CLIENT_SECRET", Scopes: []string{"read:user", "user:email"}},
		{Type: release.IdentitySocialGoogle, ClientID: "1234.apps.googleusercontent.com", ClientSecretSecret: "GOOGLE_CLIENT_SECRET", Scopes: []string{"email", "openid", "profile"}},
	}
	if !reflect.DeepEqual(id.Social, wantSocial) {
		t.Errorf("social = %+v\nwant %+v", id.Social, wantSocial)
	}
	if want := (&release.ShapeIdentityEmail{Delivery: release.IdentityMailPlatform, FromName: "Acme", FromLocal: "no-reply", ReplyTo: "help@acme.test"}); !reflect.DeepEqual(id.Email, want) {
		t.Errorf("email = %+v, want %+v", id.Email, want)
	}

	// THE LOGIN POLICY reaches the broker on both lowerings, optionally.
	var hostedOpt, clusterOpt bool
	for _, w := range e.Workloads {
		for _, ev := range w.Spec.Env {
			if ev.Name != "IDP_LOGIN_POLICY" {
				continue
			}
			switch {
			case ev.ManagedSecret != nil:
				hostedOpt = ev.ManagedSecret.Name == "RELIANT_IDENTITY_IDENTITY_"+release.IdentityLoginPolicyKey && ev.ManagedSecret.Optional
			case ev.SecretRef != nil:
				clusterOpt = ev.SecretRef.Name == "forge-identity-identity" && ev.SecretRef.Key == release.IdentityLoginPolicyKey && ev.SecretRef.Optional
			}
		}
	}
	if !hostedOpt || !clusterOpt {
		t.Errorf("IDP_LOGIN_POLICY must be an OPTIONAL reference on both runtimes: hosted=%t cluster=%t", hostedOpt, clusterOpt)
	}
	found := false
	for _, n := range id.ManagedSecrets {
		found = found || n == "RELIANT_IDENTITY_IDENTITY_LOGIN_POLICY"
	}
	if !found {
		t.Errorf("managed_secrets %v lack the login policy", id.ManagedSecrets)
	}
	for _, c := range shape.ControlPlaneSecrets {
		if c.Kind == release.ControlPlaneSecretIdentity {
			found := false
			for _, k := range c.Keys {
				found = found || k == release.IdentityLoginPolicyKey
			}
			if !found {
				t.Errorf("the delivered Secret %s does not carry %s: %v", c.Name, release.IdentityLoginPolicyKey, c.Keys)
			}
		}
	}

	// SOCIAL SECRETS NEVER REACH A POD.
	for where, ref := range identityEnvRefs(e) {
		for _, s := range id.Social {
			if strings.Contains(ref, s.ClientSecretSecret) || (s.ClientIDSecret != "" && strings.Contains(ref, s.ClientIDSecret)) {
				t.Errorf("%s reads %s, a social provider's secret", where, ref)
			}
		}
	}

	// A control plane that predates the sign-in fields is sent the v1 shape.
	limited := (&release.BundleReader{}).LimitShape(shape)
	if limited.Identity.DeclaresLoginPolicy() || limited.Identity.Owners != nil || limited.Identity.Email != nil {
		t.Errorf("an old reader was sent v2 fields: %+v", limited.Identity)
	}
	if err := limited.Validate(); err != nil {
		t.Errorf("the limited shape is invalid: %v", err)
	}
}

func TestRenderContract_IdentityV2Host(t *testing.T) {
	e, _ := loadContract(t, "identity-v2-host")
	id := identityShapeOf(t, "identity-v2-host").Identity
	if id.Runtime != release.IdentityRuntimeHost || id.DevMail != "mail" || !id.MailAvailable() {
		t.Errorf("runtime = %s, dev_mail = %q: the env's only mailpit is the identity's mail", id.Runtime, id.DevMail)
	}
	if id.SignupMode() != release.IdentitySignupOpen || len(id.Social) != 1 {
		t.Errorf("signup = %s, social = %+v", id.SignupMode(), id.Social)
	}
	refs := identityEnvRefs(e)
	if refs["api/IDP_LOGIN_POLICY"] != "forge-identity-identity/"+release.IdentityLoginPolicyKey {
		t.Errorf("the dev broker reads the login policy from %q", refs["api/IDP_LOGIN_POLICY"])
	}
	for where, ref := range refs {
		if strings.Contains(ref, "GOOGLE_CLIENT_SECRET") {
			t.Errorf("%s reads the Google client secret", where)
		}
	}
}
