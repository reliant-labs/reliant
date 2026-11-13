package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/devidentity"
	"github.com/reliant-labs/forge/internal/hostinfra"
	"github.com/reliant-labs/forge/internal/secrets"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
	"github.com/reliant-labs/forge/pkg/release"
)

func identityEntities() *KCLEntities {
	ports := []int{8085}
	api := WorkloadEntity{Name: "api", Kind: "service", Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{ListenPorts: &ports}}}
	api.Spec.Env = []deployv1alpha1.EnvVar{
		{Name: "JWT_ISSUER", SecretRef: &deployv1alpha1.SecretKeyRef{Name: "forge-identity-identity", Key: "ISSUER"}},
		{Name: "IDP_BROKER_TOKEN", SecretRef: &deployv1alpha1.SecretKeyRef{Name: "forge-identity-identity", Key: "DEV_API_BROKER_TOKEN"}},
		{Name: "DATABASE_URL", SecretRef: &deployv1alpha1.SecretKeyRef{Name: "db", Key: "DATABASE_URL"}},
	}
	// A cluster workload's identity refs are the control plane's, not the dev loop's.
	pod := WorkloadEntity{Name: "pod", Kind: "service", Runtime: RuntimeEntity{Type: RuntimeCluster}}
	pod.Spec.Env = []deployv1alpha1.EnvVar{
		{Name: "JWT_AUDIENCE", SecretRef: &deployv1alpha1.SecretKeyRef{Name: "forge-identity-identity", Key: "AUDIENCE"}},
	}
	return &KCLEntities{
		Project: "acme", Env: "dev",
		Workloads: []WorkloadEntity{api, pod},
		Infra: []HostInfraEntity{
			{Name: "pg", Engine: "postgres", Port: 5432},
			{Name: "idp", Engine: "zitadel", Port: 8080, IDPPATPath: ".forge/idp/pat.txt"},
		},
		Frontends:      []FrontendEntity{{Name: "web", Port: 3000, BasePath: "/app/"}},
		SecretProvider: &SecretProviderEntity{Type: "file", Path: "secrets/dev.yaml"},
		Identity: &release.ShapeIdentity{
			Name: "identity", Runtime: release.IdentityRuntimeHost,
			Applications: []release.ShapeIdentityApplication{{Name: "dev:api", Workload: "api", Kind: release.IdentityAppNativeBroker}},
		},
	}
}

func TestHostIdentity_OnlyAHostRuntimeIdentity(t *testing.T) {
	e := identityEntities()
	if hostIdentity(e) == nil {
		t.Fatal("a host-run identity must be found")
	}
	e.Identity.Runtime = release.IdentityRuntimeHosted
	if hostIdentity(e) != nil {
		t.Error("a hosted identity is the control plane's to converge")
	}
	if hostIdentity(nil) != nil || hostIdentity(&KCLEntities{}) != nil {
		t.Error("no identity means no step")
	}
}

// The launch pre-flight demands every declared secret from the store, but the
// identity's own refs cannot exist before the IdP is converged: asking for
// them would make the very first `forge env up` fail on values it is about to
// write.
func TestWithoutHostIdentityRefs_KeepsEveryOtherRef(t *testing.T) {
	e := identityEntities()
	refs := secretRefsForLaunch(e)
	if len(refs) != 3 {
		t.Fatalf("fixture: %d refs", len(refs))
	}
	got := withoutHostIdentityRefs(e, refs)
	if len(got) != 1 || got[0].EnvName != "DATABASE_URL" {
		t.Fatalf("refs = %+v, want only DATABASE_URL", got)
	}
	e.Identity = nil
	if len(withoutHostIdentityRefs(e, refs)) != 3 {
		t.Error("with no host identity nothing is exempt")
	}
}

