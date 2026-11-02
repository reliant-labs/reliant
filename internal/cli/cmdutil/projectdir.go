package cmdutil

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// The resolution root: the directory project lookup starts from.
//
// Every forge command locates its project by looking at — or walking up from —
// one directory. Historically that directory was always os.Getwd(), which made
// the process-global CWD the only way to target a project. That is fine for a
// one-shot binary and wrong for an in-process embedder: forge/cli exports
// NewRootCmd precisely so other cobra CLIs can mount forge as a subcommand, and
// a server-shaped embedder (reliant's daemon) serves commands for any project on
// the machine. os.Chdir is the only CWD-based answer available to such a caller,
// and it is unsafe under concurrency AND changes the meaning of every relative
// path in flight elsewhere in the process.
//
// So the root is injectable here instead, at the one place os.Getwd() is
// consulted. --project-dir / -C on the root command sets it.
//
// CONCURRENCY — read this before relying on it. The override below is
// PROCESS-GLOBAL, not per-invocation. The mutex makes concurrent access
// memory-safe (no data race, no torn read), but it does NOT make two concurrent
// forge invocations carrying DIFFERENT -C values independent: the second Set
// wins for both. An in-process embedder running commands concurrently must
// still serialize forge invocations.
//
// What this does buy over os.Chdir, which is the reason it exists: serializing
// forge calls behind a mutex is now sufficient. Under os.Chdir it was not —
// chdir also broke unrelated relative-path work on every OTHER goroutine in the
// process, which no amount of forge-side locking could contain.
//
// Making this genuinely per-invocation requires threading the root through the
// ~15 call sites of findProjectConfigFile/ProjectRoot/FindProjectRoot (or a
// resolver on the cobra command's context, which those leaf helpers do not
// currently receive). See projectDirConcurrency in project_dir_test.go.
var (
	projectDirMu       sync.RWMutex
	projectDirOverride string // absolute, or "" meaning "use os.Getwd()"
)

// SetProjectDir installs dir as the resolution root for subsequent project
// lookups. A relative dir resolves against the current working directory. An
// empty dir clears the override, restoring os.Getwd() resolution.
//
// It rejects a path that is not an existing directory, so a mistyped -C fails
// loudly here rather than degrading into a confusing "no project found" (or,
// worse, a silent fallback to "." at one of the call sites that ignores the
// lookup error).
func SetProjectDir(dir string) error {
	if dir == "" {
		projectDirMu.Lock()
		projectDirOverride = ""
		projectDirMu.Unlock()
		return nil
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve project dir %q: %w", dir, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("project dir %q: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("project dir %q is not a directory", dir)
	}

	projectDirMu.Lock()
	projectDirOverride = abs
	projectDirMu.Unlock()
	return nil
}

// ProjectDirOverride reports the installed resolution root, if any.
func ProjectDirOverride() (string, bool) {
	projectDirMu.RLock()
	defer projectDirMu.RUnlock()
	return projectDirOverride, projectDirOverride != ""
}

// ResolutionRoot returns the directory project lookup should start from: the
// --project-dir override when one is installed, otherwise os.Getwd(). This is
// the single seam every project-locating helper goes through.
func ResolutionRoot() (string, error) {
	if dir, ok := ProjectDirOverride(); ok {
		return dir, nil
	}
	return os.Getwd()
}
