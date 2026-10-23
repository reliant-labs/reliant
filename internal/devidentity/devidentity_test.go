package devidentity

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/identity"
	"github.com/reliant-labs/forge/pkg/release"
)

func hostIdentity() *release.ShapeIdentity {
	return &release.ShapeIdentity{
		Name:    "identity",
		Runtime: release.IdentityRuntimeHost,
		Login:   release.ShapeIdentityLogin{Mode: release.IdentityLoginNative},
		Roles: []release.ShapeIdentityRole{
			{Key: "admin", MembersFrom: release.IdentityMembersDeclared, Members: []string{"ops@acme.test"}},
			{Key: "member", MembersFrom: release.IdentityMembersConsole},
		},
		Applications: []release.ShapeIdentityApplication{
			{Name: "dev:api", Workload: "api", Kind: release.IdentityAppNativeBroker},
		},
		MachineUsers: []release.ShapeIdentityMachineUser{{Name: "ops", Roles: []string{"admin"}}},
		SeedUsers:    []release.ShapeIdentitySeedUser{{Email: "dev@acme.test", Roles: []string{"admin"}, PasswordSecret: "DEV_PASSWORD"}},
	}
}

func baseInput() Input {
	return Input{
		Project:        "acme",
		Identity:       hostIdentity(),
		FrontendOrigin: "http://localhost:3000",
		Secret: func(n string) (string, bool) {
			if n == "DEV_PASSWORD" {
				return "Password1!", true
			}
			return "", false
		},
	}
}

func TestSpec_DerivesEverythingFromTheDeclaration(t *testing.T) {
	spec, _, err := Spec(baseInput())
	if err != nil {
		t.Fatal(err)
	}
	if spec.Project.Name != "acme" {
		t.Errorf("project = %q, want the Bundle's project", spec.Project.Name)
	}
	if len(spec.Roles) != 2 || !spec.Roles[0].Declared || spec.Roles[0].Members[0] != "ops@acme.test" || spec.Roles[1].Declared {
		t.Errorf("roles = %+v: only the declared role is a converged set", spec.Roles)
	}
	app := spec.Applications[0]
	if app.Kind != identity.AppNativeBroker || !app.DevMode {
		t.Errorf("app = %+v, want a dev-mode native broker", app)
	}
	if app.LoginURI != "http://localhost:3000/auth/sign-in" {
		t.Errorf("login uri = %q", app.LoginURI)
	}
	if got := app.RedirectURIs; len(got) != 1 || got[0] != "http://localhost:3000/auth/callback" {
		t.Errorf("redirect uris = %v", got)
	}
	if got := app.PostLogoutURIs; len(got) != 1 || got[0] != "http://localhost:3000" {
		t.Errorf("post logout uris = %v", got)
	}
	if len(spec.MachineUsers) != 1 || !spec.MachineUsers[0].Key || spec.MachineUsers[0].Username != "ops" {
		t.Errorf("machine users = %+v: a declared machine user gets a key in the store", spec.MachineUsers)
	}
	if len(spec.SeedUsers) != 1 || spec.SeedUsers[0].Password != "Password1!" {
		t.Errorf("seed users = %+v: the password comes from the secret store", spec.SeedUsers)
	}
}

func TestSpec_LiteralURIsBeatDerivedOnes(t *testing.T) {
	in := baseInput()
	in.Identity.Applications[0].LoginURI = "http://localhost:4000/signin"
	in.Identity.Applications[0].RedirectURIs = []string{"http://localhost:4000/cb"}
	spec, _, err := Spec(in)
	if err != nil {
		t.Fatal(err)
	}
	a := spec.Applications[0]
	if a.LoginURI != "http://localhost:4000/signin" || a.RedirectURIs[0] != "http://localhost:4000/cb" {
		t.Errorf("literals were overridden: %+v", a)
	}
}

func TestSpec_ProjectNameOverrideAndWorkloadOriginFallback(t *testing.T) {
	in := baseInput()
	in.FrontendOrigin = ""
	in.WorkloadOrigins = map[string]string{"api": "http://localhost:8085"}
	in.Identity.ProjectName = "legacy-name"
	spec, _, err := Spec(in)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Project.Name != "legacy-name" {
		t.Errorf("project = %q", spec.Project.Name)
	}
	if got := spec.Applications[0].LoginURI; got != "http://localhost:8085/auth/sign-in" {
		t.Errorf("with no frontend the broker's own origin carries the routes, got %q", got)
	}
}

