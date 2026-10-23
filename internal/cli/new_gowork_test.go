package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/config"
)

// starterGoWork is the workspace the project generator emits before the dev
// bridge runs: the main module + its gen/ submodule.
const starterGoWork = "go 1.26.2\n\nuse (\n\t.\n\tgen\n)\n"

// usePaths returns the disk paths of every `use` directive in the go.work at
// path, parsed with the real modfile parser.
func usePaths(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read go.work: %v", err)
	}
	wf, err := modfile.ParseWork(path, data, nil)
	if err != nil {
		t.Fatalf("parse go.work: %v", err)
	}
	paths := make([]string, 0, len(wf.Use))
	for _, u := range wf.Use {
		paths = append(paths, u.Path)
	}
	return paths
}

func containsUse(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// starterProject writes the starter go.work into a fresh project dir and
// returns the dir and the go.work path.
func starterProject(t *testing.T) (dir, workPath string) {
	t.Helper()
	dir = t.TempDir()
	workPath = filepath.Join(dir, "go.work")
	if err := os.WriteFile(workPath, []byte(starterGoWork), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, workPath
}

// stampRoot pins DevForgeRoot for the test.
func stampRoot(t *testing.T, root string) {
	t.Helper()
	prev := buildinfo.DevForgeRoot
	buildinfo.DevForgeRoot = root
	t.Cleanup(func() { buildinfo.DevForgeRoot = prev })
}

// TestWriteDevForgeGoWork_LinkAddsForgeUse: with --link-forge, the starter
// go.work gains `use <forge root>`, preserving the existing `.` and `gen` uses.
//
// The path is the repo ROOT, not <root>/pkg. It used to be pkg/, when that
// was its own module and the only forge module a scaffold imported; forge is
// one module now, so the root is the only thing there is to `use` — and a
// `use <root>/pkg` would name a directory with no go.mod, which the go
// command rejects outright.
func TestWriteDevForgeGoWork_LinkAddsForgeUse(t *testing.T) {
	dir, workPath := starterProject(t)
	forgeRoot := t.TempDir()
	stampRoot(t, forgeRoot)

	writeDevForgeGoWork(dir, true)

	got := usePaths(t, workPath)
	if !containsUse(got, forgeRoot) {
		t.Errorf("go.work missing `use %s`; uses = %v", forgeRoot, got)
	}
	if !containsUse(got, ".") || !containsUse(got, "gen") {
		t.Errorf("dev bridge dropped the starter uses; uses = %v", got)
	}
	if stale := filepath.Join(forgeRoot, "pkg"); containsUse(got, stale) {
		t.Errorf("dev bridge used %s — pkg/ has no go.mod, so the go command would reject it. uses = %v", stale, got)
	}
}

// TestWriteDevForgeGoWork_NotRequestedWritesNothing is the opt-in, pinned.
// A dev build that knows its checkout — exactly the build that used to bridge
// every scaffold on its own, including a host binary embedding forge through
// a workspace — writes nothing unless --link-forge asked for it.
func TestWriteDevForgeGoWork_NotRequestedWritesNothing(t *testing.T) {
	dir, workPath := starterProject(t)
	buildinfo.SetDevBuild(true)
	t.Cleanup(buildinfo.ClearDevBuild)
	stampRoot(t, t.TempDir())
	buildinfo.SetDiscoveredForgeRoot(t.TempDir())
	t.Cleanup(buildinfo.ClearDiscoveredForgeRoot)

	writeDevForgeGoWork(dir, false)

	after, err := os.ReadFile(workPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != starterGoWork {
		t.Errorf("a dev build bridged the project without --link-forge:\n%s", string(after))
	}
}

// TestWriteDevForgeGoWork_Idempotent: running the bridge twice yields exactly
// one forge use (a re-scaffold or repeated call must not duplicate lines).
func TestWriteDevForgeGoWork_Idempotent(t *testing.T) {
	dir, workPath := starterProject(t)
	forgeRoot := t.TempDir()
	stampRoot(t, forgeRoot)

	writeDevForgeGoWork(dir, true)
	writeDevForgeGoWork(dir, true)

	count := 0
	for _, p := range usePaths(t, workPath) {
		if p == forgeRoot {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one `use %s`, got %d", forgeRoot, count)
	}
}

// TestWriteDevForgeGoWork_NoRootNoWrite: a binary with neither a stamped
// source root NOR a discoverable one (a trimpath'd or shipped binary) never
// guesses a path. checkScaffoldCanResolveForge refuses such a request before
// anything is written; the writer stays safe for any direct caller. Discovery
// is pinned to "" because under `go test` runtime.Caller would resolve the
// live checkout.
func TestWriteDevForgeGoWork_NoRootNoWrite(t *testing.T) {
	dir, workPath := starterProject(t)
	stampRoot(t, "")
	buildinfo.SetDiscoveredForgeRoot("")
	t.Cleanup(buildinfo.ClearDiscoveredForgeRoot)

	writeDevForgeGoWork(dir, true)

	after, err := os.ReadFile(workPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != starterGoWork {
		t.Errorf("a binary without a checkout modified go.work:\n%s", string(after))
	}
}

// TestWriteDevForgeGoWork_DiscoversRoot: with NO stamped DevForgeRoot the
// bridge still finds the checkout at runtime (the embedded-in-reliant case,
// where the host binary never stamped forge's ldflag) and uses it exactly
// like a stamped one.
func TestWriteDevForgeGoWork_DiscoversRoot(t *testing.T) {
	dir, workPath := starterProject(t)
	forgeRoot := t.TempDir()
	stampRoot(t, "") // no ldflag stamp — force the discovery fallback
	buildinfo.SetDiscoveredForgeRoot(forgeRoot)
	t.Cleanup(buildinfo.ClearDiscoveredForgeRoot)

	writeDevForgeGoWork(dir, true)

	if got := usePaths(t, workPath); !containsUse(got, forgeRoot) {
		t.Errorf("discovered-root bridge missing %q; go.work uses = %v", forgeRoot, got)
	}
}

// TestWriteDevForgeGoWork_NoGoWorkNoCreate: with no go.work present (e.g. a
// library scaffold), the bridge is a no-op and creates no file.
func TestWriteDevForgeGoWork_NoGoWorkNoCreate(t *testing.T) {
	dir := t.TempDir()
	stampRoot(t, t.TempDir())

	writeDevForgeGoWork(dir, true)

	if _, err := os.Stat(filepath.Join(dir, "go.work")); !os.IsNotExist(err) {
		t.Errorf("expected no go.work to be created, stat err = %v", err)
	}
}

// --forge-root names the checkout for a binary that cannot find its own (any
// -trimpath build): the bridge uses it, and a path that is not a forge
// checkout refuses before anything is written.
func TestWriteDevForgeGoWork_ForgeRootFlag(t *testing.T) {
	dir, workPath := starterProject(t)
	stampRoot(t, "")
	buildinfo.SetDiscoveredForgeRoot("")
	t.Cleanup(buildinfo.ClearDiscoveredForgeRoot)
	t.Setenv(buildinfo.ForgeRootEnv, "")

	checkout := t.TempDir()
	writeGoModIn(t, checkout, "module github.com/reliant-labs/forge\n\ngo 1.26\n")
	if err := os.MkdirAll(filepath.Join(checkout, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	explicitForgeRoot = checkout
	t.Cleanup(func() { explicitForgeRoot = "" })

	if err := checkScaffoldCanResolveForge(config.ProjectKindService, true, nil); err != nil {
		t.Fatalf("a named checkout was refused: %v", err)
	}
	writeDevForgeGoWork(dir, true)
	if got := usePaths(t, workPath); !containsUse(got, checkout) {
		t.Errorf("go.work uses %v, want the named checkout %s", got, checkout)
	}

	explicitForgeRoot = t.TempDir()
	err := checkScaffoldCanResolveForge(config.ProjectKindService, true, nil)
	if err == nil || !strings.Contains(err.Error(), "is not a forge checkout") {
		t.Errorf("a directory that is not a forge checkout: %v", err)
	}
}
