// Copyright (c) 2025 Reliant Labs
package worktree

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain gives the whole test binary a private HOME before any test runs.
//
// The reclaim tests drive the daemon's real handlers (daemonruntime's
// registry), whose worktree root is <HOME>/.reliant/worktrees. One HOME for
// the binary lets them share that root and run in parallel, each under a
// directory of its own (reclaimEnv.dir); a HOME per test needs t.Setenv,
// which forbids t.Parallel. It also keeps every test here off the
// developer's own ~/.reliant and ~/.gitconfig.
func TestMain(m *testing.M) {
	os.Exit(runWithPrivateHome(m))
}

func runWithPrivateHome(m *testing.M) int {
	home, err := os.MkdirTemp("", "worktree-handler-test-home-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "worktree handler tests: cannot create a private HOME:", err)
		return 1
	}
	defer os.RemoveAll(home)
	// Resolved, so the paths the daemon reports match the ones tests build
	// (macOS's /var is /private/var).
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	// No automatic maintenance: `git maintenance run --auto --detach` outlives
	// the commit that starts it, with its working directory in the checkout,
	// and a sweep would hold that checkout as in use.
	gitconfig := filepath.Join(home, ".gitconfig")
	if err := os.WriteFile(gitconfig, []byte("[maintenance]\n\tauto = false\n[gc]\n\tauto = 0\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "worktree handler tests:", err)
		return 1
	}
	for k, v := range map[string]string{
		"HOME":              home,
		"XDG_CONFIG_HOME":   filepath.Join(home, ".config"),
		"GIT_CONFIG_GLOBAL": gitconfig,
	} {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintln(os.Stderr, "worktree handler tests:", err)
			return 1
		}
	}
	return m.Run()
}
