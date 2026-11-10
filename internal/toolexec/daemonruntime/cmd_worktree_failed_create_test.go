package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/worktreeerr"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// reliant #2413: a worktree.create whose answer never reached the server
// leaves a finished checkout, locked for the FAILED row's id, holding its
// branch. worktree.remove_failed_create removes it only when it is provably
// that untouched leftover. These tests make the leftover with the real
// handler (a create whose answer is simply dropped) in a real repository.

type leftover struct {
	repo, branch, worktreeID, workspaceRoot, path string
}

// newLeftover runs worktree.create for a multi-repo workspace (the checkout
// at <workspace>/app) and drops the answer, as a server that gave up would.
func newLeftover(t *testing.T) leftover {
	t.Helper()
	repo := diagnoseRepo(t)
	l := leftover{
		repo:       repo,
		branch:     "worktree/retry-" + strings.ReplaceAll(t.Name(), "/", "-"),
		worktreeID: uuid.NewString(),
	}
	workspaceID := uniqueWorkspaceID(t, "proj") + "/ws-" + l.worktreeID[:8]
	created := createCheckout(t, repo, workspaceID, l.branch, l.worktreeID)
	require.True(t, created.Success, "the first create must succeed: %s", created.Error)
	l.workspaceRoot = testWorktreePath(t, workspaceID)
	l.path = created.WorktreePath
	require.Equal(t, filepath.Join(l.workspaceRoot, "app"), l.path)
	return l
}

type createdCheckout struct {
	Success      bool               `json:"success"`
	WorktreePath string             `json:"worktree_path"`
	Error        string             `json:"error"`
	ErrorCode    worktreeerr.Reason `json:"error_code"`
}

func createCheckout(t *testing.T, repo, workspaceID, branch, worktreeID string) createdCheckout {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"project_path": repo,
		"workspace_id": workspaceID,
		"sub_path":     "app",
		"name":         "retry",
		"branch":       branch,
		"base_branch":  "main",
		"worktree_id":  worktreeID,
	})
	require.NoError(t, err)
	raw, err := handleWorktreeCreate(context.Background(), payload)
	require.NoError(t, err)
	var out createdCheckout
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

// removeFailed runs the command against TestMain's worktrees root with no
// process using anything: whether a host's unrelated processes can be
// inspected is not the test's to control (an uninspectable one rightly holds
// every checkout). TestRemoveFailedCreate_KeepsACheckoutAProcessUses runs the
// real inspection.
func removeFailed(t *testing.T, repos []string, creates ...failedCreate) removeFailedCreateResponse {
	t.Helper()
	root := testWorktreePath(t, "")
	require.NoError(t, os.MkdirAll(root, 0o755))
	noProcess := func(context.Context, string) string { return "" }
	return removeFailedCreates(context.Background(), root,
		removeFailedCreateRequest{RepoPaths: repos, Creates: creates}, noProcess)
}

