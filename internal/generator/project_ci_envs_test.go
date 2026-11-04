package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

func writeEnvMain(t *testing.T, dir string, envs ...string) {
	t.Helper()
	for _, env := range envs {
		d := filepath.Join(dir, "deploy", "kcl", env)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "main.k"), []byte("manifests = []\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// deploy.yml targets the envs the project DECLARES — never a hard-coded
// staging/prod pair.
//
// houndersclub (dev + a hosted prod, no staging) was scaffolded with a
// deploy-staging job wired to workflow_run after every image build on main: a
// job for an env with no deploy/kcl/staging/main.k, red on every merge.
func TestCIFiles_DeployTargetsDeclaredEnvsOnly(t *testing.T) {
	g, dir := ciGenerator(t, config.FeaturesConfig{})
	writeEnvMain(t, dir, "dev", "prod")
	if err := g.generateCIFiles(); err != nil {
		t.Fatalf("generateCIFiles: %v", err)
	}
	deploy := readCIFile(t, filepath.Join(dir, ".github", "workflows", "deploy.yml"))
	if strings.Contains(deploy, "staging") {
		t.Error("deploy.yml targets staging, which this project does not declare")
	}
	if !strings.Contains(deploy, "- env: prod") {
		t.Error("deploy.yml does not target prod, which this project declares")
	}
	if strings.Contains(deploy, "- env: dev") {
		t.Error("deploy.yml targets dev, which runs locally via `forge env up`")
	}
	// A lone env is production: never auto-deployed on a merge to main.
	if !strings.Contains(deploy, "auto: false") {
		t.Errorf("a lone prod env is auto-deployed on every merge to main:\n%s", deploy)
	}
}

// Discovered envs are ordered along the PROMOTION path, not lexically: qa is
// a pre-production stage, so it follows staging, and prod is always last.
// (The scaffold path used to sort alphabetically, which auto-deployed qa
// ahead of staging; the generate path already ranked them. One mapper now
// answers for both.)
func TestCIDeployEnvs_PromotionOrder(t *testing.T) {
	dir := t.TempDir()
	writeEnvMain(t, dir, "dev", "prod", "staging", "qa")
	envs := ciDeployEnvs(serviceCfg(), declaredKCLEnvs(dir))
	var names []string
	for _, e := range envs {
		names = append(names, e.Name)
	}
	if got := strings.Join(names, ","); got != "staging,qa,prod" {
		t.Fatalf("envs = %s, want staging,qa,prod (dev excluded, promotion order, prod last)", got)
	}
	if !envs[0].Auto || envs[0].Protection {
		t.Errorf("first env must auto-deploy and be unprotected: %+v", envs[0])
	}
	if last := envs[len(envs)-1]; last.Auto || !last.Protection {
		t.Errorf("prod must be protected and never auto-deployed: %+v", last)
	}
	if got := ciDeployEnvs(serviceCfg(), declaredKCLEnvs(t.TempDir())); len(got) != 0 {
		t.Errorf("a project with no deploy/kcl has no deploy envs, got %+v", got)
	}
}

// A lone cloud env (houndersclub: dev + prod) IS production: protected, and
// never auto-deployed on a merge to main.
func TestCIDeployEnvs_LoneEnvIsNeverAuto(t *testing.T) {
	dir := t.TempDir()
	writeEnvMain(t, dir, "dev", "prod")
	envs := ciDeployEnvs(serviceCfg(), declaredKCLEnvs(dir))
	if len(envs) != 1 || envs[0].Name != "prod" || envs[0].Auto || !envs[0].Protection {
		t.Fatalf("a lone prod env must be protected and never auto-deployed: %+v", envs)
	}
}

// The default scaffold (dev/prod/staging) must auto-deploy staging and gate
// prod — ListEnvs order is alphabetical, which put PROD first.
func TestCIDeployEnvs_DefaultScaffoldStagingThenProd(t *testing.T) {
	dir := t.TempDir()
	writeEnvMain(t, dir, "dev", "prod", "staging")
	envs := ciDeployEnvs(serviceCfg(), declaredKCLEnvs(dir))
	if len(envs) != 2 {
		t.Fatalf("want 2 cloud envs (dev is local-only), got %+v", envs)
	}
	if first := envs[0]; first.Name != "staging" || !first.Auto || first.Protection {
		t.Fatalf("first env must be staging, auto-deployed and unprotected, got %+v", first)
	}
	if last := envs[1]; last.Name != "prod" || last.Auto || !last.Protection {
		t.Fatalf("last env must be prod, protected and never auto-deployed, got %+v", last)
	}
}
