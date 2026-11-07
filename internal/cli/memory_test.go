package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderProjectMemory_HappyPath verifies the public API used by
// reliant: given a project root with a forge.yaml, the rendered body
// substitutes the project name, includes the "Use forge skills" callout
// that we want every harness's memory to surface, and contains the
// critical-rules section that downstream agents rely on.
func TestRenderProjectMemory_HappyPath(t *testing.T) {
	dir := t.TempDir()
	cfg := "name: my-app\nmodule_path: example.com/my-app\nversion: \"1.0.0\"\n"
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}

	body, err := RenderProjectMemory(dir)
	if err != nil {
		t.Fatalf("RenderProjectMemory: %v", err)
	}
	got := string(body)
	if !strings.Contains(got, "# my-app") {
		t.Errorf("rendered body missing project name heading; got:\n%s", got[:min(200, len(got))])
	}
	if !strings.Contains(got, "Use forge skills to guide your work") {
		t.Errorf("rendered body missing \"Use forge skills\" callout (lost in upgrade?)")
	}
	if !strings.Contains(got, "## Critical rules") {
		t.Errorf("rendered body missing Critical rules section")
	}
	if !strings.Contains(got, "<!-- forge:version=1 -->") {
		t.Errorf("rendered body missing forge:version marker")
	}
}

// TestRenderProjectMemory_MissingForgeYAML verifies a clear error rather
// than a panic when the caller points at a non-forge directory.
func TestRenderProjectMemory_MissingForgeYAML(t *testing.T) {
	dir := t.TempDir()
	if _, err := RenderProjectMemory(dir); err == nil {
		t.Fatal("expected error when forge.yaml is missing")
	}
}

// TestRenderProjectMemory_EmptyRoot is a defensive check on the
// signature contract — callers must not pass empty.
func TestRenderProjectMemory_EmptyRoot(t *testing.T) {
	if _, err := RenderProjectMemory(""); err == nil {
		t.Fatal("expected error when projectRoot is empty")
	}
}

// TestRenderProjectMemory_MissingName verifies the loader surfaces the
// missing-name case rather than rendering "# " (silently broken).
func TestRenderProjectMemory_MissingName(t *testing.T) {
	dir := t.TempDir()
	cfg := "module_path: example.com/x\nversion: \"1.0.0\"\n"
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	if _, err := RenderProjectMemory(dir); err == nil {
		t.Fatal("expected error when forge.yaml has no `name:`")
	}
}
