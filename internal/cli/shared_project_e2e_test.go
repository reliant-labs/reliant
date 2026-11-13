//go:build e2e

package cli

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ── Shared `project new` starting points ────────────────────────────────────
//
// A `forge project new --frontend …` costs minutes: the project files, the
// frontend's dependency install, the whole first `forge generate`, and the
// module bridge (addCorpusForgePkgReplace). Several frontend tests started
// from byte-for-byte the same project and paid that cost each, before reaching
// the step they actually test. A sharedProjectE2E makes that starting point
// ONCE per test process (sync.Once, like buildforgeBinary) and hands each test
// a private copy in its own t.TempDir(), so the tests stay t.Parallel and keep
// no shared mutable state.
//
// The bar for sharing: the shared steps are NOT the subject of any test that
// shares them, and at least two tests start from exactly that shape. A test
// whose subject is a different scaffold variant keeps its own `project new`.
// So does the from-scratch smoke proof of the whole pipeline — `project new`
// → install → generate → `next build`/`tsc` in one directory:
// TestE2EScaffoldFrontendListResource (dashboard, `next build`),
// TestE2EDigitNamedServicesAndEntitiesBuild (web, tsc),
// TestE2EScaffoldFrontendEnumEntityBuilds (dashboard, tsc),
// TestE2ENextBuildPrerendersWithInstalledPeerPins and
// TestE2EFixtureCorpusFrontendBasePath. Never move all of them onto a fixture:
// the copy is not how a user gets a project, and only those prove the path a
// user takes.

// sharedProjectSpec is one shared starting point: `forge project new <name>
// <newArgs…>`, the module bridge, then each forge invocation in then.
type sharedProjectSpec struct {
	name    string
	newArgs []string
	then    [][]string
}

func (s sharedProjectSpec) key() string {
	parts := append([]string{s.name}, s.newArgs...)
	for _, step := range s.then {
		parts = append(parts, "&&", strings.Join(step, " "))
	}
	return strings.Join(parts, " ")
}

var (
	// webItemProjectE2E: a Next.js `web` frontend and an empty `item`
	// service — the start of every test that then authors its own Item
	// entity and checks what generate emits for the frontend.
	webItemProjectE2E = sharedProjectSpec{
		name:    "feapp",
		newArgs: []string{"--mod", "example.com/feapp", "--frontend", "web"},
		then:    [][]string{{"scaffold", "service", "item"}},
	}
	// dashboardCatalogProjectE2E: a `dashboard` frontend and a `catalog`
	// service, born by `project new --service` — the fresh scaffold the
	// lint-clean and validate-projection tests author their entity into.
	dashboardCatalogProjectE2E = sharedProjectSpec{
		name:    "dashapp",
		newArgs: []string{"--mod", "example.com/dashapp", "--service", "catalog", "--frontend", "dashboard"},
	}
	// widgetServiceProjectE2E: one `widget` service and no frontend — the
	// start of the tests whose subject is `forge scaffold frontend`.
	widgetServiceProjectE2E = sharedProjectSpec{
		name:    "widgetapp",
		newArgs: []string{"--mod", "example.com/widgetapp", "--service", "widget"},
	}
)

type sharedProject struct {
	once sync.Once
	dir  string // the project directory; read-only once built
	err  error
}

var sharedProjects = struct {
	sync.Mutex
	m map[string]*sharedProject
}{m: map[string]*sharedProject{}}

// newProjectFromSharedE2E returns a private copy of spec's project in a fresh
// t.TempDir(), building the shared original first if no test has yet.
func newProjectFromSharedE2E(t *testing.T, forgeBin string, spec sharedProjectSpec) string {
	t.Helper()
	return copyProjectE2E(t, sharedProjectE2E(t, forgeBin, spec), t.TempDir())
}

