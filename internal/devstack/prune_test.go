package devstack

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
)

// pruneGit runs a git command in dir, failing the test on error. Separate
// from resolve_git_test.go's `git` helper (same package, but keeping this
// self-contained avoids a cross-file rename hazard if that helper changes).
func pruneGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// pruneInitRepo makes a primary git checkout in dir with one commit, local
// user.email/user.name only (never --global — this machine's git config is
// shared across concurrent agents).
func pruneInitRepo(t *testing.T, dir string) {
	t.Helper()
	pruneGit(t, dir, "init", "-q")
	pruneGit(t, dir, "config", "user.email", "forge-prune-test@example.com")
	pruneGit(t, dir, "config", "user.name", "forge prune test")
	pruneGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
}

// registerStackKey marks key as a dev-stack entry in projectDir's registry,
// exactly as AllocateBlock does when it is asked for a key that equals the
// active worktree.
func registerStackKey(t *testing.T, projectDir, key string, base int) int {
	t.Helper()
	setActiveWorktree(t, key)
	port, err := AllocatePort(projectDir, base, key)
	if err != nil {
		t.Fatalf("AllocatePort(%q): %v", key, err)
	}
	return port
}

// registerPlainKey marks key as a non-stack port-block entry (e.g. "prod"),
// matching how a KCL expression that just wants a port allocates one.
func registerPlainKey(t *testing.T, projectDir, key string, base int) int {
	t.Helper()
	setActiveWorktree(t, "") // no active worktree equals this key -> not a stack
	port, err := AllocatePort(projectDir, base, key)
	if err != nil {
		t.Fatalf("AllocatePort(%q): %v", key, err)
	}
	return port
}

func sortedKeys(pruned []PrunedBlock) []string {
	keys := make([]string, len(pruned))
	for i, p := range pruned {
		keys[i] = p.Key
	}
	sort.Strings(keys)
	return keys
}

// TestPruneReclaimsDeadWorktreeStackKey is the core case: a worktree removed
// from disk (its dir deleted, without `git worktree remove`, so git reports
// it "prunable") has its stack key reclaimed.
func TestPruneReclaimsDeadWorktreeStackKey(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	deadDir := filepath.Join(t.TempDir(), "wt-dead")
	pruneGit(t, primary, "worktree", "add", "-q", "-b", "dead", deadDir)

	registerStackKey(t, primary, "wt-dead", 28080)

	if err := os.RemoveAll(deadDir); err != nil {
		t.Fatalf("remove worktree dir: %v", err)
	}

	pruned, err := Prune(primary, false)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(pruned) != 1 || pruned[0].Key != "wt-dead" {
		t.Fatalf("Prune reclaimed = %+v, want [{wt-dead ...}]", pruned)
	}

	stacks, err := ListStacks(primary)
	if err != nil {
		t.Fatalf("ListStacks: %v", err)
	}
	if len(stacks) != 0 {
		t.Errorf("wt-dead still in registry after prune: %v", stacks)
	}
}

// TestPruneNeverReclaimsPlainPortBlockKey is the regression lock for the
// single most important correctness property: a plain port-block key (e.g.
// prod's reliant-web dev-server port) is not tied to any worktree, so it must
// survive a prune even though nothing on disk backs it either.
func TestPruneNeverReclaimsPlainPortBlockKey(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	registerPlainKey(t, primary, "prod", 3000)

	pruned, err := Prune(primary, false)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(pruned) != 0 {
		t.Fatalf("Prune reclaimed a non-stack key: %+v — this would move prod's live port", pruned)
	}

	list, err := List(primary)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Key != "prod" || list[0].Stack {
		t.Fatalf("prod entry mutated by prune: %+v", list)
	}
}

// TestPruneNeverTouchesDefaultKey: the default key "" (block 0) is never
// stored by AllocatePort in the first place, but Prune must not choke on or
// otherwise disturb the registry when only the default has ever been used.
func TestPruneNeverTouchesDefaultKey(t *testing.T) {
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	setActiveWorktree(t, "")
	if _, err := AllocatePort(primary, 3000, ""); err != nil {
		t.Fatalf("AllocatePort(\"\"): %v", err)
	}

	pruned, err := Prune(primary, false)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(pruned) != 0 {
		t.Fatalf("Prune touched the default key: %+v", pruned)
	}
	if list, _ := List(primary); len(list) != 0 {
		t.Fatalf("default key \"\" got persisted into the registry: %v", list)
	}
}

