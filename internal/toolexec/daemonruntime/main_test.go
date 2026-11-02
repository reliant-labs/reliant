// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	forgecli "github.com/reliant-labs/forge/cli"
	"github.com/reliant-labs/reliant/internal/credsandbox"
	"github.com/reliant-labs/reliant/internal/forgepin"
	"github.com/reliant-labs/reliant/internal/heavyjob"
)

// Start on a managed daemon (Host.DaemonType "managed") rewrites the GLOBAL
// git config. Run inside a Reliant workspace, where a real HOME is set, this
// package's tests that start a managed daemon would otherwise point the
// developer's own ~/.gitconfig at a test binary go deletes moments later,
// breaking every later git fetch/push on the machine.
//
// So TestMain gives every test a private HOME and git config before any test
// runs. (Whether Start behaves as a pod is its StartOptions.Host, never the
// environment the test binary inherited.)
//
// That HOME is also what lets the git-heavy tests run in parallel. Every git
// process costs a fork and an exec, which on a loaded machine is hundreds of
// milliseconds, and a worktree test runs dozens. A test that needs its own
// HOME must t.Setenv it, and t.Setenv forbids t.Parallel — so a parallel test
// shares this one and keeps its checkouts apart by workspace ID instead
// (uniqueWorkspaceID).

// testRoot is the directory runIsolated creates for the whole test binary;
// fixtures built once per binary (cloneOrigin, copyOfRepoTemplate) live
// under it.
var testRoot string

func TestMain(m *testing.M) {
	// This test binary also stands in for the reliant binary as the
	// heavy-slot wrapper (cmd_exec_heavy_test.go).
	heavyjob.Dispatch()
	// credentials: none runs in a sandbox that is this binary re-executed; on a
	// host that does not allow it the sandbox is turned off, so the tests that
	// only use credentials: none still check what they were written to check.
	credsandbox.DispatchForTest()
	// No test resolves, builds or runs a real pinned forge for the framework
	// memory (forge_memory.go): every project is served by the embedded forge
	// unless a test installs fakes (useFakePinnedForges).
	pinnedForge = &forgepin.Source{Resolve: func(context.Context, string, string, io.Writer) (forgecli.PinnedForge, bool, error) {
		return forgecli.PinnedForge{}, false, nil
	}}
	os.Exit(runIsolated(m))
}

func runIsolated(m *testing.M) int {
	root, err := os.MkdirTemp("", "daemonruntime-test-home-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "daemonruntime tests: cannot create isolated home:", err)
		return 1
	}
	defer os.RemoveAll(root)
	// Resolved, so the shared HOME is the path git reports (macOS's /var
	// is /private/var) and tests can compare it with git's output.
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	testRoot = root

	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "daemonruntime tests:", err)
		return 1
	}
	// A real workspace's repositories live here; the managed heal scans it.
	managedProjectsDir = filepath.Join(root, "managed-projects")
	// Pushes go where the production check allows, plus the fixtures a test
	// opted in to (allowFileRemotes), so the publish tests can run in parallel.
	publishURLAllowed = allowTestRemotes(publishURLAllowed)

	gitconfig := filepath.Join(home, ".gitconfig")
	if err := os.WriteFile(gitconfig, []byte(noAutoMaintenanceConfig), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "daemonruntime tests:", err)
		return 1
	}
	for k, v := range map[string]string{
		"HOME":                home,
		"XDG_CONFIG_HOME":     filepath.Join(home, ".config"),
		"GIT_CONFIG_GLOBAL":   gitconfig,
		"GIT_CONFIG_SYSTEM":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
	} {
		os.Setenv(k, v)
	}
	return m.Run()
}

// Test git never runs automatic maintenance.
//
// Since git 2.5x, every commit (and every push a repo receives) ends with
// `git maintenance run --auto --detach`: a background process that outlives
// the git command, creates .git/objects/maintenance.lock, and deletes it
// whenever it finishes. A fixture copied with a directory walk then meets a
// file that vanishes between the listing and the lstat ("lstat
// .../repo-template-*/.git/objects/maintenance.lock: no such file or
// directory"), and a t.TempDir() can be written to while it is being removed.
// git 2.43 ran the same maintenance in the foreground, which is why this only
// ever failed in CI. Reproduced with git 2.55: 6 failures in one -count=20
// run of the worktree tests, every one that lock; none with this config.
//
// noAutoMaintenanceConfig turns it off in a git config file, and
// noAutoMaintenance on one command line, for a build that deliberately
// ignores the process's git config. -c reaches every git the command spawns,
// a receive-pack on the far side of a local push included.
const noAutoMaintenanceConfig = "[maintenance]\n\tauto = false\n[gc]\n\tauto = 0\n"

// noAutoMaintenance prefixes git args with the -c options that turn
// automatic maintenance off for that one command.
func noAutoMaintenance(args ...string) []string {
	return append([]string{"-c", "maintenance.auto=false", "-c", "gc.auto=0"}, args...)
}

// testWorktreePath is where worktree.create puts workspaceID under the
// shared $HOME.
func testWorktreePath(t *testing.T, workspaceID string) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, ".reliant", "worktrees", workspaceID)
}

// uniqueWorkspaceID suffixes id with the test's name and a sequence number,
// so a parallel test's worktrees under the shared $HOME/.reliant/worktrees
// never meet another test's, nor an earlier run's under -count.
func uniqueWorkspaceID(t *testing.T, id string) string {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	return fmt.Sprintf("%s-%s-%d", id, name, workspaceSeq.Add(1))
}

var workspaceSeq atomic.Int64
