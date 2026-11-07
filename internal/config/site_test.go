package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSiteTree lays down a static site: a Vite app at the root, a forge.yaml
// with no module_path, and no go.mod.
func writeSiteTree(t *testing.T, extra map[string]string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"forge.yaml":     "name: landing\nharness: reliant\n",
		"package.json":   `{"name":"landing","devDependencies":{"vite":"^4"}}`,
		"vite.config.ts": "export default {}\n",
	}
	for k, v := range extra {
		files[k] = v
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// A site has no Go module, so it has no module path to declare, and the load
// must not demand one; the moment a go.mod appears the field is required.
func TestModulePathOptionalWithoutGoModule(t *testing.T) {
	root := writeSiteTree(t, nil)
	cfg, err := LoadProjectDir(root)
	if err != nil {
		t.Fatalf("a site with no module_path must load: %v", err)
	}
	if cfg.HasGoModule() {
		t.Fatal("HasGoModule = true for a tree with no go.mod")
	}
	if cfg.Features.ContractsEnabled() {
		t.Error("contracts must derive off without a Go module: there is no contract.go to check")
	}

	withGo := writeSiteTree(t, map[string]string{"go.mod": "module example.com/x\n\ngo 1.24\n"})
	if _, err := LoadProjectDir(withGo); err == nil || !strings.Contains(err.Error(), "module_path") {
		t.Fatalf("a Go module with no module_path must fail naming module_path, got %v", err)
	}
}

// The root app of a site is in the inventory every command loads — lint,
// typecheck and build included — not only in the one generate renders.
func TestRootFrontendIsInTheLoadedInventory(t *testing.T) {
	root := writeSiteTree(t, nil)
	cfg, err := LoadProjectDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Frontends) != 1 {
		t.Fatalf("want the root frontend in the inventory, got %d frontends", len(cfg.Frontends))
	}
	fe := cfg.Frontends[0]
	dir, ok := fe.Dir(root)
	if fe.Name != RootFrontendName || fe.Type != "vite-spa" || !ok || dir != "." {
		t.Errorf("root frontend = %+v (dir %q, ok %v), want %s / vite-spa at .", fe, dir, ok, RootFrontendName)
	}

	// A Go module's root is its module: a vite.config there is not guessed
	// into a frontend.
	goRoot := writeSiteTree(t, map[string]string{
		"go.mod":     "module example.com/x\n\ngo 1.24\n",
		"forge.yaml": "name: landing\nmodule_path: example.com/x\n",
	})
	if _, ok := DiscoverRootFrontend(goRoot); ok {
		t.Error("a Go module's root must not be discovered as a frontend")
	}
}

// KCL may declare the root app under any name; generate's merge must fold
// the declaration onto the discovered entry (same directory) rather than
// report two frontends at ".".
func TestMergeFrontendInventoryMatchesOneDirectoryUnderTwoNames(t *testing.T) {
	disk := []FrontendConfig{FrontendConfig{Name: RootFrontendName, Type: "vite-spa", DevRunner: "npm"}.WithDir(".")}
	kcl := []FrontendConfig{FrontendConfig{Name: "site", Type: "vite-spa"}.WithDir(".")}
	got := MergeFrontendInventory(disk, kcl)
	if len(got) != 1 || got[0].Name != "site" || got[0].DevRunner != "npm" {
		t.Fatalf("merge = %+v, want one frontend named site keeping the discovered dev runner", got)
	}
}