func TestDevIdentityInput_ResolvesTheEnv(t *testing.T) {
	e := identityEntities()
	dir := t.TempDir()
	prov := secrets.NewPulledProvider(map[string]string{"DEV_PASSWORD": "pw"})
	in, err := devIdentityInput(e, e.Identity, dir, prov)
	if err != nil {
		t.Fatal(err)
	}
	if in.BaseURL != "http://localhost:8080" {
		t.Errorf("base = %q: the IdP is dialled at the port the env declares", in.BaseURL)
	}
	if want := filepath.Join(dir, ".forge", "idp", "pat.txt"); in.PATPath != want {
		t.Errorf("pat = %q, want %q", in.PATPath, want)
	}
	if want := filepath.Join(dir, "secrets", "dev.yaml"); in.StorePath != want {
		t.Errorf("store = %q, want this checkout's own store %q", in.StorePath, want)
	}
	if want := filepath.Join(dir, ".forge", "identity", "identity.json"); in.OutputsPath != want {
		t.Errorf("outputs = %q, want %q", in.OutputsPath, want)
	}
	if in.FrontendOrigin != "http://localhost:3000/app" {
		t.Errorf("frontend origin = %q: the base path belongs in the registered routes", in.FrontendOrigin)
	}
	if in.WorkloadOrigins["api"] != "http://localhost:8085" {
		t.Errorf("workload origins = %v", in.WorkloadOrigins)
	}
	if len(in.Refs) != 2 {
		t.Errorf("refs = %+v, want the host workload's two identity refs", in.Refs)
	}
	if v, ok := in.Secret("DEV_PASSWORD"); !ok || v != "pw" {
		t.Errorf("secret lookup = %q %v", v, ok)
	}
	if _, ok := in.Secret("ABSENT"); ok {
		t.Error("an absent secret must not resolve")
	}
}

