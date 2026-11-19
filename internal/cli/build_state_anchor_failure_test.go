package cli

// An UNREADABLE LEDGER IS AN ERROR, NEVER "no anchor".
//
// resolveFreshnessAnchor answers two different questions with one bool, and
// the defect was that it collapsed them. "This env is unbound and the tree is
// dirty, so there is no commit to measure against" is a legitimate stand-down
// — nothing is wrong, and the guard has nothing to compare. "The ledger could
// not be opened, or its promotion log would not decode" is not: the guard's
// whole subject could not be read, and the one thing that must not follow is
// the guard quietly passing.
//
// Both reported enforce=false, so the second case DEPLOYED. A corrupt
// promotions line, a ledger directory whose permissions changed, a project
// whose forge.yaml name went missing — each one silently disabled the
// stale-image guard on the real-money path, and the deploy printed nothing
// about it. That is strictly worse than the false refusal the release anchor
// exists to fix: a refusal is read and acted on, where this is invisible.
//
// So the signature gains an error, and these tests pin the three states apart:
// an anchor, an honest stand-down, and a failure that must stop the deploy.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveFreshnessAnchor_UnreadableLedgerIsAnError is the defect. The
// env is bound to a release, so the ledger IS the anchor — and a ledger whose
// promotion log does not decode must fail loudly rather than report "nothing
// to compare".
func TestResolveFreshnessAnchor_UnreadableLedgerIsAnError(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	dir := newGitRepo(t)
	gitignoreForgeState(t, dir)
	bindEnvToRelease(t, dir, "prod", "v1.4.0", gitHeadSHA(t, dir))

	corruptPromotionLog(t, dir, "prod")

	_, _, err := resolveFreshnessAnchor(context.Background(), dir, "prod")
	if err == nil {
		t.Fatal("a promotion log that does not decode must be an error: reporting " +
			"'no anchor' silently disables the stale-image guard on the deploy path")
	}
	if !strings.Contains(err.Error(), "prod") {
		t.Fatalf("the error must name the env whose ledger could not be read; got: %v", err)
	}
}

// TestResolveDeployImageTag_UnreadableLedgerRefusesTheDeploy is the same
// defect at the call site, which is where it mattered: a recorded build and
// an unreadable ledger used to deploy.
func TestResolveDeployImageTag_UnreadableLedgerRefusesTheDeploy(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	dir := useTestLedger(t, newGitRepo(t))
	gitignoreForgeState(t, dir)
	head := gitHeadSHA(t, dir)
	bindEnvToRelease(t, dir, "prod", "v1.4.0", head)

	if err := WriteBuildState(dir, "prod", BuildState{
		Tag: "ship-1", Image: "app", Commit: head, GitTag: "v1.4.0",
	}); err != nil {
		t.Fatalf("write state: %v", err)
	}
	corruptPromotionLog(t, dir, "prod")

	_, _, _, err := resolveDeployImageTag(context.Background(), dir, "prod", "", false)
	if err == nil {
		t.Fatal("an unreadable ledger must refuse the deploy, not deploy with the guard off")
	}
}

// TestResolveFreshnessAnchor_UnboundDirtyTreeStandsDownWithoutError is the
// other half of the contract, and the reason the fix is an error rather than
// "always enforce". An unbound env in a dirty tree has NO commit the build
// could be measured against, which is a legitimate stand-down and must stay
// one — turning it into an error would refuse every deploy from a working
// tree with an edit in it.
func TestResolveFreshnessAnchor_UnboundDirtyTreeStandsDownWithoutError(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	dir := useTestLedger(t, newGitRepo(t))
	gitignoreForgeState(t, dir)
	// Dirty a TRACKED file: the tree now has no single HEAD the build can
	// be compared against.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("mid-edit"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, enforce, err := resolveFreshnessAnchor(context.Background(), dir, "prod")
	if err != nil {
		t.Fatalf("an unbound env in a dirty tree is a stand-down, not a failure; got: %v", err)
	}
	if enforce {
		t.Fatal("a dirty tree has no HEAD anchor, so the guard must stand down")
	}
}

// TestResolveFreshnessAnchor_ReleaseWithoutCommitStandsDownWithoutError: a
// release cut on a non-git tree records no commit, so there is nothing to
// anchor to. The ledger read SUCCEEDED and simply holds no commit — a
// stand-down, not a failure, and the distinction is the whole point of the
// change.
func TestResolveFreshnessAnchor_ReleaseWithoutCommitStandsDownWithoutError(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	dir := useTestLedger(t, newGitRepo(t))
	gitignoreForgeState(t, dir)
	bindEnvToRelease(t, dir, "prod", "v1.4.0", "")

	_, enforce, err := resolveFreshnessAnchor(context.Background(), dir, "prod")
	if err != nil {
		t.Fatalf("a commit-less release is a stand-down, not a failure; got: %v", err)
	}
	if enforce {
		t.Fatal("a release with no recorded commit is no anchor, so the guard must stand down")
	}
}

// TestResolveFreshnessAnchor_BoundReleaseStillAnchors proves the fix did not
// make the ordinary path error: a readable ledger with a bound release still
// returns that release's commit as the anchor.
func TestResolveFreshnessAnchor_BoundReleaseStillAnchors(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	dir := useTestLedger(t, newGitRepo(t))
	gitignoreForgeState(t, dir)
	builtCommit := gitHeadSHA(t, dir)
	bindEnvToRelease(t, dir, "prod", "v1.4.0", builtCommit)

	anchor, enforce, err := resolveFreshnessAnchor(context.Background(), dir, "prod")
	if err != nil {
		t.Fatalf("a readable ledger must not error: %v", err)
	}
	if !enforce {
		t.Fatal("a bound release with a recorded commit IS an anchor")
	}
	if anchor.Commit != builtCommit || anchor.Release != "v1.4.0" {
		t.Fatalf("anchor = %+v, want commit %s release v1.4.0", anchor, builtCommit)
	}
}

// corruptPromotionLog writes a line the promotion decoder cannot read into
// the env's log.
//
// It corrupts the REAL file the store would read, rather than injecting a
// failing fake, because the property under test is what the production read
// path does with a file it cannot parse — and a fake store would be asserting
// that resolveFreshnessAnchor propagates an error some other object chose to
// return, which is a weaker claim than the defect needs.
func corruptPromotionLog(t *testing.T, dir, env string) {
	t.Helper()
	store := testStore(t, dir)
	path := filepath.Join(store.Dir(), "promotions", env+".jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("precondition: %s should hold %s's promotions: %v", path, env, err)
	}
	if err := os.WriteFile(path, []byte("{this is not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Prove the store really cannot read it, so a later refactor that
	// changes the file layout fails HERE rather than silently making every
	// assertion above vacuous.
	if _, _, err := store.CurrentPromotion(env); err == nil {
		t.Fatalf("precondition: %s should no longer decode", path)
	}
}
