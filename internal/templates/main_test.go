package templates

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestMain removes the process-wide temp directories this package's fixtures
// create.
//
// Several fixtures here are shared across every subtest through a sync.Once —
// a scaffolded demo project (skills_validation_test.go) and a KCL module cache
// (kcl_module_test.go) — so neither can use t.TempDir: the first test to
// finish would delete the tree the rest still read. That made them permanent
// leaks instead, because nothing else could own their lifetime. Measured on
// one developer machine: 132 `forge-skill-validate-*` and 158
// `forge-kcl-module-test-*` directories, 226 MB, one pair per package run.
//
// The process is the right owner, so cleanup belongs here: after m.Run(), when
// no test can still be reading them.
//
// On FAILURE the trees are KEPT and their paths printed. A failing scaffold
// assertion is diagnosed by looking at the bytes that were generated, and
// deleting them on the way out would leave the developer with a message about
// a file they can no longer open.
func TestMain(m *testing.M) {
	code := m.Run()
	stopKCLWorkers()
	if code == 0 && os.Getenv("FORGE_KEEP_TEST_TREES") == "" {
		if err := removeSharedTempDirs(); err != nil {
			fmt.Fprintf(os.Stderr, "templates: remove shared fixtures: %v\n", err)
			code = 1
		}
	} else {
		for _, dir := range sharedTempDirs() {
			fmt.Fprintf(os.Stderr, "templates: kept fixture tree %s\n", dir)
		}
	}
	os.Exit(code)
}

var sharedTemp struct {
	sync.Mutex
	dirs []string
}

// RegisterSharedTempDir hands a sync.Once-shared fixture directory to TestMain
// for removal after the suite. Exported because the fixtures live in both this
// package and the external templates_test package, which share one test binary
// and therefore one TestMain.
func RegisterSharedTempDir(dir string) {
	if dir == "" {
		return
	}
	sharedTemp.Lock()
	defer sharedTemp.Unlock()
	sharedTemp.dirs = append(sharedTemp.dirs, dir)
}

func sharedTempDirs() []string {
	sharedTemp.Lock()
	defer sharedTemp.Unlock()
	return append([]string(nil), sharedTemp.dirs...)
}

// removeSharedTempDirs deletes every registered tree, chmod-ing first.
//
// The chmod pass is required rather than defensive: a scaffolded project is
// generated with `go mod download` having populated a module cache, whose
// files are 0444 inside 0555 directories, and RemoveAll cannot unlink a child
// of a directory it has no write permission on.
func removeSharedTempDirs() error {
	var firstErr error
	for _, dir := range sharedTempDirs() {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // best effort; RemoveAll reports what actually fails
			}
			if d.IsDir() {
				_ = os.Chmod(p, 0o700)
			} else if d.Type().IsRegular() {
				_ = os.Chmod(p, 0o600)
			}
			return nil
		})
		if err := os.RemoveAll(dir); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
