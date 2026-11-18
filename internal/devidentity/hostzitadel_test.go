package devidentity

// The real thing: host postgres and the pinned Zitadel binary, started through
// hostinfra.Start exactly as `forge env up` starts them, then converged by
// Converge. The unit tests prove the derivation; this proves the instance
// accepts it, that a second pass writes nothing, and that the broker token it
// stored really signs a seed user in.
//
// Skipped under -short: it boots two servers (the first run on a machine also
// downloads the Zitadel release into the user cache).

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/hostinfra"
	"github.com/reliant-labs/forge/internal/secrets"
	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/pkg/devidp"
	"github.com/reliant-labs/forge/pkg/identity"
	"github.com/reliant-labs/forge/pkg/release"
)

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func TestConverge_AgainstTheRealHostZitadel(t *testing.T) {
	if testing.Short() {
		t.Skip("boots host postgres and the pinned Zitadel; runs in task test")
	}
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

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
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
	if err := hostinfra.Start(ctx, dir, pgSpec); err != nil {
		t.Skipf("host postgres is unavailable here: %v", err)
	}
	if err := hostinfra.Start(ctx, dir, idpSpec); err != nil {
		t.Skipf("host Zitadel is unavailable here: %v", err)
	}

	const frontend = "http://localhost:3000"
	sh := &release.ShapeIdentity{
		Name:    "identity",
		Runtime: release.IdentityRuntimeHost,
		Login:   release.ShapeIdentityLogin{Mode: release.IdentityLoginNative},
		Roles: []release.ShapeIdentityRole{
			{Key: "admin", MembersFrom: release.IdentityMembersConsole},
			{Key: "member", MembersFrom: release.IdentityMembersConsole},
		},
		Applications: []release.ShapeIdentityApplication{{Name: "dev:api", Workload: "api", Kind: release.IdentityAppNativeBroker}},
		MachineUsers: []release.ShapeIdentityMachineUser{{Name: "ops", Roles: []string{"admin"}}},
		SeedUsers:    []release.ShapeIdentitySeedUser{{Email: "dev@acme.test", Roles: []string{"admin"}, PasswordSecret: "DEV_PASSWORD"}},
	}
	store := filepath.Join(dir, "secrets", "dev.yaml")
	in := Input{
		Project:        "acme",
		Identity:       sh,
		BaseURL:        "http://localhost:" + itoa(idpSpec.Port),
		PATPath:        idpSpec.PATPath(dir),
		FrontendOrigin: frontend,
		Secret: func(n string) (string, bool) {
			if n == "DEV_PASSWORD" {
				return "Password1!", true
			}
			return "", false
		},
		Refs: []Ref{
			{EnvName: "JWT_ISSUER", SecretName: "forge-identity-identity", SecretKey: "ISSUER"},
			{EnvName: "JWT_AUDIENCE", SecretName: "forge-identity-identity", SecretKey: "AUDIENCE"},
			{EnvName: "OIDC_CLIENT_ID", SecretName: "forge-identity-identity", SecretKey: "DEV_API_CLIENT_ID"},
			{EnvName: "OIDC_REDIRECT_URI", SecretName: "forge-identity-identity", SecretKey: "DEV_API_REDIRECT_URI"},
			{EnvName: "IDP_BASE", SecretName: "forge-identity-identity", SecretKey: "ISSUER"},
			{EnvName: "IDP_BROKER_TOKEN", SecretName: "forge-identity-identity", SecretKey: "DEV_API_BROKER_TOKEN"},
		},
		StorePath:   store,
		OutputsPath: filepath.Join(dir, ".forge", "identity", "identity.json"),
		KeysDir:     filepath.Join(dir, ".forge", "identity", "identity.keys"),
	}

	first, err := Converge(ctx, in)
	if err != nil {
		t.Fatalf("first converge: %v", err)
	}
	if !first.Report.Changed() {
		t.Fatal("a fresh instance converged without a single change")
	}

	// The non-secret outputs.
	var out identity.Output
	raw, err := os.ReadFile(in.OutputsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Issuer != in.BaseURL || out.Audience == "" {
		t.Errorf("outputs = issuer %q audience %q", out.Issuer, out.Audience)
	}
	app := out.Apps["dev:api"]
	if app.ClientID == "" || app.LoginURI != frontend+"/auth/sign-in" {
		t.Errorf("app = %+v", app)
	}
	if strings.Contains(string(raw), "pat") && strings.Contains(string(raw), app.BrokerToken) && app.BrokerToken != "" {
		t.Error("the broker token leaked into the non-secret outputs file")
	}

	// The secrets, under the names a host process reads.
	vals, err := secrets.ReadSecretFile(store)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"JWT_ISSUER": in.BaseURL, "IDP_BASE": in.BaseURL, "JWT_AUDIENCE": out.Audience,
		"OIDC_CLIENT_ID": app.ClientID, "OIDC_REDIRECT_URI": frontend + "/auth/callback",
	} {
		if vals[k] != want {
			t.Errorf("store %s = %q, want %q", k, vals[k], want)
		}
	}
	token := vals["IDP_BROKER_TOKEN"]
	if token == "" {
		t.Fatal("no broker token reached the secret store")
	}
	if !strings.Contains(vals["OPS_MACHINE_KEY"], "keyId") {
		t.Errorf("the machine user's key is not in the store: %q", vals["OPS_MACHINE_KEY"])
	}
	if len(first.StoreKeys) == 0 {
		t.Error("the result reports no store writes")
	}

	// The token and client the app would run with really sign the seed user in.
	client := &devidp.Client{Base: in.BaseURL, Token: token}
	toks, err := client.SignIn(ctx, devidp.FlowConfig{
		ClientID: app.ClientID, RedirectURI: vals["OIDC_REDIRECT_URI"], Scopes: out.Scopes,
	}, devidp.Credentials{LoginName: "dev@acme.test", Password: "Password1!"})
	if err != nil {
		t.Fatalf("sign in with the stored broker token: %v", err)
	}
	if strings.Count(toks.AccessToken, ".") != 2 {
		t.Errorf("access token is not a JWT (opaque tokens never validate): %.20s…", toks.AccessToken)
	}

	// A second pass: nothing to write, nothing rotated, no store churn.
	second, err := Converge(ctx, in)
	if err != nil {
		t.Fatalf("second converge: %v", err)
	}
	if second.Report.Changed() {
		t.Errorf("a converged instance was written to again: %v", second.Report.Changes)
	}
	if len(second.StoreKeys) != 0 {
		t.Errorf("a converged store was rewritten: %v", second.StoreKeys)
	}
	again, _ := secrets.ReadSecretFile(store)
	if again["IDP_BROKER_TOKEN"] != token {
		t.Error("the broker token changed on an idempotent pass")
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
