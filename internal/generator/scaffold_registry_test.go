package generator_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
)

// The image registry is DECLARED on each WORKLOAD, as part of its image in
// deploy/kcl/workloads.k. No ENV declares one, and no scaffolded env may route
// one through a render option (`option("registry")`), the retired
// forge.registry helper, or a comment telling CI to pass `-D registry=`.
//
// This replaces the per-env literal this test used to assert. The env field it
// checked for is gone from both schemas, so asserting it would now demand a
// line that cannot compile.
func TestScaffoldDeclaresTheRegistryOnEachWorkloadImage(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds and generates a full project; runs in task test")
	}
	imageLine := regexp.MustCompile(`(?m)^\s*image = "([^"]+)"$`)
	envRegistryLine := regexp.MustCompile(`(?m)^\s*registry = "`)

	for name, tc := range map[string]struct {
		module string
		shared bool
		// wantImage is the reference every scaffolded workload declares.
		wantImage string
	}{
		"github module": {module: "github.com/Acme/shop", wantImage: "ghcr.io/acme/shop"},
		// No GitHub owner to derive from: a placeholder that still parses as a
		// host (so the tree compiles) and can never resolve (so a push fails
		// at the placeholder rather than somewhere real).
		"non-github module":     {module: "example.com/shop", wantImage: "REPLACE-ME-REGISTRY.invalid/shop"},
		"shared-binary project": {module: "github.com/acme/shop", shared: true, wantImage: "ghcr.io/acme/shop"},
	} {
		t.Run(name, func(t *testing.T) {
			tmp := t.TempDir()
			g := generator.NewProjectGenerator("shop", tmp, tc.module)
			g.Kind = config.ProjectKindService
			g.ApplyKindFeatureDefaults(config.ProjectKindService)
			if tc.shared {
				g.Binary = "shared"
			}
			if err := g.Generate(); err != nil {
				t.Fatalf("Generate: %v", err)
			}

			// Every workload declares the full reference, once, in workloads.k.
			raw, err := os.ReadFile(filepath.Join(tmp, "deploy", "kcl", "workloads.k"))
			if err != nil {
				t.Fatalf("read workloads.k: %v", err)
			}
			var declared []string
			for _, m := range imageLine.FindAllStringSubmatch(string(raw), -1) {
				declared = append(declared, m[1])
			}
			if len(declared) == 0 {
				t.Fatalf("no workload declares an image:\n%s", raw)
			}
			for _, image := range declared {
				// A third-party image names its own registry and is not this
				// project's artifact; everything forge builds is the project's.
				if strings.HasPrefix(image, "docker.io/") {
					continue
				}
				if image != tc.wantImage {
					t.Errorf("workload image = %q, want %q", image, tc.wantImage)
				}
			}

			// No env declares a registry, in any spelling.
			for _, env := range []string{"dev", "staging", "prod"} {
				raw, err := os.ReadFile(filepath.Join(tmp, "deploy", "kcl", env, "main.k"))
				if err != nil {
					t.Fatalf("read %s/main.k: %v", env, err)
				}
				body := string(raw)
				if envRegistryLine.MatchString(body) {
					t.Errorf("%s/main.k declares a `registry = …` — an env has no registry; the workload's image carries it", env)
				}
				for _, bad := range []string{"forge.registry", `option("registry")`, "-D registry", "$REGISTRY"} {
					if strings.Contains(body, bad) {
						t.Errorf("%s/main.k carries %q — the registry is part of a workload's image, and nothing overrides it", env, bad)
					}
				}
			}
		})
	}
}
