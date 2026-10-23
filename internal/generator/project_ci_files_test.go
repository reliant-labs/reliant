package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// ciGenerator returns a service-kind generator whose project dir already
// holds what generateCIFiles reads, as it does mid-scaffold: the forge.yaml
// (CI data is derived from the loaded config) and a deploy/kcl tree (the
// kind a service project is read back as). Callers that change Kind or
// Features after this must call writeCIProjectConfig again.
func ciGenerator(t *testing.T, features config.FeaturesConfig) (*ProjectGenerator, string) {
	t.Helper()
	dir := t.TempDir()
	g := &ProjectGenerator{
		Name:       "myapp",
		Path:       dir,
		ModulePath: "github.com/example/myapp",
		Kind:       "service",
		Features:   features,
	}
	writeCIProjectConfig(t, g)
	return g, dir
}

// writeCIProjectConfig writes g's forge.yaml, plus the deploy/kcl/dev env
// a service scaffold always has (kind is derived from the tree on load).
func writeCIProjectConfig(t *testing.T, g *ProjectGenerator) {
	t.Helper()
	if g.isService() {
		writeEnvMain(t, g.Path, "dev")
	}
	if err := g.writeProjectConfig(); err != nil {
		t.Fatalf("writeProjectConfig: %v", err)
	}
}

func readCIFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// A CLI project gets neither an image build nor a deploy workflow: there is no
// Dockerfile to build from and nothing to deploy.
func TestCIFiles_CLIGetsNoImageOrDeployWorkflow(t *testing.T) {
	dir := t.TempDir()
	g := &ProjectGenerator{Name: "myapp", Path: dir, ModulePath: "github.com/example/myapp", Kind: "cli"}
	if err := os.MkdirAll(filepath.Join(dir, "cmd", "myapp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmd", "myapp", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeCIProjectConfig(t, g)
	if err := g.generateCIFiles(); err != nil {
		t.Fatalf("generateCIFiles: %v", err)
	}
	for _, absent := range []string{"build-images.yml", "deploy.yml"} {
		if _, err := os.Stat(filepath.Join(dir, ".github", "workflows", absent)); !os.IsNotExist(err) {
			t.Errorf("%s was written for a CLI project (err=%v)", absent, err)
		}
	}
}

// TestCIFiles_BuildImagesBuildsForTheFirstDeployEnv: the once-per-commit
// image is built for the env deploy.yml auto-deploys, so it lands in the
// registry THAT env's KCL declares — and the workflow names no registry of its
// own. With no deploy env there is no registry to push to, and no
// build-images.yml that could only fail.
func TestCIFiles_BuildImagesBuildsForTheFirstDeployEnv(t *testing.T) {
	g, dir := ciGenerator(t, config.FeaturesConfig{})
	writeEnvMain(t, dir, "staging", "prod")
	if err := g.generateCIFiles(); err != nil {
		t.Fatalf("generateCIFiles: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", "build-images.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "FORGE_ENV: staging") {
		t.Errorf("build-images.yml should build for staging (the first deploy env):\n%s", b)
	}

	lone := t.TempDir()
	g2 := &ProjectGenerator{Name: "myapp", Path: lone, ModulePath: "github.com/example/myapp", Kind: "service"}
	writeEnvMain(t, lone, "dev")
	if err := g2.writeProjectConfig(); err != nil {
		t.Fatal(err)
	}
	if err := g2.generateCIFiles(); err != nil {
		t.Fatalf("generateCIFiles (dev only): %v", err)
	}
	if _, err := os.Stat(filepath.Join(lone, ".github", "workflows", "build-images.yml")); !os.IsNotExist(err) {
		t.Errorf("a project with no deploy env got a build-images.yml (stat err=%v); it has no registry to push to", err)
	}
}
