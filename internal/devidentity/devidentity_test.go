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

// A seed user that cannot be created is a warning, never the end of the pass:
// the application, broker and machine users still converge, because the app's
// sign-in routes are mounted from them.
func TestSpec_SeedUserFailureIsNotFatal(t *testing.T) {
	t.Run("missing seed password", func(t *testing.T) {
		in := baseInput()
		in.Secret = func(string) (string, bool) { return "", false }
		spec, warnings, err := Spec(in)
		if err != nil {
			t.Fatalf("err = %v: one seed user must not fail the whole identity", err)
		}
		if len(spec.SeedUsers) != 0 {
			t.Errorf("seed users = %+v, want the one without a password left out", spec.SeedUsers)
		}
		if len(spec.Applications) != 1 || len(spec.MachineUsers) != 1 {
			t.Errorf("applications = %+v, machine users = %+v: the rest must still converge", spec.Applications, spec.MachineUsers)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "dev@acme.test") ||
			!strings.Contains(warnings[0], "forge secret set --env <env> DEV_PASSWORD") {
			t.Fatalf("warnings = %v, want one naming the seed user and the command that fixes it", warnings)
		}
	})
	t.Run("seed user without a password secret", func(t *testing.T) {
		in := baseInput()
		in.Identity.SeedUsers[0].PasswordSecret = ""
		spec, warnings, err := Spec(in)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(spec.SeedUsers) != 0 || len(spec.Applications) != 1 {
			t.Errorf("seed users = %+v, applications = %+v", spec.SeedUsers, spec.Applications)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "password_secret") {
			t.Fatalf("warnings = %v, want one asking for password_secret", warnings)
		}
	})
}

func storeInput(t *testing.T) Input {
	t.Helper()
	in := baseInput()
	in.StorePath = filepath.Join(t.TempDir(), "secrets", "dev.yaml")
	in.Secret = func(string) (string, bool) { return "", false }
	return in
}

func TestEnsureSeedPasswords_GeneratesIntoTheStore(t *testing.T) {
	in := storeInput(t)
	if err := os.MkdirAll(filepath.Dir(in.StorePath), 0o700); err != nil {
		t.Fatal(err)
	}
	// The scaffolded store: a header, an empty slot, a developer's value.
	if err := os.WriteFile(in.StorePath, []byte("# keep me\nDEV_PASSWORD: \"\"\nSTRIPE_KEY: sk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	in.Identity.SeedUsers = append(in.Identity.SeedUsers,
		release.ShapeIdentitySeedUser{Email: "ops@acme.test", PasswordSecret: "DEV_PASSWORD"})

	got, made, warnings := ensureSeedPasswords(in)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if len(made) != 2 || made[0] != (SeedPassword{Email: "dev@acme.test", Secret: "DEV_PASSWORD"}) || made[1].Email != "ops@acme.test" {
		t.Fatalf("made = %+v, want both seed users reported against DEV_PASSWORD", made)
	}
	stored, err := secretFile{Path: in.StorePath}.get("DEV_PASSWORD")
	if err != nil || stored == "" {
		t.Fatalf("store holds %q, err %v", stored, err)
	}
	if !policyCompliant(stored) {
		t.Errorf("generated password %q does not meet the default complexity policy", stored)
	}
	raw, _ := os.ReadFile(in.StorePath)
	if !strings.Contains(string(raw), "# keep me") || !strings.Contains(string(raw), "STRIPE_KEY: sk") {
		t.Errorf("the store lost its comment or another key:\n%s", raw)
	}

	spec, warnings, err := Spec(got)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("spec err %v warnings %v", err, warnings)
	}
	if len(spec.SeedUsers) != 2 || spec.SeedUsers[0].Password != stored || spec.SeedUsers[1].Password != stored {
		t.Fatalf("seed users = %+v: both must get the generated password", spec.SeedUsers)
	}

	// The next pass resolves it from the store and generates nothing.
	again := storeInput(t)
	again.StorePath = in.StorePath
	again.Secret = func(n string) (string, bool) {
		v, _ := secretFile{Path: in.StorePath}.get(n)
		return v, v != ""
	}
	if _, made, _ := ensureSeedPasswords(again); len(made) != 0 {
		t.Fatalf("second pass generated %+v", made)
	}
	if v, _ := (secretFile{Path: in.StorePath}).get("DEV_PASSWORD"); v != stored {
		t.Fatal("second pass replaced the password")
	}
}

