package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// viteApp lays down an existing Vite SPA at the root of a fresh directory —
// the shape of a landing page being adopted.
func viteApp(t *testing.T, viteConfig string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range map[string]string{
		"package.json":   `{"name":"landing","devDependencies":{"vite":"^4"}}`,
		"vite.config.ts": viteConfig,
		"index.html":     "<!doctype html>\n",
		".gitignore":     "node_modules/\ndist/\n",
	} {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// --kind site writes forge.yaml and the deploy envs, declaring the root app,
// and nothing else: no Go, and not a byte of the app's own files.
func TestRunNewSiteAdoptsTheRootApp(t *testing.T) {
	root := viteApp(t, "export default { build: { outDir: 'public-build' } }\n")
	if err := runNewSite(root, "landing", true, false, "reliant"); err != nil {
		t.Fatalf("runNewSite: %v", err)
	}

	for _, absent := range []string{"go.mod", "cmd", "proto", "Dockerfile", "internal", "Taskfile.yml"} {
		if _, err := os.Stat(filepath.Join(root, absent)); err == nil {
			t.Errorf("a site must not get %s", absent)
		}
	}
	yaml := readFile(t, filepath.Join(root, "forge.yaml"))
	if !strings.Contains(yaml, "name: landing") || strings.Contains(yaml, "\nmodule_path:") {
		t.Errorf("forge.yaml must name the site and carry no module_path:\n%s", yaml)
	}
	for _, env := range []string{"dev", "staging", "prod"} {
		main := readFile(t, filepath.Join(root, "deploy", "kcl", env, "main.k"))
		for _, want := range []string{`name = "web"`, `type = "vite"`, `path = "."`, `public_dir = "public-build"`} {
			if !strings.Contains(main, want) {
				t.Errorf("%s/main.k lacks %s:\n%s", env, want, main)
			}
		}
		binder := "_hosted_frontend(_web_frontend)"
		if env == "dev" {
			binder = "_on_host_frontend(_web_frontend)"
		}
		if !strings.Contains(main, binder) {
			t.Errorf("%s/main.k must bind the frontend with %s:\n%s", env, binder, main)
		}
	}
	gi := readFile(t, filepath.Join(root, ".gitignore"))
	if !strings.HasPrefix(gi, "node_modules/\ndist/\n") || !strings.Contains(gi, ".forge/*") {
		t.Errorf(".gitignore must keep the app's entries and gain forge's:\n%s", gi)
	}
	if got := readFile(t, filepath.Join(root, "vite.config.ts")); !strings.Contains(got, "public-build") {
		t.Errorf("the app's vite.config.ts was touched: %q", got)
	}
}

func TestRunNewSiteRefusals(t *testing.T) {
	t.Run("not in place", func(t *testing.T) {
		err := runNewSite("", "landing", false, false, "reliant")
		if err == nil || !strings.Contains(err.Error(), "--in-place") {
			t.Fatalf("want a refusal naming --in-place, got %v", err)
		}
	})
	t.Run("no frontend at the root", func(t *testing.T) {
		err := runNewSite(t.TempDir(), "landing", true, false, "reliant")
		if err == nil || !strings.Contains(err.Error(), "no frontend at its root") {
			t.Fatalf("want a no-frontend refusal, got %v", err)
		}
	})
	t.Run("a Go module", func(t *testing.T) {
		root := viteApp(t, "export default {}\n")
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := runNewSite(root, "landing", true, false, "reliant")
		if err == nil || !strings.Contains(err.Error(), "Go module") {
			t.Fatalf("want a Go-module refusal, got %v", err)
		}
	})
}

// The service scaffold over a root frontend used to write a Go server beside
// the app and never declare it. With no --kind it now stops and names
// --kind site, before writing anything.
func TestProjectNewInPlaceOverARootFrontendPointsAtKindSite(t *testing.T) {
	root := viteApp(t, "export default {}\n")
	err := runNew(t.Context(), "landing", root, "", "", nil, nil, "", true, false, nil, "reliant", true, "local", "per-service", false, false, false, false, newFrontendOptions{})
	if err == nil || !strings.Contains(err.Error(), "--kind site") {
		t.Fatalf("want a refusal naming --kind site, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "forge.yaml")); statErr == nil {
		t.Error("the refusal must come before anything is written")
	}
}

// A site's generate skips every step that needs Go (the tidies, goimports,
// the compile check, the contract emitter, workloads.k, the CI workflows
// that install forge from go.mod) instead of failing on `pattern ./...`.
func TestGenerateSkipsTheGoStepsForASite(t *testing.T) {
	root := viteApp(t, "export default {}\n")
	if err := runNewSite(root, "landing", true, false, "reliant"); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadProjectConfigFrom(filepath.Join(root, "forge.yaml"))
	if err != nil {
		t.Fatalf("load the site's forge.yaml: %v", err)
	}
	ctx, err := newPipelineContextWithFlags(root, pipelineFlags{})
	if err != nil {
		t.Fatal(err)
	}
	ctx.Cfg = cfg
	goSteps := map[string]bool{
		"ensure gen/go.mod": true, "go mod tidy (gen/)": true, "go mod tidy (root)": true,
		"goimports on generated Go": true, "go build (validate generated code)": true,
		"internal package contracts": true, "workloads.k (scaffold once)": true, "CI workflows": true,
	}
	for _, step := range generateSteps() {
		if goSteps[step.Name] {
			if step.Gate(ctx) {
				t.Errorf("step %q runs on a project with no go.mod", step.Name)
			}
			delete(goSteps, step.Name)
		}
	}
	for name := range goSteps {
		t.Errorf("step %q is no longer in the plan; update this test", name)
	}
}