// removeFailedViaHandler is the command as the daemon runs it, real process
// inspection included.
func removeFailedViaHandler(t *testing.T, repos []string, creates ...failedCreate) removeFailedCreateResponse {
	t.Helper()
	payload, err := json.Marshal(removeFailedCreateRequest{RepoPaths: repos, Creates: creates})
	require.NoError(t, err)
	raw, err := handleWorktreeRemoveFailedCreate(context.Background(), payload)
	require.NoError(t, err)
	var out removeFailedCreateResponse
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func (l leftover) create() failedCreate {
	return failedCreate{WorktreeID: l.worktreeID, Branch: l.branch}
}

func (l leftover) git(t *testing.T, dir string, args ...string) {
	t.Helper()
	require.NoError(t, fixtureGit(dir, args...))
}

// requireKept asserts the leftover is still there, still locked for its row,
// and the response says why.
func (l leftover) requireKept(t *testing.T, resp removeFailedCreateResponse, why string) {
	t.Helper()
	require.Len(t, resp.Checkouts, 1, "the locked checkout must be found and reported")
	c := resp.Checkouts[0]
	require.False(t, c.Removed, "a checkout that is not provably the untouched leftover must be kept")
	require.Contains(t, c.Kept, why)
	_, err := os.Stat(filepath.Join(l.path, ".git"))
	require.NoError(t, err, "the kept checkout must still be on disk")
	out, err := exec.Command("git", "-C", l.repo, "worktree", "list", "--porcelain").Output()
	require.NoError(t, err)
	require.Contains(t, string(out), "locked "+worktreereclaim.LockReason(l.worktreeID),
		"a kept checkout keeps its lock")
}

// The issue's regression: the create's answer is lost, the server marks the
// row FAILED, the cleanup runs, and the retry — a new row, the same branch —
// succeeds.
func TestRemoveFailedCreate_RemovesTheLeftoverAndTheRetrySucceeds(t *testing.T) {
	testkit.Slow(t, "creates real git checkouts and worktrees on disk")
	t.Parallel()
	l := newLeftover(t)

	// Without the cleanup the retry is refused, as in prod.
	retryWorkspace := uniqueWorkspaceID(t, "proj") + "/ws-retry"
	refused := createCheckout(t, l.repo, retryWorkspace, l.branch, uuid.NewString())
	require.False(t, refused.Success)
	require.Equal(t, worktreeerr.BranchCheckedOut, refused.ErrorCode)

	resp := removeFailed(t, []string{l.repo}, l.create())
	require.Empty(t, resp.Unchecked)
	require.Equal(t, []string{l.repo}, resp.Checked, "the repository listed is reported checked")
	require.Len(t, resp.Checkouts, 1)
	require.True(t, resp.Checkouts[0].Removed, "kept: %s", resp.Checkouts[0].Kept)

	_, err := os.Stat(l.workspaceRoot)
	require.True(t, os.IsNotExist(err), "the workspace directory the failed create made must be gone")
	require.NoError(t, fixtureGit(l.repo, "rev-parse", "--verify", "refs/heads/"+l.branch),
		"the branch is never deleted")

	retry := createCheckout(t, l.repo, retryWorkspace, l.branch, uuid.NewString())
	require.True(t, retry.Success, "the retry must succeed once the leftover is gone: %s", retry.Error)

	// Asked again, there is nothing locked for the FAILED row any more.
	again := removeFailed(t, []string{l.repo}, l.create())
	require.Empty(t, again.Checkouts)
}

// A checkout whose directory is already gone leaves only git's record, which
// holds the branch all the same; it is removed.
func TestRemoveFailedCreate_RemovesTheRecordOfAVanishedLeftover(t *testing.T) {
	testkit.Slow(t, "creates real git checkouts and worktrees on disk")
	t.Parallel()
	l := newLeftover(t)
	require.NoError(t, os.RemoveAll(l.path))

	resp := removeFailed(t, []string{l.repo}, l.create())
	require.Len(t, resp.Checkouts, 1)
	require.True(t, resp.Checkouts[0].Removed, "kept: %s", resp.Checkouts[0].Kept)
	out, err := exec.Command("git", "-C", l.repo, "worktree", "list", "--porcelain").Output()
	require.NoError(t, err)
	require.NotContains(t, string(out), l.worktreeID)
}

func TestRemoveFailedCreate_KeepsAModifiedCheckout(t *testing.T) {
	testkit.Slow(t, "creates real git checkouts and worktrees on disk")
	t.Parallel()
	l := newLeftover(t)
	require.NoError(t, os.WriteFile(filepath.Join(l.path, "f.txt"), []byte("edited\n"), 0o644))
	l.requireKept(t, removeFailed(t, []string{l.repo}, l.create()), "uncommitted, untracked or ignored files")
}

func TestRemoveFailedCreate_KeepsUntrackedFiles(t *testing.T) {
	testkit.Slow(t, "creates real git checkouts and worktrees on disk")
	t.Parallel()
	l := newLeftover(t)
	require.NoError(t, os.WriteFile(filepath.Join(l.path, "notes.md"), []byte("mine\n"), 0o644))
	l.requireKept(t, removeFailed(t, []string{l.repo}, l.create()), "uncommitted, untracked or ignored files")
}

// Ignored files are often data (.env, a local database): `git worktree
// remove` would delete them silently, so they keep the checkout.
func TestRemoveFailedCreate_KeepsIgnoredFiles(t *testing.T) {
	testkit.Slow(t, "creates real git checkouts and worktrees on disk")
	t.Parallel()
	l := newLeftover(t)
	common := filepath.Join(l.repo, ".git", "info")
	require.NoError(t, os.MkdirAll(common, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(common, "exclude"), []byte(".env\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(l.path, ".env"), []byte("SECRET=1\n"), 0o644))
	l.requireKept(t, removeFailed(t, []string{l.repo}, l.create()), "uncommitted, untracked or ignored files")
}

// A commit made in the checkout is work: it is on no other branch, and the
// retry's create would reset the branch under it.
func TestRemoveFailedCreate_KeepsACheckoutWithUnpushedCommits(t *testing.T) {
	testkit.Slow(t, "creates real git checkouts and worktrees on disk")
	t.Parallel()
	l := newLeftover(t)
	l.git(t, l.path, "commit", "-q", "--allow-empty", "-m", "work")
	l.requireKept(t, removeFailed(t, []string{l.repo}, l.create()), "used after it was created")
}

// A commit on the branch that nothing else holds keeps the checkout even
// when the checkout itself was never touched.
func TestRemoveFailedCreate_KeepsABranchHoldingCommitsNoOtherRefHas(t *testing.T) {
	testkit.Slow(t, "creates real git checkouts and worktrees on disk")
	t.Parallel()
	l := newLeftover(t)
	// A commit only the branch's reflog reaches: the branch moved to it and
	// back, from the main checkout.
	l.git(t, l.repo, "commit", "-q", "--allow-empty", "-m", "side")
	side := strings.TrimSpace(revParseIn(t, l.repo, "rev-parse", "HEAD"))
	l.git(t, l.repo, "reset", "-q", "--hard", "HEAD~1")
	tip := strings.TrimSpace(revParseIn(t, l.repo, "rev-parse", "refs/heads/"+l.branch))
	l.git(t, l.repo, "update-ref", "refs/heads/"+l.branch, side)
	l.git(t, l.repo, "update-ref", "refs/heads/"+l.branch, tip)
	l.requireKept(t, removeFailed(t, []string{l.repo}, l.create()), "on no other branch, remote or tag")
}

// A process working in the checkout keeps it.
func TestRemoveFailedCreate_KeepsACheckoutAProcessUses(t *testing.T) {
	testkit.Slow(t, "creates real git checkouts and worktrees on disk")
	t.Parallel()
	l := newLeftover(t)
	sleep := exec.Command("sleep", "60")
	sleep.Dir = l.path
	require.NoError(t, sleep.Start())
	t.Cleanup(func() { _ = sleep.Process.Kill(); _ = sleep.Wait() })
	require.NotEmpty(t, checkoutInUse(context.Background(), comparablePath(l.path)),
		"a process working in the checkout must be seen")
	resp := removeFailedViaHandler(t, []string{l.repo}, l.create())
	require.Len(t, resp.Checkouts, 1)
	require.False(t, resp.Checkouts[0].Removed)
	require.Contains(t, resp.Checkouts[0].Kept, "process")
	_, err := os.Stat(l.path)
	require.NoError(t, err)
}

// A checkout locked for a row the server did not name — a live workspace the
// server references — is never looked at, let alone removed.
func TestRemoveFailedCreate_LeavesCheckoutsOfOtherRows(t *testing.T) {
	testkit.Slow(t, "creates real git checkouts and worktrees on disk")
	t.Parallel()
	l := newLeftover(t)
	resp := removeFailed(t, []string{l.repo}, failedCreate{WorktreeID: uuid.NewString(), Branch: l.branch})
	require.Empty(t, resp.Checkouts)
	_, err := os.Stat(filepath.Join(l.path, ".git"))
	require.NoError(t, err)
}

// A lock someone else wrote (a person's, an archive fence, git's
// "initializing") is not the lock worktree.create writes: left alone.
func TestRemoveFailedCreate_LeavesForeignLocks(t *testing.T) {
	testkit.Slow(t, "creates real git checkouts and worktrees on disk")
	t.Parallel()
	for name, reason := range map[string]func(id string) string{
		"person":       func(string) string { return "on my external drive" },
		"archived":     func(id string) string { return worktreereclaim.ArchivedLockReason(id, "123") },
		"initializing": func(string) string { return "initializing" },
	} {
		t.Run(name, func(t *testing.T) {
			l := newLeftover(t)
			l.git(t, l.repo, "worktree", "unlock", l.path)
			l.git(t, l.repo, "worktree", "lock", "--reason", reason(l.worktreeID), l.path)
			resp := removeFailed(t, []string{l.repo}, l.create())
			require.Empty(t, resp.Checkouts)
			_, err := os.Stat(filepath.Join(l.path, ".git"))
			require.NoError(t, err)
		})
	}
}

// A checkout on another branch than the row recorded is not its leftover.
func TestRemoveFailedCreate_KeepsACheckoutOnAnotherBranch(t *testing.T) {
	testkit.Slow(t, "creates real git checkouts and worktrees on disk")
	t.Parallel()
	l := newLeftover(t)
	resp := removeFailed(t, []string{l.repo}, failedCreate{WorktreeID: l.worktreeID, Branch: "something-else"})
	l.requireKept(t, resp, "not on the branch")
}

// Only checkouts under this machine's worktrees directory are Reliant's: one
// elsewhere, even locked with a Reliant reason, is the user's.
func TestRemoveFailedCreate_KeepsCheckoutsOutsideTheWorktreesRoot(t *testing.T) {
	testkit.Slow(t, "creates real git checkouts and worktrees on disk")
	t.Parallel()
	repo := diagnoseRepo(t)
	id := uuid.NewString()
	outside := filepath.Join(t.TempDir(), "mine")
	require.NoError(t, fixtureGit(repo, "worktree", "add", "-q", "-b", "mine", outside, "main"))
	require.NoError(t, fixtureGit(repo, "worktree", "lock", "--reason", worktreereclaim.LockReason(id), outside))
	resp := removeFailed(t, []string{repo}, failedCreate{WorktreeID: id, Branch: "mine"})
	require.Len(t, resp.Checkouts, 1)
	require.False(t, resp.Checkouts[0].Removed)
	require.Contains(t, resp.Checkouts[0].Kept, "not under this machine's worktrees directory")
	_, err := os.Stat(filepath.Join(outside, ".git"))
	require.NoError(t, err)
}

// A repository path that is not a main checkout is reported unchecked, so
// the server concludes nothing about the rows it asked about.
func TestRemoveFailedCreate_ReportsAnUnreadableRepositoryUnchecked(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "gone")
	resp := removeFailed(t, []string{missing}, failedCreate{WorktreeID: uuid.NewString(), Branch: "b"})
	require.Equal(t, []string{missing}, resp.Unchecked)
	require.Empty(t, resp.Checked)
	require.Empty(t, resp.Checkouts)
}

// A path the daemon will not look in (relative, or empty) is reported
// unchecked too, never silently dropped: the server must not read its
// silence as "nothing there".
func TestRemoveFailedCreate_ReportsASkippedPathUnchecked(t *testing.T) {
	t.Parallel()
	resp := removeFailed(t, []string{"relative/repo", ""}, failedCreate{WorktreeID: uuid.NewString(), Branch: "b"})
	require.Equal(t, []string{"relative/repo", ""}, resp.Unchecked)
	require.Empty(t, resp.Checked)
}

func revParseIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	require.NoError(t, err)
	return string(out)
}
