// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const minimalForgeYAML = "name: testproj\nmodule_path: example.com/testproj\nversion: \"1.0.0\"\n"

// TestProjectMemoryWithForgeFramework_NoForgeYAML verifies the gating:
// when forge.yaml is absent at projectPath, on-disk bytes are returned
// untouched. This is the critical contract — reliant must not change
// behavior for non-forge projects.
func TestProjectMemoryWithForgeFramework_NoForgeYAML(t *testing.T) {
	dir := t.TempDir()
	onDisk := []byte("user wrote this\n")
	got := projectMemoryWithForgeFramework(dir, onDisk)
	if !bytes.Equal(got, onDisk) {
		t.Fatalf("expected on-disk bytes returned unchanged when no forge.yaml; got:\n%s", got)
	}
}

// TestProjectMemoryWithForgeFramework_NoForgeYAML_EmptyOnDisk covers the
// other half of the non-forge path: empty in → empty out, no synthetic
// content appears.
func TestProjectMemoryWithForgeFramework_NoForgeYAML_EmptyOnDisk(t *testing.T) {
	dir := t.TempDir()
	got := projectMemoryWithForgeFramework(dir, nil)
	if len(got) != 0 {
		t.Fatalf("expected empty result for non-forge project with no on-disk memory, got %d bytes:\n%s", len(got), got)
	}
}

// TestProjectMemoryWithForgeFramework_ForgeOnly verifies the framework
// is rendered and returned standalone when there's no user-written
// reliant.md to combine with.
func TestProjectMemoryWithForgeFramework_ForgeOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(minimalForgeYAML), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	got := projectMemoryWithForgeFramework(dir, nil)
	if len(got) == 0 {
		t.Fatal("expected framework body to be returned for forge project")
	}
	gotStr := string(got)
	if !strings.Contains(gotStr, "# testproj") {
		t.Errorf("framework missing project name heading from forge.yaml; got:\n%s", gotStr[:min(200, len(gotStr))])
	}
	if !strings.Contains(gotStr, "Use forge skills to guide your work") {
		t.Errorf("framework missing \"Use forge skills\" callout")
	}
}

// TestProjectMemoryWithForgeFramework_Combined verifies the
// user-content-after-framework ordering: framework block first, then a
// blank-line gap, then the on-disk bytes. Order matters because the
// user's project notes should have the final word on conflicts.
func TestProjectMemoryWithForgeFramework_Combined(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(minimalForgeYAML), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	userMemo := []byte("# my project-specific notes\n\ndo X, not Y.\n")
	got := projectMemoryWithForgeFramework(dir, userMemo)
	gotStr := string(got)

	frameworkIdx := strings.Index(gotStr, "# testproj")
	userIdx := strings.Index(gotStr, "# my project-specific notes")
	if frameworkIdx < 0 || userIdx < 0 {
		t.Fatalf("missing one or both sections: framework=%d user=%d in:\n%s", frameworkIdx, userIdx, gotStr)
	}
	if frameworkIdx >= userIdx {
		t.Errorf("expected framework block to precede user content; framework at %d, user at %d", frameworkIdx, userIdx)
	}
	if !bytes.Contains(got, userMemo) {
		t.Errorf("user-written reliant.md was mutated; expected verbatim inclusion")
	}
}

// TestProjectMemoryWithForgeFramework_BrokenForgeYAML verifies the
// best-effort contract: a forge.yaml that can't be rendered (missing
// `name:`, parse error, etc.) must NOT break the snapshot — fall back
// to returning on-disk bytes unchanged.
func TestProjectMemoryWithForgeFramework_BrokenForgeYAML(t *testing.T) {
	dir := t.TempDir()
	// Missing `name:` — forgecli.RenderProjectMemory errors out.
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("module_path: example.com/x\n"), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	onDisk := []byte("user content\n")
	got := projectMemoryWithForgeFramework(dir, onDisk)
	if !bytes.Equal(got, onDisk) {
		t.Errorf("expected on-disk bytes returned unchanged when forge.yaml is unrenderable; got:\n%s", got)
	}
}

// TestProjectMemoryWithForgeFramework_EmptyProjectPath defends the
// signature contract — empty paths must be no-ops, not panics, not
// stat-on-cwd surprises.
func TestProjectMemoryWithForgeFramework_EmptyProjectPath(t *testing.T) {
	onDisk := []byte("anything\n")
	got := projectMemoryWithForgeFramework("", onDisk)
	if !bytes.Equal(got, onDisk) {
		t.Errorf("expected on-disk bytes returned unchanged for empty projectPath; got:\n%s", got)
	}
}

func writeRepoFile(t *testing.T, dir, name, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
}

func TestCollectRepoMemories_FrameworkOnceAndIdenticalCollapsed(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"b-forge", "a-forge", "c-plain", "d-plain"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, n, ".git"), 0o755))
	}
	for _, n := range []string{"a-forge", "b-forge"} {
		writeRepoFile(t, filepath.Join(root, n), "forge.yaml", "name: "+n+"\n")
		writeRepoFile(t, filepath.Join(root, n), "reliant.md", "notes "+n)
	}
	writeRepoFile(t, filepath.Join(root, "c-plain"), "reliant.md", "same text")
	writeRepoFile(t, filepath.Join(root, "d-plain"), "reliant.md", "same text")

	mem, ver1 := collectRepoMemories(root)
	require.Contains(t, string(mem["a-forge"]), "notes a-forge")
	require.Greater(t, len(mem["a-forge"]), 5000, "first forge repo carries the framework")
	require.Contains(t, string(mem["b-forge"]), "notes b-forge")
	require.Contains(t, string(mem["b-forge"]), "`a-forge`")
	require.Less(t, len(mem["b-forge"]), 1000)
	require.Equal(t, "same text", string(mem["c-plain"]))
	require.Contains(t, string(mem["d-plain"]), "`c-plain`")

	writeRepoFile(t, filepath.Join(root, "c-plain"), "reliant.md", "changed")
	_, ver2 := collectRepoMemories(root)
	require.NotEqual(t, string(ver1), string(ver2))
}

func TestCollectRepoMemories_RootForgeCarriesFramework(t *testing.T) {
	root := t.TempDir()
	writeRepoFile(t, root, "forge.yaml", "name: root\n")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "x", ".git"), 0o755))
	writeRepoFile(t, filepath.Join(root, "x"), "forge.yaml", "name: x\n")
	mem, _ := collectRepoMemories(root)
	require.Contains(t, string(mem["x"]), "project memory")
	require.Less(t, len(mem["x"]), 1000)
}
