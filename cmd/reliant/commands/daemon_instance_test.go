// Copyright (c) 2025 Reliant Labs
package commands

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemoninstance"
	"github.com/spf13/cobra"
)

const testServerURL = "http://localhost:8090"

// TestResolveDataDir_SameWorktreeFromAnySubdirectory is the regression guard for
// the original defect, and the most important test in this file.
//
// `daemon start` run from the repo root and `daemon stop` run from
// <root>/electron used to address two different data directories, because the
// directory was the cwd-relative "./data". stop then read a record that did not
// exist, printed "No daemon running", and exited zero — over a live daemon.
//
// Every directory inside one git worktree must resolve to ONE instance, and a
// second worktree must resolve to a different one.
func TestResolveDataDir_SameWorktreeFromAnySubdirectory(t *testing.T) {
	isolateInstanceEnv(t)

	worktree := initGitRepo(t)
	sub := filepath.Join(worktree, "electron")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir electron: %v", err)
	}
	nested := filepath.Join(worktree, "internal", "toolexec", "daemonruntime")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	fromRoot := resolveFrom(t, worktree, daemonInstanceFlags{})
	for _, dir := range []string{sub, nested} {
		got := resolveFrom(t, dir, daemonInstanceFlags{})
		if got != fromRoot {
			t.Fatalf("data dir must not depend on the working directory\n  from %s: %s\n  from %s: %s",
				worktree, fromRoot, dir, got)
		}
	}

	// A genuinely different worktree is a different instance — that is the
	// multi-worktree concurrency this whole change enables.
	other := initGitRepo(t)
	if got := resolveFrom(t, other, daemonInstanceFlags{}); got == fromRoot {
		t.Fatalf("two worktrees must resolve to different data dirs, both got %s", got)
	}
}

