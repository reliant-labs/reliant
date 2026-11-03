package buildinfo

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
)

// TestForgeRootFromFile exercises the pure upward walk behind runtime source
// discovery against fixture trees: it must find the forge root only when the
// module (github.com/reliant-labs/forge) and its pkg/ runtime-library
// directory are both present, and return "" for every shape that is not a
// genuine forge checkout.
func TestForgeRootFromFile(t *testing.T) {
	// Build a fixture forge checkout: <root>/go.mod (forge) + <root>/pkg/,
	// with a nested source file deep inside it. pkg/ carries no go.mod of its
	// own — it is a directory in the single forge module.
	root := t.TempDir()
	writeGoMod(t, root, "module "+forgeModulePath+"\n\ngo 1.26\n")
	mustMkdirAll(t, filepath.Join(root, "pkg"))
	deepFile := filepath.Join(root, "internal", "buildinfo", "buildinfo.go")
	mustMkdirAll(t, filepath.Dir(deepFile))

	if got := forgeRootFromFile(deepFile); got != root {
		t.Fatalf("valid checkout: forgeRootFromFile = %q, want %q", got, root)
	}

	// pkg/ missing → not a usable forge root (nothing to `use`).
	noPkg := t.TempDir()
	writeGoMod(t, noPkg, "module "+forgeModulePath+"\n\ngo 1.26\n")
	if got := forgeRootFromFile(filepath.Join(noPkg, "internal", "buildinfo", "x.go")); got != "" {
		t.Fatalf("missing pkg/: forgeRootFromFile = %q, want \"\"", got)
	}

	// A go.mod exists but declares a DIFFERENT module (e.g. an embedder like
	// reliant walking up past its own root) → not forge, return "".
	other := t.TempDir()
	writeGoMod(t, other, "module github.com/reliant-labs/reliant\n\ngo 1.26\n")
	if got := forgeRootFromFile(filepath.Join(other, "cmd", "reliant", "main.go")); got != "" {
		t.Fatalf("foreign module root: forgeRootFromFile = %q, want \"\"", got)
	}

	// No go.mod anywhere up the tree (e.g. a -trimpath stub path that does not
	// exist on disk resolves to nothing) → "".
	if got := forgeRootFromFile(filepath.Join(t.TempDir(), "a", "b", "c.go")); got != "" {
		t.Fatalf("no go.mod on path: forgeRootFromFile = %q, want \"\"", got)
	}

	// Module path right, but `pkg` is a FILE → reject. With pkg/ no longer
	// carrying its own go.mod, "is it a directory" is the entire remaining
	// discriminator, so it gets a case of its own.
	pkgIsFile := t.TempDir()
	writeGoMod(t, pkgIsFile, "module "+forgeModulePath+"\n\ngo 1.26\n")
	if err := os.WriteFile(filepath.Join(pkgIsFile, "pkg"), []byte("not a directory\n"), 0o644); err != nil {
		t.Fatalf("write pkg file: %v", err)
	}
	if got := forgeRootFromFile(filepath.Join(pkgIsFile, "internal", "x.go")); got != "" {
		t.Fatalf("pkg is a file: forgeRootFromFile = %q, want \"\"", got)
	}
}

// TestDiscoverDevForgeRootFromSource is the live end-to-end path: because this
// test file IS compiled from a real forge checkout (and `go test` does not
// trimpath), discovery must resolve to a directory that actually contains this
// package's source.
func TestDiscoverDevForgeRootFromSource(t *testing.T) {
	root := DiscoverDevForgeRootFromSource()
	if root == "" {
		t.Skip("no on-disk source root discoverable (e.g. -trimpath test binary); walk logic covered by TestForgeRootFromFile")
	}
	// The discovered root must hold the markers we require: the module's
	// go.mod, and the pkg/ directory the runtime libraries live in.
	for _, rel := range []string{"go.mod", "pkg"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("discovered root %q missing %s: %v", root, rel, err)
		}
	}
	// And this test's own package must live under it.
	if _, err := os.Stat(filepath.Join(root, "internal", "buildinfo", "discover_test.go")); err != nil {
		t.Fatalf("discovered root %q does not contain this package's source: %v", root, err)
	}
}

