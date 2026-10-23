package cli

import (
	"path/filepath"
	"strings"
	"testing"

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