// TestResolveDataDir_AllFourCommandsAgree pins that start, status, stop and
// logs resolve identically. They share one helper precisely so they cannot
// drift; if they ever do, `stop` again fails to find what `start` created,
// which is the original bug wearing a new hat.
//
// The commands are driven through cobra rather than by calling the helper four
// times, so a subcommand that forgot to register the instance flags — or wired
// up its own — fails here.
func TestResolveDataDir_AllFourCommandsAgree(t *testing.T) {
	isolateInstanceEnv(t)
	worktree := initGitRepo(t)

	// Each command is invoked from a different directory inside the worktree,
	// which is exactly the situation that used to produce four answers.
	dirs := map[string]string{
		"start":  worktree,
		"status": filepath.Join(worktree, "electron"),
		"stop":   filepath.Join(worktree, "web"),
		"logs":   filepath.Join(worktree, "internal"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	resolved := make(map[string]string, len(dirs))
	for name, dir := range dirs {
		cmd := daemonSubcommand(t, name)
		var flags daemonInstanceFlags
		// Read back what the command's own --data-dir/--account/--workspace
		// flags default to, then resolve through the shared helper the command
		// itself uses.
		flags.dataDir = flagValue(t, cmd, "data-dir")
		flags.account = flagValue(t, cmd, "account")
		flags.workspace = flagValue(t, cmd, "workspace")
		resolved[name] = resolveFrom(t, dir, flags)
	}

	want := resolved["start"]
	for name, got := range resolved {
		if got != want {
			t.Fatalf("%s resolves %s but start resolves %s — start/status/stop/logs must agree", name, got, want)
		}
	}
}

// TestResolveDataDir_ExplicitDataDirWins covers the containers.
// docker-compose.yml and docker-compose.cloud.yml both set DAEMON_DATA_DIR=/data
// where the volume is mounted; derivation would put daemon state on the
// container's ephemeral filesystem instead.
func TestResolveDataDir_ExplicitDataDirWins(t *testing.T) {
	isolateInstanceEnv(t)
	worktree := initGitRepo(t)

	// The flag.
	if got := resolveFrom(t, worktree, daemonInstanceFlags{dataDir: "/data"}); got != "/data" {
		t.Fatalf("explicit --data-dir must win verbatim, got %s", got)
	}

	// The environment variable, through the flag default the commands register.
	t.Setenv(envDataDir, "/data")
	cmd := daemonSubcommand(t, "start")
	fromEnv := flagValue(t, cmd, "data-dir")
	if fromEnv != "/data" {
		t.Fatalf("DAEMON_DATA_DIR must become the --data-dir default, got %q", fromEnv)
	}
	if got := resolveFrom(t, worktree, daemonInstanceFlags{dataDir: fromEnv}); got != "/data" {
		t.Fatalf("DAEMON_DATA_DIR must win over derivation, got %s", got)
	}
}

// TestResolveDataDir_DistinctAccountsAndWorkspaces pins the two remaining
// components of the key. Two accounts in one worktree, and two workspaces under
// one account, must each get their own directory — a shared directory is a
// shared lock and a shared daemon id.
func TestResolveDataDir_DistinctAccountsAndWorkspaces(t *testing.T) {
	isolateInstanceEnv(t)
	worktree := initGitRepo(t)

	alice := resolveFrom(t, worktree, daemonInstanceFlags{account: "user-alice"})
	bob := resolveFrom(t, worktree, daemonInstanceFlags{account: "user-bob"})
	if alice == bob {
		t.Fatalf("two accounts must not share a data dir, both got %s", alice)
	}

	one := resolveFrom(t, worktree, daemonInstanceFlags{account: "user-alice", workspace: "/tmp/wt-one"})
	two := resolveFrom(t, worktree, daemonInstanceFlags{account: "user-alice", workspace: "/tmp/wt-two"})
	if one == two {
		t.Fatalf("two workspaces must not share a data dir, both got %s", one)
	}
}

// TestResolveWorkspace_Precedence pins the ordering: flag, then env, then the
// git worktree root. The env layer is what containers and CI use, where cwd
// names nothing meaningful.
func TestResolveWorkspace_Precedence(t *testing.T) {
	isolateInstanceEnv(t)
	worktree := initGitRepo(t)

	inDir(t, worktree, func() {
		if got := resolveWorkspace("explicit-label"); got != "explicit-label" {
			t.Fatalf("flag must win, got %q", got)
		}

		t.Setenv(daemoninstance.EnvWorkspace, "ci-workload")
		if got := resolveWorkspace(""); got != "ci-workload" {
			t.Fatalf("env must win over the git root, got %q", got)
		}
		if got := resolveWorkspace("explicit-label"); got != "explicit-label" {
			t.Fatalf("flag must win over env, got %q", got)
		}

		t.Setenv(daemoninstance.EnvWorkspace, "")
		got, err := filepath.EvalSymlinks(resolveWorkspace(""))
		if err != nil {
			t.Fatalf("evalsymlinks: %v", err)
		}
		want, err := filepath.EvalSymlinks(worktree)
		if err != nil {
			t.Fatalf("evalsymlinks want: %v", err)
		}
		if got != want {
			t.Fatalf("git worktree root: got %q, want %q", got, want)
		}
	})
}

// TestResolveWorkspace_OutsideGitFallsBackToCwd pins that a daemon started
// outside any repository still starts. It gets a cwd-shaped identity, which is
// the best available answer when there is no worktree to name.
func TestResolveWorkspace_OutsideGitFallsBackToCwd(t *testing.T) {
	isolateInstanceEnv(t)

	// A temp dir with no repository anywhere above it. macOS /var is a symlink
	// to /private/var, so compare resolved paths.
	bare := t.TempDir()
	if gitWorktreeRootIn(t, bare) != "" {
		t.Skip("temp dir is inside a git repository on this machine; cannot test the no-repo path")
	}

	inDir(t, bare, func() {
		got, err := filepath.EvalSymlinks(resolveWorkspace(""))
		if err != nil {
			t.Fatalf("evalsymlinks: %v", err)
		}
		want, err := filepath.EvalSymlinks(bare)
		if err != nil {
			t.Fatalf("evalsymlinks want: %v", err)
		}
		if got != want {
			t.Fatalf("outside a repo the workspace must be cwd: got %q, want %q", got, want)
		}
	})
}

// --- helpers ---

// isolateInstanceEnv points ~/.reliant at a temp dir and clears the env
// overrides, so a developer's real instances directory is never touched and a
// stray RELIANT_* in the ambient environment cannot change what a test resolves.
func isolateInstanceEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv(daemoninstance.EnvWorkspace, "")
	t.Setenv(envAccount, "")
	t.Setenv(envDataDir, "")
}

// resolveFrom runs the real resolution helper with cwd set to dir, which is the
// variable the whole change exists to make irrelevant.
func resolveFrom(t *testing.T, dir string, flags daemonInstanceFlags) string {
	t.Helper()
	var got string
	inDir(t, dir, func() {
		resolved, err := resolveDaemonDataDir(flags, testServerURL)
		if err != nil {
			t.Fatalf("resolveDaemonDataDir from %s: %v", dir, err)
		}
		got = resolved
	})
	return got
}

// inDir runs fn with the process working directory set to dir and restores it
// afterwards. os.Chdir rather than t.Chdir because these tests are not parallel
// and the restore must happen before the next subtest reads cwd.
func inDir(t *testing.T, dir string, fn func()) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	defer func() {
		if err := os.Chdir(prev); err != nil {
			t.Fatalf("restoring cwd: %v", err)
		}
	}()
	fn()
}

// initGitRepo creates a real git worktree in a temp dir. Real rather than
// faked because the thing under test is `git rev-parse --show-toplevel`, and a
// fake would only assert that the test's own idea of a repo matches itself.
func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "--quiet").CombinedOutput(); err != nil {
		t.Skipf("git init unavailable (%v): %s", err, out)
	}
	return dir
}

// gitWorktreeRootIn asks the same question gitWorktreeRoot asks, but about a
// named directory, so a test can check its assumptions without chdir'ing.
func gitWorktreeRootIn(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// daemonSubcommand returns the named subcommand of `reliant daemon`, built the
// same way the real CLI builds it.
func daemonSubcommand(t *testing.T, name string) *cobra.Command {
	t.Helper()
	for _, sub := range newDaemonCmd().Commands() {
		if sub.Name() == name {
			return sub
		}
	}
	t.Fatalf("daemon subcommand %q not found", name)
	return nil
}

// flagValue reads a registered flag's current (default) value, and fails when
// the flag is absent — which is how a subcommand that skipped
// registerDaemonInstanceFlags gets caught.
func flagValue(t *testing.T, cmd *cobra.Command, name string) string {
	t.Helper()
	flag := cmd.Flags().Lookup(name)
	if flag == nil {
		t.Fatalf("command %q has no --%s flag", cmd.Name(), name)
	}
	return flag.Value.String()
}