// TestPruneKeepsLiveWorktreeKey: a worktree that still exists on disk is
// never reclaimed, whether or not the process still has it recorded as the
// active worktree.
func TestPruneKeepsLiveWorktreeKey(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	liveDir := filepath.Join(t.TempDir(), "wt-live")
	pruneGit(t, primary, "worktree", "add", "-q", "-b", "live", liveDir)
	registerStackKey(t, primary, "wt-live", 28080)
	setActiveWorktree(t, "") // active options don't matter to Prune; only disk does

	pruned, err := Prune(primary, false)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(pruned) != 0 {
		t.Fatalf("Prune reclaimed a live worktree's key: %+v", pruned)
	}
	stacks, _ := ListStacks(primary)
	if len(stacks) != 1 || stacks[0] != "wt-live" {
		t.Fatalf("live worktree key missing after prune: %v", stacks)
	}
}

// TestPruneMixedRegistryOnlyReclaimsDead combines all four kinds of entry in
// one registry to prove Prune sorts them correctly against each other, not
// just in isolation.
func TestPruneMixedRegistryOnlyReclaimsDead(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	liveDir := filepath.Join(t.TempDir(), "wt-live")
	pruneGit(t, primary, "worktree", "add", "-q", "-b", "live", liveDir)
	registerStackKey(t, primary, "wt-live", 28080)

	deadDir := filepath.Join(t.TempDir(), "wt-dead")
	pruneGit(t, primary, "worktree", "add", "-q", "-b", "dead", deadDir)
	registerStackKey(t, primary, "wt-dead", 29080)
	if err := os.RemoveAll(deadDir); err != nil {
		t.Fatalf("remove worktree dir: %v", err)
	}

	registerPlainKey(t, primary, "prod", 3000)
	setActiveWorktree(t, "")
	if _, err := AllocatePort(primary, 4000, ""); err != nil {
		t.Fatalf("AllocatePort(\"\"): %v", err)
	}

	pruned, err := Prune(primary, false)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if got := sortedKeys(pruned); len(got) != 1 || got[0] != "wt-dead" {
		t.Fatalf("Prune reclaimed = %v, want [wt-dead]", got)
	}

	list, err := List(primary)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	remaining := make(map[string]bool)
	for _, b := range list {
		remaining[b.Key] = true
	}
	if !remaining["wt-live"] || !remaining["prod"] {
		t.Fatalf("prune removed a key it must not have: remaining=%v", list)
	}
	if remaining["wt-dead"] {
		t.Fatalf("wt-dead survived prune: %v", list)
	}
}

// TestPruneDryRunChangesNothing: dry-run reports the same reclaimable set but
// leaves the registry untouched — a second dry-run (or a real run) sees the
// identical state.
func TestPruneDryRunChangesNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	deadDir := filepath.Join(t.TempDir(), "wt-dead")
	pruneGit(t, primary, "worktree", "add", "-q", "-b", "dead", deadDir)
	registerStackKey(t, primary, "wt-dead", 28080)
	if err := os.RemoveAll(deadDir); err != nil {
		t.Fatalf("remove worktree dir: %v", err)
	}

	pruned, err := Prune(primary, true)
	if err != nil {
		t.Fatalf("Prune(dryRun): %v", err)
	}
	if len(pruned) != 1 || pruned[0].Key != "wt-dead" {
		t.Fatalf("dry-run Prune reported = %+v, want [{wt-dead ...}]", pruned)
	}

	stacks, err := ListStacks(primary)
	if err != nil {
		t.Fatalf("ListStacks: %v", err)
	}
	if len(stacks) != 1 || stacks[0] != "wt-dead" {
		t.Fatalf("dry-run mutated the registry: stacks = %v, want [wt-dead] still present", stacks)
	}
}

// TestPruneNonGitDirReclaimsNothing is the conservative-failure lock: when
// live-worktree enumeration cannot be trusted (not a git checkout here; the
// same path a git-not-installed machine would take), Prune must return an
// error and touch nothing rather than guess.
func TestPruneNonGitDirReclaimsNothing(t *testing.T) {
	dir := t.TempDir() // deliberately not git-init'ed

	// Seed a registry entry the way a stale copy of .forge/blocks.json might
	// exist without git being available to double-check it (e.g. the repo
	// was deleted out from under the project dir).
	setActiveWorktree(t, "wt-orphan")
	if _, err := AllocatePort(dir, 28080, "wt-orphan"); err != nil {
		t.Fatalf("seed AllocatePort: %v", err)
	}
	setActiveWorktree(t, "")

	pruned, err := Prune(dir, false)
	if err == nil {
		t.Fatalf("Prune succeeded against a non-git dir; want an error and no reclaiming, got %+v", pruned)
	}
	if pruned != nil {
		t.Fatalf("Prune returned reclaimed entries alongside an error: %+v", pruned)
	}

	stacks, err := ListStacks(dir)
	if err != nil {
		t.Fatalf("ListStacks: %v", err)
	}
	if len(stacks) != 1 || stacks[0] != "wt-orphan" {
		t.Fatalf("registry mutated despite a failed enumeration: stacks = %v", stacks)
	}
}
