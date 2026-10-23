package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/generator"
)

// `forge project new` and `forge generate` must render the SAME GitHub
// Actions files for the same project.
//
// They used to build their template data in two places, and the two
// disagreed on a project seconds old. houndersclub deleted ci.yml to adopt
// forge v0.1.18's template, re-ran `forge generate`, and the Docker Build job
// was gone: the generate-side mapper never set HasDocker. The same pass also
// dropped e2e.yml, and on the default scaffold (no service proto yet) every
// buf step and proto-breaking.yml.
//
// The test scaffolds a project, deletes the workflows the scaffold wrote (and
// their scaffold-once birth records, which is how a user re-scaffolds one),
// runs the generate-side CI step over the reloaded forge.yaml, and demands
// byte-identical files — the same set, the same bytes.
func TestCIWorkflows_NewAndGenerateRenderIdentically(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds and generates a full project; runs in task test")
	}
	cases := []struct {
		name  string
		shape func(g *generator.ProjectGenerator)
	}{
		{"service with frontend", func(g *generator.ProjectGenerator) {
			g.ServiceName = "item"
			g.FrontendName = "web"
		}},
		{"default scaffold", func(*generator.ProjectGenerator) {}},
		{"cli", func(g *generator.ProjectGenerator) { g.Kind = "cli" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			g := generator.NewProjectGenerator("demo", dir, "github.com/example/demo")
			tc.shape(g)
			if err := g.Generate(); err != nil {
				t.Fatalf("scaffold: %v", err)
			}

			scaffolded := readCIWorkflowSet(t, dir)
			if _, ok := scaffolded[".github/workflows/ci.yml"]; !ok {
				t.Fatalf("scaffold wrote no ci.yml; got %v", sortedKeys(scaffolded))
			}
			for rel := range scaffolded {
				if err := os.Remove(filepath.Join(dir, rel)); err != nil {
					t.Fatal(err)
				}
				checksums.ForgetScaffold(dir, rel)
			}

			cfg := loadAdvisoryConfig(t, dir)
			// `forge generate` discovers which envs are hosted by RENDERING
			// them, and a project's envs import the config projection the
			// generate run inside `forge project new` already wrote. Give
			// this bare scaffold that projection, or its hosted staging and
			// prod cannot render and read as cluster envs.
			if _, err := os.Stat(filepath.Join(dir, "deploy", "kcl")); err == nil {
				cs, err := generator.LoadChecksums(dir)
				if err != nil {
					t.Fatal(err)
				}
				captureStdout(t, func() {
					if err := generatePerEnvDeployConfig(dir, cfg, cs, nil); err != nil {
						t.Fatalf("generatePerEnvDeployConfig: %v", err)
					}
				})
			}
			captureStdout(t, func() {
				if err := generateCIWorkflows(dir, cfg, nil, false); err != nil {
					t.Fatalf("generateCIWorkflows: %v", err)
				}
			})
			regenerated := readCIWorkflowSet(t, dir)

			for rel, want := range scaffolded {
				got, ok := regenerated[rel]
				if !ok {
					t.Errorf("%s: `forge project new` writes it, `forge generate` does not", rel)
					continue
				}
				if got != want {
					t.Errorf("%s: `forge generate` renders different bytes than `forge project new`\n--- new ---\n%s\n--- generate ---\n%s", rel, want, got)
				}
			}
			for rel := range regenerated {
				if _, ok := scaffolded[rel]; !ok {
					t.Errorf("%s: `forge generate` writes it, `forge project new` does not", rel)
				}
			}
		})
	}
}

// readCIWorkflowSet returns the CI files BOTH commands own, keyed by
// slash path: every workflow except pre-commit.yml (a developer-experience
// file only `forge project new` writes, from dx_files.go) plus dependabot.
func readCIWorkflowSet(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	paths, _ := filepath.Glob(filepath.Join(dir, ".github", "workflows", "*.yml"))
	paths = append(paths, filepath.Join(dir, ".github", "dependabot.yml"))
	for _, p := range paths {
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if rel == ".github/workflows/pre-commit.yml" {
			continue
		}
		b, err := os.ReadFile(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		out[rel] = string(b)
	}
	return out
}