// A store created 0644 (by hand, or by an older scaffold) is narrowed to 0600
// BEFORE the generated password is written: atomicfile keeps the existing
// mode for its temp file, so a chmod after the write would leave the password
// world-readable until it ran.
func TestEnsureSeedPasswords_RestrictsALooseStoreBeforeWriting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes do not apply on Windows")
	}
	in := storeInput(t)
	if err := os.MkdirAll(filepath.Dir(in.StorePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(in.StorePath, []byte("DEV_PASSWORD: \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(in.StorePath, 0o644); err != nil { // umask-proof
		t.Fatal(err)
	}
	var modeAtWrite os.FileMode
	orig := writeStoreFile
	t.Cleanup(func() { writeStoreFile = orig })
	writeStoreFile = func(path string, data []byte, perm os.FileMode) error {
		if fi, err := os.Stat(path); err == nil {
			modeAtWrite = fi.Mode().Perm()
		}
		return orig(path, data, perm)
	}

	_, made, warnings := ensureSeedPasswords(in)
	if len(warnings) != 0 || len(made) != 1 {
		t.Fatalf("made %+v, warnings %v", made, warnings)
	}
	if modeAtWrite != 0o600 {
		t.Errorf("store mode when the password was written = %v, want 0600", modeAtWrite)
	}
	if fi, _ := os.Stat(in.StorePath); fi.Mode().Perm() != 0o600 {
		t.Errorf("store mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestEnsureSeedPasswords_NeverReplacesAStoredValue(t *testing.T) {
	in := storeInput(t)
	// The resolver missed it (a stale snapshot), but the store has it.
	if err := (secretFile{Path: in.StorePath}).set("DEV_PASSWORD", "Mine-Password1"); err != nil {
		t.Fatal(err)
	}
	got, made, _ := ensureSeedPasswords(in)
	if len(made) != 0 {
		t.Fatalf("made = %+v", made)
	}
	if v, _ := got.Secret("DEV_PASSWORD"); v != "Mine-Password1" {
		t.Fatalf("resolved %q, want the stored value", v)
	}
	if v, _ := (secretFile{Path: in.StorePath}).get("DEV_PASSWORD"); v != "Mine-Password1" {
		t.Fatalf("store holds %q", v)
	}
}

func TestEnsureSeedPasswords_OnlyForAHostRunIdentityWithALocalStore(t *testing.T) {
	t.Run("hosted identity", func(t *testing.T) {
		in := storeInput(t)
		in.Identity.Runtime = release.IdentityRuntimeHosted
		if _, made, _ := ensureSeedPasswords(in); len(made) != 0 {
			t.Fatalf("generated %+v for a hosted identity", made)
		}
		if _, err := os.Stat(in.StorePath); err == nil {
			t.Fatal("wrote a store for a hosted identity")
		}
	})
	t.Run("no local store", func(t *testing.T) {
		in := storeInput(t)
		in.StorePath = ""
		if _, made, _ := ensureSeedPasswords(in); len(made) != 0 {
			t.Fatalf("generated %+v with no store to keep it in", made)
		}
	})
	t.Run("already resolved", func(t *testing.T) {
		in := storeInput(t)
		in.Secret = baseInput().Secret
		if _, made, _ := ensureSeedPasswords(in); len(made) != 0 {
			t.Fatalf("generated %+v over a value the store already has", made)
		}
	})
}

func TestEnsureSeedPasswords_UnwritableStoreWarns(t *testing.T) {
	in := storeInput(t)
	if err := os.MkdirAll(in.StorePath, 0o700); err != nil { // a directory, not a file
		t.Fatal(err)
	}
	got, made, warnings := ensureSeedPasswords(in)
	if len(made) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], "dev@acme.test") {
		t.Fatalf("made %+v warnings %v, want one warning naming the seed user", made, warnings)
	}
	if _, _, err := Spec(got); err != nil {
		t.Fatalf("spec err %v: the seed user is left out, not fatal", err)
	}
}

func policyCompliant(pw string) bool {
	var upper, lower, digit, symbol bool
	for _, r := range pw {
		switch {
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= '0' && r <= '9':
			digit = true
		default:
			symbol = true
		}
	}
	return len(pw) >= 8 && len(pw) <= 72 && upper && lower && digit && symbol
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
