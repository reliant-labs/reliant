package generator_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
)

func TestScaffoldIngressWiresKCLForServiceKind(t *testing.T) {
	tmp := t.TempDir()
	g := generator.NewProjectGenerator("svc-ingress", tmp, "example.com/svc-ingress")
	g.Kind = config.ProjectKindService
	g.ApplyKindFeatureDefaults(config.ProjectKindService)
	// Ingress is experimental — default off. The scaffold still emits
	// the ingress.k wiring so a project flipping
	// `features.experimental.ingress: true` doesn't need a rescaffold.
	// That's what this test pins.
	if err := g.Generate(); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, p := range []string{
		"deploy/kcl/ingress.k",
		"deploy/kcl/dev/ingress.k",
		"deploy/kcl/staging/ingress.k",
		"deploy/kcl/prod/ingress.k",
	} {
		if _, err := os.Stat(filepath.Join(tmp, p)); err != nil {
			t.Errorf("missing scaffolded file %s: %v", p, err)
		}
	}
	for _, env := range []string{"dev", "staging", "prod"} {
		raw, err := os.ReadFile(filepath.Join(tmp, "deploy/kcl", env, "main.k"))
		if err != nil {
			t.Fatalf("read %s/main.k: %v", env, err)
		}
		// Relative import — deploy/kcl/ is the KCL package root
		// (deploy/kcl/kcl.mod), so each env imports its sibling
		// ingress.k with a leading-dot path.
		if !strings.Contains(string(raw), "import .ingress as ing") {
			t.Errorf("%s/main.k missing ingress import:\n%s", env, raw)
		}
		if !strings.Contains(string(raw), "gateways = ing.GATEWAYS") {
			t.Errorf("%s/main.k missing gateways wiring:\n%s", env, raw)
		}
	}
}

func TestScaffoldIngressSkippedForCLIKind(t *testing.T) {
	tmp := t.TempDir()
	g := generator.NewProjectGenerator("cli-noingress", tmp, "example.com/cli-noingress")
	g.Kind = config.ProjectKindCLI
	g.ApplyKindFeatureDefaults(config.ProjectKindCLI)
	// Ingress is experimental — default off for every kind, including
	// CLI. No explicit per-kind override needed.
	if g.Features.IngressEnabled() {
		t.Fatal("cli kind: IngressEnabled() = true, want false (experimental default OFF)")
	}
}