func TestDevIdentityInput_RefusesWhatItCannotRunOn(t *testing.T) {
	t.Run("no zitadel infra", func(t *testing.T) {
		e := identityEntities()
		e.Infra = e.Infra[:1]
		_, err := devIdentityInput(e, e.Identity, t.TempDir(), nil)
		if err == nil || !strings.Contains(err.Error(), "zitadel") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("two zitadels", func(t *testing.T) {
		e := identityEntities()
		e.Infra = append(e.Infra, HostInfraEntity{Name: "idp2", Engine: "zitadel", Port: 8081})
		if _, err := devIdentityInput(e, e.Identity, t.TempDir(), nil); err == nil {
			t.Fatal("ambiguous infra must be refused")
		}
	})
	t.Run("no file secret store", func(t *testing.T) {
		e := identityEntities()
		e.SecretProvider = nil
		_, err := devIdentityInput(e, e.Identity, t.TempDir(), nil)
		if err == nil || !strings.Contains(err.Error(), "FileSecrets") {
			t.Fatalf("err = %v, want the declaration that fixes it", err)
		}
	})
}

func TestDevIdentityInput_FindsTheEnvsMailpit(t *testing.T) {
	e := identityEntities()
	if in, err := devIdentityInput(e, e.Identity, t.TempDir(), nil); err != nil || in.MailpitPort != 0 {
		t.Fatalf("no mailpit: port %d, %v", in.MailpitPort, err)
	}
	e.Infra = append(e.Infra, HostInfraEntity{Name: "mail", Engine: "mailpit", Port: 1025})
	if in, err := devIdentityInput(e, e.Identity, t.TempDir(), nil); err != nil || in.MailpitPort != 1025 {
		t.Fatalf("the only mailpit: port %d, %v", in.MailpitPort, err)
	}
	e.Infra = append(e.Infra, HostInfraEntity{Name: "mail2", Engine: "mailpit", Port: 1026})
	if _, err := devIdentityInput(e, e.Identity, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "IdentityOnHost {mail") {
		t.Fatalf("two mailpits and no name: %v", err)
	}
	e.Identity.DevMail = "mail2"
	if in, err := devIdentityInput(e, e.Identity, t.TempDir(), nil); err != nil || in.MailpitPort != 1026 {
		t.Fatalf("the named mailpit: port %d, %v", in.MailpitPort, err)
	}
	e.Identity.DevMail = "pg"
	if _, err := devIdentityInput(e, e.Identity, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "not a forge.HostInfra with engine \"mailpit\"") {
		t.Fatalf("a named non-mailpit: %v", err)
	}
}

// The bootstrap PAT belongs to the IdP database, so it is looked for beside
// the database first and in the checkout second. A token that authenticated
// keeps the two homes coherent: one from the shared file refreshes the
// checkout's copy, and one from the checkout (an older forge's database) is
// published for every other checkout. A token that did not authenticate is
// never published.
func TestConvergeWithIDPPAT(t *testing.T) {
	prev := convergeDevIdentityFn
	t.Cleanup(func() { convergeDevIdentityFn = prev })
	var tried []string
	convergeDevIdentityFn = func(_ context.Context, in devidentity.Input) (*devidentity.Result, error) {
		b, _ := os.ReadFile(in.PATPath)
		tried = append(tried, string(b))
		if string(b) != "good" {
			return nil, errors.New("401 Unauthenticated")
		}
		return &devidentity.Result{}, nil
	}
	setup := func(t *testing.T, shared, checkout string) hostinfra.IDPPATLocations {
		t.Helper()
		tried = nil
		dir := t.TempDir()
		loc := hostinfra.IDPPATLocations{Shared: filepath.Join(dir, "home", "pat.txt"), Checkout: filepath.Join(dir, "clone", "pat.txt")}
		for p, body := range map[string]string{loc.Shared: shared, loc.Checkout: checkout} {
			if body == "" {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return loc
	}
	content := func(p string) string { b, _ := os.ReadFile(p); return string(b) }

	t.Run("a fresh clone of a shared database uses the shared token and gets a copy", func(t *testing.T) {
		loc := setup(t, "good", "")
		if _, _, err := convergeWithIDPPAT(t.Context(), devidentity.Input{}, loc); err != nil {
			t.Fatal(err)
		}
		if content(loc.Checkout) != "good" {
			t.Error("the checkout's .forge/idp/pat.txt must be refreshed for anything that reads it")
		}
	})

	t.Run("an older forge's database: the checkout token is used, then published", func(t *testing.T) {
		loc := setup(t, "", "good")
		_, notes, err := convergeWithIDPPAT(t.Context(), devidentity.Input{}, loc)
		if err != nil {
			t.Fatal(err)
		}
		if content(loc.Shared) != "good" {
			t.Error("a token that authenticated must be published beside the database")
		}
		if len(notes) != 1 || !strings.Contains(notes[0], "published") {
			t.Errorf("notes = %q, want one saying it was published", notes)
		}
	})

	t.Run("a refused shared token falls back to a different checkout token", func(t *testing.T) {
		loc := setup(t, "stale", "good")
		if _, _, err := convergeWithIDPPAT(t.Context(), devidentity.Input{}, loc); err != nil {
			t.Fatal(err)
		}
		if strings.Join(tried, ",") != "stale,good" {
			t.Errorf("tried %q, want the shared token then the checkout's", tried)
		}
		if content(loc.Shared) != "good" {
			t.Error("the token that authenticated must replace the refused shared one")
		}
	})

	t.Run("a refused token is never published", func(t *testing.T) {
		loc := setup(t, "", "stale")
		if _, _, err := convergeWithIDPPAT(t.Context(), devidentity.Input{}, loc); err == nil {
			t.Fatal("want the convergence error")
		}
		if _, err := os.Stat(loc.Shared); !errors.Is(err, os.ErrNotExist) {
			t.Error("a token that did not authenticate must not reach the shared file every checkout trusts")
		}
	})

	t.Run("no token anywhere", func(t *testing.T) {
		loc := setup(t, "", "")
		if _, _, err := convergeWithIDPPAT(t.Context(), devidentity.Input{}, loc); !errors.Is(err, errNoIDPPAT) {
			t.Fatalf("err = %v, want errNoIDPPAT", err)
		}
		if len(tried) != 0 {
			t.Error("nothing to authenticate with: the IdP must not be called")
		}
	})
}

// The failure this replaces said only "read the personal access token from
// <this checkout>/.forge/idp/pat.txt: no such file or directory". The message
// must name the database, every place forge looked, and both ways out.
func TestMissingIDPPATMessage(t *testing.T) {
	z := &HostInfraEntity{Name: "idp", Engine: "zitadel", IDPDatabase: "zitadel_idp", IDPDatabasePort: 5434, User: "postgres"}
	loc := hostinfra.IDPPATLocations{Shared: "/home/me/.forge/idp/localhost_5434_zitadel_idp/pat.txt", Checkout: "/src/clone/.forge/idp/pat.txt"}
	msg := missingIDPPATMessage("identity", z, loc, "dev")
	for _, want := range []string{
		`"zitadel_idp" on localhost:5434`,
		loc.Shared + ", " + loc.Checkout,
		"run `forge env up dev` once in the checkout that initialized it",
		`psql -h localhost -p 5434 -U postgres -d postgres -c 'DROP DATABASE "zitadel_idp"'`,
		"deletes the dev IdP's users",
		"sign-in is unavailable this run",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
	custom := missingIDPPATMessage("identity", z, hostinfra.IDPPATLocations{Checkout: "/src/clone/secrets/pat.txt"}, "dev")
	if !strings.Contains(custom, "copy the token from the checkout that initialized it to /src/clone/secrets/pat.txt") {
		t.Errorf("a declared path is the only location, so the fix names it:\n%s", custom)
	}
}
