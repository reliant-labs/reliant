package kclrender_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/internal/kclvendor"
)

// TestRunSuppliesForgeModuleFromTheBinary is the contract every forge render
// depends on: a project whose kcl.mod declares NO forge dependency — which is
// what forge scaffolds, identically under every forge build — resolves
// `import forge` from the module embedded in the rendering binary, and the
// render writes nothing into the project tree to do it (no .forge-kcl/, no
// kcl.mod edit). That is what lets a released forge render a project on a
// fresh CI checkout or in a container with no network and no vendored copy.
func TestRunSuppliesForgeModuleFromTheBinary(t *testing.T) {
	t.Cleanup(kclvendor.SetCacheDirForTest(t.TempDir()))
	dir := t.TempDir()
	envDir := filepath.Join(dir, "deploy", "kcl", "dev")
	const kclMod = "[package]\nname = \"module_probe\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\n"
	files := map[string]string{
		"deploy/kcl/kcl.mod":    kclMod,
		"deploy/kcl/dev/main.k": "import forge\nimport forge.workloads\n\nok = forge.Bundle is not None\n",
	}
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out, err := kclrender.Run(dir, envDir, []string{"env=dev"})
	if err != nil {
		t.Fatalf("render of a project with no forge dependency must resolve `import forge` from the binary; got: %v", err)
	}
	if !strings.Contains(string(out), `"ok": true`) && !strings.Contains(string(out), `"ok":true`) {
		t.Errorf("render output does not show the forge module resolved:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".forge-kcl")); !os.IsNotExist(err) {
		t.Errorf("render materialized a project-local .forge-kcl/ (stat err %v); the module must come from the binary", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "deploy", "kcl", "kcl.mod")); string(got) != kclMod {
		t.Errorf("render edited kcl.mod:\n%s", got)
	}
	// kpm writes an empty kcl.mod.lock for a dependency-free package. That
	// is the one file a render may leave, and it must be byte-empty so a
	// committed one never shows a diff after rendering.
	var extra []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if _, declared := files[rel]; declared {
			return nil
		}
		if rel == "deploy/kcl/kcl.mod.lock" && info.Size() == 0 {
			return nil
		}
		extra = append(extra, rel)
		return nil
	})
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("render wrote into the project: %v", extra)
	}
}

// TestRunRefusesAnUnmigratedKclMod: a kcl.mod still declaring the old
// vendored path would make kpm resolve `import forge` from a stale project
// copy (or fail on a missing one with advice that is wrong for forge). The
// render must refuse and name `forge generate`.
func TestRunRefusesAnUnmigratedKclMod(t *testing.T) {
	t.Cleanup(kclvendor.SetCacheDirForTest(t.TempDir()))
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"deploy/kcl/kcl.mod":    "[package]\nname = \"p\"\n\n[dependencies]\nforge = { path = \"../../.forge-kcl\" }\n",
		"deploy/kcl/dev/main.k": "import forge\n\nok = True\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, err := kclrender.Run(dir, filepath.Join(dir, "deploy", "kcl", "dev"), []string{"env=dev"})
	if err == nil || !strings.Contains(err.Error(), "forge generate") {
		t.Fatalf("Run on an unmigrated kcl.mod = %v; want a refusal naming `forge generate`", err)
	}
}
