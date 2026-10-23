package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
)

// TestIsForgeVersionSkew pins the comparability rules.
func TestIsForgeVersionSkew(t *testing.T) {
	cases := []struct {
		binary, project string
		want            bool
	}{
		{"v1.6.0", "v1.5.0", true},
		{"1.6.0", "v1.6.0", false}, // v-prefix normalized
		{"v1.6.0", "", false},      // no project pin → silent
		{"dev", "v1.5.0", false},   // dev binary → silent
		{"(devel)", "v1.5.0", false},
		{"v0.0.0-20260101-abcdef", "v1.5.0", false}, // pseudoversion → silent
		{"v1.6.0", "v1.6.0", false},
	}
	for _, c := range cases {
		if got := isForgeVersionSkew(c.binary, c.project); got != c.want {
			t.Errorf("isForgeVersionSkew(%q, %q) = %v, want %v", c.binary, c.project, got, c.want)
		}
	}
}

// TestResolveSkillContentAtVersionSkewAdvisory verifies the end-to-end
// advisory: a project pinning a different forge_version than the running
// forge gets the one-line note inserted after the frontmatter.
func TestResolveSkillContentAtVersionSkewAdvisory(t *testing.T) {
	// Stamp a fake release version; restore the dev default afterwards.
	buildinfo.Set("v9.9.9-test", "today", "deadbeef")
	t.Cleanup(func() { buildinfo.Set("dev", "unknown", "unknown") })
	// Isolate from any real ~/.forge/skills overrides on the dev machine.
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: demo\nforge_version: 1.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	body, scope, err := ResolveSkillContentAt(root, "db")
	if err != nil {
		t.Fatalf("ResolveSkillContentAt: %v", err)
	}
	if scope != SkillScopeForge {
		t.Fatalf("scope = %q, want forge", scope)
	}
	s := string(body)
	if !strings.HasPrefix(s, "---\n") {
		t.Error("advisory must not displace frontmatter from byte 0")
	}
	if !strings.Contains(s, "Note: this guidance is from forge v9.9.9-test; this project pins forge 1.0.0.") {
		t.Errorf("missing version-skew advisory; got head:\n%s", s[:min(len(s), 400)])
	}

	// Metadata side: ListSkillsAt must mark forge-scope skills as skewed.
	metas, err := ListSkillsAt(root)
	if err != nil {
		t.Fatalf("ListSkillsAt: %v", err)
	}
	var found bool
	for _, m := range metas {
		if m.Path == "db" && m.Scope == SkillScopeForge {
			found = true
			if !m.VersionSkew {
				t.Error("db skill: VersionSkew = false, want true")
			}
			if m.SkillForgeVersion != "v9.9.9-test" {
				t.Errorf("SkillForgeVersion = %q", m.SkillForgeVersion)
			}
			if m.ProjectForgeVersion != "1.0.0" {
				t.Errorf("ProjectForgeVersion = %q", m.ProjectForgeVersion)
			}
		}
	}
	if !found {
		t.Fatal("db skill not found in listing")
	}
}

// TestResolveSkillContentAtNoSkewNoAdvisory verifies matching versions
// produce no advisory line.
func TestResolveSkillContentAtNoSkewNoAdvisory(t *testing.T) {
	buildinfo.Set("v9.9.9-test", "today", "deadbeef")
	t.Cleanup(func() { buildinfo.Set("dev", "unknown", "unknown") })
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: demo\nforge_version: v9.9.9-test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, _, err := ResolveSkillContentAt(root, "db")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "Note: this guidance is from forge") {
		t.Error("advisory present despite matching versions")
	}
}

// TestInsertAfterFrontmatter covers the no-frontmatter fallback.
func TestInsertAfterFrontmatter(t *testing.T) {
	out := insertAfterFrontmatter([]byte("plain body\n"), []byte("NOTE\n"))
	if string(out) != "NOTE\nplain body\n" {
		t.Errorf("no-frontmatter fallback: got %q", out)
	}
	out = insertAfterFrontmatter([]byte("---\nname: x\n---\nbody\n"), []byte("NOTE\n"))
	if string(out) != "---\nname: x\n---\nNOTE\nbody\n" {
		t.Errorf("frontmatter insert: got %q", out)
	}
}