func writeGoMod(t *testing.T, dir, contents string) {
	t.Helper()
	mustMkdirAll(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write go.mod in %s: %v", dir, err)
	}
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

// TestForgeRootFromFile_ModuleCacheIsNotACheckout is the reproduction for a
// pinned forge bridging projects into the READ-ONLY module cache.
//
// A project pins forge by pseudo-version, and its CI (and anyone following
// its docs) installs it with `go install …/cmd/forge@<pseudo-version>`. That
// build is not -trimpath'd, so runtime.Caller resolves into
// $GOMODCACHE/github.com/reliant-labs/forge@<version>/ — a tree with forge's
// real go.mod and pkg/. The walk accepted it as a dev checkout, and forge
// symlinked .forge-link/web-runtime into the module cache: `npm install` then
// failed with EACCES creating web-runtime/node_modules, so the project's
// frontend test lane could not run at all.
func TestForgeRootFromFile_ModuleCacheIsNotACheckout(t *testing.T) {
	modCache := filepath.Join(t.TempDir(), "pkg", "mod")
	cached := filepath.Join(modCache, "github.com", "reliant-labs", "forge@v0.1.18-0.20260926053138-05d5d6999d39")
	writeGoMod(t, cached, "module "+forgeModulePath+"\n\ngo 1.26\n")
	mustMkdirAll(t, filepath.Join(cached, "pkg"))
	mustMkdirAll(t, filepath.Join(cached, "web-runtime"))

	if got := forgeRootFromFile(filepath.Join(cached, "internal", "buildinfo", "buildinfo.go")); got != "" {
		t.Fatalf("module-cache copy accepted as a dev checkout: forgeRootFromFile = %q, want \"\" — "+
			"a pinned forge would bridge the project's frontends into a read-only tree", got)
	}

	// A tagged release in the cache is the same thing.
	tagged := filepath.Join(modCache, "github.com", "reliant-labs", "forge@v0.2.0")
	writeGoMod(t, tagged, "module "+forgeModulePath+"\n\ngo 1.26\n")
	mustMkdirAll(t, filepath.Join(tagged, "pkg"))
	if got := forgeRootFromFile(filepath.Join(tagged, "cmd", "forge", "main.go")); got != "" {
		t.Fatalf("tagged module-cache copy accepted as a checkout: %q", got)
	}

	// A real checkout whose directory merely contains an '@' (no module
	// version after it) is still a checkout.
	checkout := filepath.Join(t.TempDir(), "forge@work")
	writeGoMod(t, checkout, "module "+forgeModulePath+"\n\ngo 1.26\n")
	mustMkdirAll(t, filepath.Join(checkout, "pkg"))
	if got := forgeRootFromFile(filepath.Join(checkout, "internal", "x.go")); got != checkout {
		t.Fatalf("a checkout named %q was rejected: forgeRootFromFile = %q", filepath.Base(checkout), got)
	}
}

func forgeCheckout(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeGoMod(t, root, "module "+forgeModulePath+"\n\ngo 1.26\n")
	mustMkdirAll(t, filepath.Join(root, "pkg"))
	return root
}

// A stated path is the answer, and a wrong one is an error, never a silent
// fall-through to some other checkout.
func TestResolveForgeRoot_Explicit(t *testing.T) {
	root := forgeCheckout(t)
	t.Setenv(ForgeRootEnv, "")
	got, ok, err := ResolveForgeRoot(root)
	if err != nil || !ok || got.Dir != root {
		t.Fatalf("flag: %+v ok=%v err=%v", got, ok, err)
	}
	t.Setenv(ForgeRootEnv, root)
	if got, ok, err := ResolveForgeRoot(""); err != nil || !ok || got.Dir != root {
		t.Fatalf("env: %+v ok=%v err=%v", got, ok, err)
	}
	if _, _, err := ResolveForgeRoot(t.TempDir()); err == nil {
		t.Error("a directory that is not a forge checkout was accepted")
	}
	cache := filepath.Join(t.TempDir(), "forge@v0.1.46")
	mustMkdirAll(t, filepath.Join(cache, "pkg"))
	writeGoMod(t, cache, "module "+forgeModulePath+"\n\ngo 1.26\n")
	if _, _, err := ResolveForgeRoot(cache); err == nil {
		t.Error("a module-cache copy was accepted as a checkout")
	}
}

// A -trimpath host binary (reliant) knows only the replace as go.mod wrote it,
// "../forge", relative to a module whose location it no longer knows.
func TestForgeRootFromBuildInfoReplace(t *testing.T) {
	ws := t.TempDir()
	checkout := filepath.Join(ws, "forge")
	mustMkdirAll(t, filepath.Join(checkout, "pkg"))
	writeGoMod(t, checkout, "module "+forgeModulePath+"\n\ngo 1.26\n")
	host := filepath.Join(ws, "reliant", "bin")
	mustMkdirAll(t, host)

	info := func(replace *debug.Module) *debug.BuildInfo {
		return &debug.BuildInfo{
			Main: debug.Module{Path: "github.com/reliant-labs/reliant"},
			Deps: []*debug.Module{{Path: forgeModulePath, Version: "v0.1.46", Replace: replace}},
		}
	}
	bases := searchBases(filepath.Join(host, "reliant"))
	if got, ok := forgeRootFromBuildInfoReplace(info(&debug.Module{Path: "../forge"}), bases); !ok || got != checkout {
		t.Errorf("relative replace: %q ok=%v, want %q", got, ok, checkout)
	}
	if got, ok := forgeRootFromBuildInfoReplace(info(&debug.Module{Path: checkout}), nil); !ok || got != checkout {
		t.Errorf("absolute replace: %q ok=%v", got, ok)
	}
	if _, ok := forgeRootFromBuildInfoReplace(info(nil), bases); ok {
		t.Error("no replace resolved a checkout")
	}
	if _, ok := forgeRootFromBuildInfoReplace(info(&debug.Module{Path: "../elsewhere"}), bases); ok {
		t.Error("a replace to a directory that does not exist resolved")
	}
	if _, ok := forgeRootFromBuildInfoReplace(info(&debug.Module{Path: "example.com/fork", Version: "v1.0.0"}), bases); ok {
		t.Error("a versioned (non-directory) replace resolved a checkout")
	}
	standalone := &debug.BuildInfo{Main: debug.Module{Path: forgeModulePath}}
	if _, ok := forgeRootFromBuildInfoReplace(standalone, bases); ok {
		t.Error("standalone forge has no embedding replace")
	}
}
