package generator

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The bridge links a CHECKOUT's web-runtime, whose dist/ is a gitignored
// build output. A missing or stale one handed the frontends some other
// runtime than the generated hooks were written for — the owner's dogfood
// scaffold failed typecheck on createInfiniteQueryHook / createQueryOptions /
// ServiceKeyBuilders.infinite until `pnpm --dir .forge-link/web-runtime run
// build`. The bridge now builds dist/ itself when it is missing or older than
// its sources, and only then.

// stubWebRuntimeBuild records each build the bridge asks for, and writes the
// dist/ a real build would.
func stubWebRuntimeBuild(t *testing.T) *[]string {
	t.Helper()
	var calls []string
	prev := buildWebRuntimeDist
	buildWebRuntimeDist = func(dir string) ([]byte, error) {
		calls = append(calls, dir)
		writeAt(t, filepath.Join(dir, "dist", "index.js"), time.Now())
		return nil, nil
	}
	t.Cleanup(func() { buildWebRuntimeDist = prev })
	return &calls
}

func writeAt(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// buildableWebRuntime is a fake checkout's web-runtime with its build script,
// sources and package files all stamped at `at`.
func buildableWebRuntime(t *testing.T, dir string, at time.Time) {
	t.Helper()
	for _, f := range []string{"package.json", "tsconfig.json", "tsconfig.build.json", webRuntimeBuildScript, "src/index.ts"} {
		writeAt(t, filepath.Join(dir, filepath.FromSlash(f)), at)
	}
}

func TestEnsureWebRuntimeDist_BuildsOnlyAMissingOrStaleDist(t *testing.T) {
	old := time.Now().Add(-2 * time.Hour)
	built := time.Now().Add(-time.Hour)
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string)
		build bool
	}{
		{"no dist/ (a fresh clone or worktree)", func(*testing.T, string) {}, true},
		{"dist/ newer than every source", func(t *testing.T, dir string) {
			writeAt(t, filepath.Join(dir, "dist", "index.js"), built)
		}, false},
		{"a source edited after the build", func(t *testing.T, dir string) {
			writeAt(t, filepath.Join(dir, "dist", "index.js"), built)
			writeAt(t, filepath.Join(dir, "src", "service-hooks.ts"), time.Now())
		}, true},
		{"package.json moved after the build", func(t *testing.T, dir string) {
			writeAt(t, filepath.Join(dir, "dist", "index.js"), built)
			writeAt(t, filepath.Join(dir, "package.json"), time.Now())
		}, true},
		// tsconfig.build.json excludes tests from dist/, so they cannot stale it.
		{"only a test edited after the build", func(t *testing.T, dir string) {
			writeAt(t, filepath.Join(dir, "dist", "index.js"), built)
			writeAt(t, filepath.Join(dir, "src", "service-hooks.test.ts"), time.Now())
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			buildableWebRuntime(t, dir, old)
			tc.setup(t, dir)
			calls := stubWebRuntimeBuild(t)

			ensureWebRuntimeDist(dir)
			if got := len(*calls) == 1; got != tc.build {
				t.Fatalf("built = %v (%d builds), want %v", got, len(*calls), tc.build)
			}
			// Built once, current: a second pass builds nothing.
			ensureWebRuntimeDist(dir)
			if len(*calls) > 1 {
				t.Errorf("a current dist/ was rebuilt (%d builds)", len(*calls))
			}
		})
	}
}

// A web-runtime with no build script (a fixture, a copied package) has
// nothing forge can build: it is left alone rather than failed on.
func TestEnsureWebRuntimeDist_NoBuildScriptIsLeftAlone(t *testing.T) {
	dir := t.TempDir()
	writeAt(t, filepath.Join(dir, "package.json"), time.Now())
	calls := stubWebRuntimeBuild(t)
	ensureWebRuntimeDist(dir)
	if len(*calls) != 0 {
		t.Errorf("built a web-runtime that has no %s", webRuntimeBuildScript)
	}
}

// Creating the bridge is what builds it: before this, a scaffold from a fresh
// forge worktree installed a web-runtime with no dist/ at all.
func TestEnsureDevWebRuntimeLink_BuildsTheLinkedRuntime(t *testing.T) {
	base := t.TempDir()
	forgeRoot := fakeForgeCheckout(t, filepath.Join(base, "forge"))
	buildableWebRuntime(t, filepath.Join(forgeRoot, "web-runtime"), time.Now().Add(-time.Hour))
	pinDevBuild(t, true, forgeRoot)
	projectDir := filepath.Join(base, "app")
	bridgeProject(t, projectDir, forgeRoot)
	writeFrontendManifest(t, projectDir, "web", "")
	calls := stubWebRuntimeBuild(t)

	EnsureDevWebRuntimeLink(projectDir)
	if len(*calls) != 1 || resolvePath((*calls)[0]) != resolvePath(filepath.Join(forgeRoot, "web-runtime")) {
		t.Fatalf("the bridge did not build the linked web-runtime's missing dist/: builds = %v", *calls)
	}
}