func TestSpec_RefusesWhatItCannotDerive(t *testing.T) {
	t.Run("no origin at all", func(t *testing.T) {
		in := baseInput()
		in.FrontendOrigin = ""
		if _, _, err := Spec(in); err == nil || !strings.Contains(err.Error(), "dev:api") {
			t.Fatalf("err = %v, want it to name the application", err)
		}
	})
	t.Run("missing seed password", func(t *testing.T) {
		in := baseInput()
		in.Secret = func(string) (string, bool) { return "", false }
		_, _, err := Spec(in)
		if err == nil || !strings.Contains(err.Error(), "forge secret set --env <env> DEV_PASSWORD") {
			t.Fatalf("err = %v, want the command that fixes it", err)
		}
	})
	t.Run("seed user without a password secret", func(t *testing.T) {
		in := baseInput()
		in.Identity.SeedUsers[0].PasswordSecret = ""
		if _, _, err := Spec(in); err == nil {
			t.Fatal("a seed user with no password source must be refused")
		}
	})
	t.Run("hosted identity", func(t *testing.T) {
		in := baseInput()
		in.Identity.Runtime = release.IdentityRuntimeHosted
		if _, _, err := Spec(in); err == nil {
			t.Fatal("a hosted identity is the control plane's, not the dev loop's")
		}
	})
	t.Run("no identity", func(t *testing.T) {
		in := baseInput()
		in.Identity = nil
		if _, _, err := Spec(in); err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestSpec_SMTPWithoutPasswordWarns(t *testing.T) {
	in := baseInput()
	in.Identity.SMTP = &release.ShapeIdentitySMTP{Host: "localhost", Port: 1025, Sender: "no-reply@acme.test", PasswordSecret: "SMTP_PASSWORD"}
	spec, warnings, err := Spec(in)
	if err != nil {
		t.Fatal(err)
	}
	if spec.SMTP == nil || spec.SMTP.Host != "localhost:1025" {
		t.Errorf("smtp = %+v", spec.SMTP)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "SMTP_PASSWORD") {
		t.Errorf("warnings = %v", warnings)
	}
}

func TestToken_FoldsLikeTheRender(t *testing.T) {
	for in, want := range map[string]string{"dev:api": "DEV_API", "prod-api": "PROD_API", "a..b": "A_B", "ops": "OPS"} {
		if got := Token(in); got != want {
			t.Errorf("Token(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStoreValues_FillsExactlyTheReferencedKeys(t *testing.T) {
	in := baseInput()
	in.Refs = []Ref{
		{EnvName: "JWT_ISSUER", SecretName: "forge-identity-identity", SecretKey: "ISSUER"},
		{EnvName: "JWT_AUDIENCE", SecretName: "forge-identity-identity", SecretKey: "AUDIENCE"},
		{EnvName: "OIDC_CLIENT_ID", SecretName: "forge-identity-identity", SecretKey: "DEV_API_CLIENT_ID"},
		{EnvName: "IDP_BASE", SecretName: "forge-identity-identity", SecretKey: "ISSUER"},
		{EnvName: "IDP_BROKER_TOKEN", SecretName: "forge-identity-identity", SecretKey: "DEV_API_BROKER_TOKEN"},
		{EnvName: "JWT_JWKS_SNAPSHOT", SecretName: "forge-identity-identity", SecretKey: "JWKS"},
		{EnvName: "STRIPE_KEY", SecretName: "stripe", SecretKey: "key"},
	}
	out := identity.Output{
		Issuer: "http://localhost:8080", Audience: "proj-1",
		Scopes: []string{"openid", "profile"},
		Apps: map[string]identity.AppOutput{"dev:api": {
			ClientID: "client-1", RedirectURIs: []string{"http://localhost:3000/auth/callback"},
			BrokerToken: "pat-1",
		}},
	}
	vals, err := StoreValues(in, out, func(user string) ([]byte, error) {
		return []byte(`{"keyId":"k1"}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"JWT_ISSUER": "http://localhost:8080", "JWT_AUDIENCE": "proj-1", "OIDC_CLIENT_ID": "client-1",
		"IDP_BASE": "http://localhost:8080", "IDP_BROKER_TOKEN": "pat-1",
		"OPS_MACHINE_KEY": `{"keyId":"k1"}`,
	}
	for k, v := range want {
		if vals[k] != v {
			t.Errorf("%s = %q, want %q", k, vals[k], v)
		}
	}
	if _, ok := vals["STRIPE_KEY"]; ok {
		t.Error("another Secret's reference was filled from the identity")
	}
	if _, ok := vals["JWT_JWKS_SNAPSHOT"]; ok {
		t.Error("a snapshot the instance did not serve must be absent, not empty")
	}
}

func TestWriteStore_IsIdempotentAndKeepsTheRest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets", "dev.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# my dev secrets\nDATABASE_URL: postgres://x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	written, err := writeStore(path, map[string]string{"IDP_BROKER_TOKEN": "pat-1", "JWT_ISSUER": "http://localhost:8080"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(written, ",") != "IDP_BROKER_TOKEN,JWT_ISSUER" {
		t.Errorf("written = %v", written)
	}
	raw, _ := os.ReadFile(path)
	for _, want := range []string{"# my dev secrets", "DATABASE_URL: postgres://x", "IDP_BROKER_TOKEN: pat-1"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("store lost %q:\n%s", want, raw)
		}
	}
	again, err := writeStore(path, map[string]string{"IDP_BROKER_TOKEN": "pat-1", "JWT_ISSUER": "http://localhost:8080"})
	if err != nil || len(again) != 0 {
		t.Fatalf("a converged store was rewritten: %v %v", again, err)
	}
	rotated, _ := writeStore(path, map[string]string{"IDP_BROKER_TOKEN": "pat-2"})
	if len(rotated) != 1 || rotated[0] != "IDP_BROKER_TOKEN" {
		t.Errorf("a rotated token was not written: %v", rotated)
	}
	// Windows has no POSIX mode bits: Stat reports 0666 whatever was asked.
	if fi, _ := os.Stat(path); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("store mode = %v, want 0600", fi.Mode().Perm())
	}
}