// sharedProjectE2E returns the shared original of spec, building it on first
// use. The original is READ-ONLY: copy it with copyProjectE2E.
func sharedProjectE2E(t *testing.T, forgeBin string, spec sharedProjectSpec) string {
	t.Helper()
	sharedProjects.Lock()
	p, ok := sharedProjects.m[spec.key()]
	if !ok {
		p = &sharedProject{}
		sharedProjects.m[spec.key()] = p
	}
	sharedProjects.Unlock()

	p.once.Do(func() {
		// The build runs on the first caller's t, so a failing step
		// t.Fatal's THAT test (its log holds the command output) and ends
		// this func by runtime.Goexit. Once still counts that as done, so
		// the deferred check is what tells every later caller it failed.
		built := false
		defer func() {
			if !built && p.err == nil {
				p.err = fmt.Errorf("building the shared project %q failed in %s — its log has the failing command", spec.key(), t.Name())
			}
		}()
		root, err := os.MkdirTemp("", "forge-e2e-shared-project-")
		if err != nil {
			p.err = err
			return
		}
		// Removed by TestMain with the other process-wide temp dirs: every
		// test sharing it copies from it until the process ends.
		registerSharedTempDir(root)
		runCmd(t, root, forgeBin, append([]string{"project", "new", spec.name}, spec.newArgs...)...)
		projectDir := filepath.Join(root, spec.name)
		addCorpusForgePkgReplace(t, projectDir)
		for _, step := range spec.then {
			runCmd(t, projectDir, forgeBin, step...)
		}
		p.dir = projectDir
		built = true
	})
	if p.err != nil {
		t.Fatalf("%v", p.err)
	}
	return p.dir
}

// copyProjectE2E copies the project at src into parent, under the same base
// name, and returns the copy's path. The copy behaves as if `project new` had
// run there:
//
//   - A symlink that leaves the project is re-aimed from the copy's location,
//     relative as before (the --link-forge bridge's .forge-link/web-runtime is
//     one); a symlink inside the project is kept verbatim, so node_modules'
//     relative links resolve within the copy.
//   - Package files under node_modules/.pnpm/<pkg>/ are HARD-LINKED, not
//     copied: that is the cheap part of sharing an install (hundreds of MB,
//     tens of thousands of files). pnpm itself hard-links exactly these files
//     from its machine-wide store into every project on a machine, so no tool
//     may modify them in place without corrupting every other project — and
//     node_modules metadata, which pnpm does rewrite, is copied.
//   - Copied files under node_modules that record the original's absolute
//     path (pnpm's workspace state) are rewritten to the copy's.
//
// Every other file is copied with its mode and mtime, so the copy is
// independent of the original and of every other test's copy.
func copyProjectE2E(t *testing.T, src, parent string) string {
	t.Helper()
	dst := filepath.Join(parent, filepath.Base(src))
	if err := copyProjectTree(src, dst); err != nil {
		t.Fatalf("copy the shared project %s -> %s: %v", src, dst, err)
	}
	return dst
}

func copyProjectTree(src, dst string) error {
	srcPrefix := []byte(src + string(filepath.Separator))
	dstPrefix := []byte(dst + string(filepath.Separator))
	within := func(root, p string) bool {
		return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if filepath.IsAbs(link) {
				if within(src, link) {
					link = filepath.Join(dst, strings.TrimPrefix(link, src))
				}
			} else if resolved := filepath.Join(filepath.Dir(p), link); !within(src, resolved) {
				if link, err = filepath.Rel(filepath.Dir(target), resolved); err != nil {
					return err
				}
			}
			return os.Symlink(link, target)
		case !d.Type().IsRegular():
			return nil // sockets, fifos: nothing a project new leaves behind
		}
		if inPnpmPackage(rel) {
			if err := os.Link(p, target); err == nil {
				return nil
			}
			// Another filesystem: fall through to a copy.
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if inNodeModules(rel) {
			data = bytes.ReplaceAll(data, srcPrefix, dstPrefix)
		}
		if err := os.WriteFile(target, data, info.Mode().Perm()); err != nil {
			return err
		}
		return os.Chtimes(target, info.ModTime(), info.ModTime())
	})
}

// inPnpmPackage reports whether rel lies inside a package directory of a pnpm
// virtual store: node_modules/.pnpm/<pkg>/<file…>. Files directly in .pnpm
// (its lock.yaml) are pnpm's own metadata and are not.
func inPnpmPackage(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i := 0; i+3 < len(parts); i++ {
		if parts[i] == "node_modules" && parts[i+1] == ".pnpm" {
			return true
		}
	}
	return false
}

func inNodeModules(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "node_modules" {
			return true
		}
	}
	return false
}
