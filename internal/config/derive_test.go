package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// serviceProjectPath writes src to a temp forge.yaml and creates the on-disk
// marker that makes the project derive to kind=service (pkg/app is the
// service composition root), returning the forge.yaml path.
//
// Kind derivation reads the TREE, not the file, so a fixture that wants a
// kind has to BE a tree. A bare path with no project behind it loads as a
// library — see deriveProjectKindFromSources.
func serviceProjectPath(t *testing.T, src string) string {
	t.Helper()
	return projectPath(t, src, filepath.Join("pkg", "app"))
}

// projectPath writes src to a temp forge.yaml, mkdir -p's each shape marker
// in dirs (project-relative), and returns the forge.yaml path.
func projectPath(t *testing.T, src string, dirs ...string) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "forge.yaml")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	return path
}

// minimalForgeYAML is the scaffold output: identity only. Everything else is
// derived from the tree.
const minimalForgeYAML = `
name: demo
module_path: example.com/demo
forge_version: v0.0.0-test
`

// TestLoadProject_RejectsBuildSection confirms forge.yaml carries NO build
// config: the `build:` block was removed (build is a per-service, per-env
// polymorphic declaration in KCL), so a `build:` key must be REJECTED as
// an unknown key rather than silently ignored. This is the back-compat
// guardrail — a project upgrading from the old build.version/version_var
// shape gets a loud error pointing it at the KCL GoBuild ldflags path.
func TestLoadProject_RejectsBuildSection(t *testing.T) {
	src := minimalForgeYAML + `
build:
    version_var: github.com/acme/app/internal/buildinfo.Version
`
	_, err := LoadProject([]byte(src), serviceProjectPath(t, src))
	if err == nil {
		t.Fatal("expected LoadProject to reject a `build:` section, got nil error")
	}
	if !strings.Contains(err.Error(), "build") {
		t.Errorf("error should mention the rejected `build` key, got: %v", err)
	}
}
