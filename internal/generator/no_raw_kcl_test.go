package generator

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// Nothing forge scaffolds renders KCL behind forge's back.
//
// An env's main.k imports `forge`, a KCL module the forge binary INJECTS at
// render time (and `kcl_plugin.forge`, which only forge registers). A bare
// `kcl run` — or the KCL CLI install that exists only to feed one — therefore
// cannot render any env, and where it once could it skipped the declared
// kubectl-context binding, the per-env config.js render and the deploy
// preflight. Every workflow step and bootstrap script goes through forge
// (`forge env deploy|render`, `forge ci validate-kcl`) instead.
func TestScaffoldedWorkflowsNeverRunKCLDirectly(t *testing.T) {
	g, dir := ciGenerator(t, config.FeaturesConfig{})
	g.FrontendName = "web"
	writeCIProjectConfig(t, g)
	writeEnvMain(t, dir, "dev", "staging", "prod")
	if err := g.generateCIFiles(); err != nil {
		t.Fatalf("generateCIFiles: %v", err)
	}
	if err := g.generatePreCommitWorkflow(); err != nil {
		t.Fatalf("generatePreCommitWorkflow: %v", err)
	}
	if err := g.generateBootstrapScript(); err != nil {
		t.Fatalf("generateBootstrapScript: %v", err)
	}

	rawKCL := regexp.MustCompile(`(?m)^[^#\n]*\bkcl (run|mod|fmt|vet|lint|test)\b|kcl-lang\.io/script/install|name: Install KCL|~/\.kcl|\.kcl/bin`)
	files, _ := filepath.Glob(filepath.Join(dir, ".github", "workflows", "*.yml"))
	files = append(files, filepath.Join(dir, "scripts", "bootstrap.sh"))
	if len(files) < 5 {
		t.Fatalf("expected the full workflow set plus bootstrap.sh, got %v", files)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(dir, f)
		for _, m := range rawKCL.FindAll(b, -1) {
			t.Errorf("%s runs KCL outside forge: %q", rel, m)
		}
	}
}
