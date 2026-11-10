// Copyright (c) 2025 Reliant Labs

package workspacecreate

import (
	"context"
	"path/filepath"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// A worktree.create whose answer is lost — the server's wait ran out on a
// saturated machine, or the connection dropped mid-create — leaves a finished
// checkout the server never recorded: Finish marks the row FAILED with no
// path, but the daemon completed the checkout and locked it for the row's id.
// It holds its branch, so the user's retry failed with "branch '…' is already
// checked out at …" (reliant #2413).
//
// Owner decision 17 (a) (2026-11-08): the server, which knows the workspace
// failed, cleans it up. It names the FAILED rows; the daemon removes only a
// checkout locked for exactly one of them that is untouched, clean and holds
// nothing no other ref does, and keeps anything else
// (worktree.remove_failed_create, internal/toolexec/daemonruntime). That runs
// in two places:
//
//   - when the machine connects (RemoveFailedCreatesOn), for every recent
//     FAILED row it owns;
//   - when a create finds its branch held (Finish), for the FAILED rows of
//     the same branch, after which the create is tried once more.
//
// A daemon older than the command answers with an error; both callers then
// carry on exactly as before.

const removeFailedCreateCmd = "worktree.remove_failed_create"

// removeFailedCreateTimeoutMs bounds one request: the daemon inspects
// processes and runs git status on each checkout it finds.
const removeFailedCreateTimeoutMs int32 = 150_000

// failedCreateWindow is how far back a connect looks for FAILED rows, and
// failedCreateLimit how many it sends at most. An older leftover is still
// cleared by the retry that meets it.
const (
	failedCreateWindow = 30 * 24 * time.Hour
	failedCreateLimit  = 100
)

type failedCreateRef struct {
	WorktreeID string `json:"worktree_id"`
	Branch     string `json:"branch"`
}

type removeFailedCreatePayload struct {
	RepoPaths []string          `json:"repo_paths"`
	Creates   []failedCreateRef `json:"creates"`
}

type removeFailedCreateCheckout struct {
	WorktreeID string `json:"worktree_id"`
	RepoPath   string `json:"repo_path"`
	Path       string `json:"path"`
	Removed    bool   `json:"removed"`
	Kept       string `json:"kept,omitempty"`
}

type removeFailedCreateResult struct {
	Checkouts []removeFailedCreateCheckout `json:"checkouts"`
	// Checked are the repositories the daemon listed; a daemon older than
	// the field reports none, and so settles nothing.
	Checked   []string `json:"checked,omitempty"`
	Unchecked []string `json:"unchecked,omitempty"`
}

// removed reports whether any checkout was removed.
func (r removeFailedCreateResult) removed() bool {
	for _, c := range r.Checkouts {
		if c.Removed {
			return true
		}
	}
	return false
}

// settled returns the ids, of those asked about, that have nothing left
// behind: every checkout locked for them was removed, or none was found, in
// repoPaths — which must be non-empty, and which the daemon must report it
// listed, every one. Anything less (a repository it skipped or could not
// read, an older daemon that does not say what it listed) settles nothing,
// and the next connect asks again.
func (r removeFailedCreateResult) settled(ids, repoPaths []string) []string {
	if len(r.Unchecked) > 0 || len(repoPaths) == 0 {
		return nil
	}
	checked := make(map[string]bool, len(r.Checked))
	for _, p := range r.Checked {
		checked[p] = true
	}
	for _, p := range repoPaths {
		if !checked[p] {
			return nil
		}
	}
	kept := map[string]bool{}
	for _, c := range r.Checkouts {
		if !c.Removed {
			kept[c.WorktreeID] = true
		}
	}
	var out []string
	for _, id := range ids {
		if !kept[id] {
			out = append(out, id)
		}
	}
	return out
}

// removeFailedCreates asks m to remove the checkouts left behind by rows,
// looking in repoPaths.
func removeFailedCreates(ctx context.Context, m Machine, repoPaths []string, rows []*db.Worktree) (removeFailedCreateResult, error) {
	payload := removeFailedCreatePayload{RepoPaths: repoPaths}
	for _, w := range rows {
		payload.Creates = append(payload.Creates, failedCreateRef{WorktreeID: w.ID, Branch: w.Branch})
	}
	var res removeFailedCreateResult
	err := m.Send(ctx, removeFailedCreateCmd, payload, &res, removeFailedCreateTimeoutMs)
	return res, err
}

// isFailedLeftover reports whether row may have left a checkout behind that
// nothing references: a FAILED workspace with no path (Finish's rollback of
// one it heard back from leaves nothing, and one it gave up on leaves at
// most that checkout).
func isFailedLeftover(w *db.Worktree) bool {
	return w != nil && !w.IsMain && w.Path == "" &&
		w.Status == int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED)
}

// freeBranchFromFailedCreates removes the checkouts that FAILED rows of the
// project left holding wt's branch in repoPath, and reports whether any was
// removed (the caller then retries its create). A machine or store that
// cannot answer removes nothing.
func freeBranchFromFailedCreates(ctx context.Context, store Store, m Machine, wt *db.Worktree, repoPath string) bool {
	holders, err := store.ListWorktreesByBranch(ctx, wt.ProjectID, wt.Branch)
	if err != nil {
		logging.WarnContext(ctx, "Could not look up the failed workspaces of a held branch", "error", err,
			"branch", wt.Branch, "worktreeID", wt.ID)
		return false
	}
	var failed []*db.Worktree
	for _, h := range holders {
		if h.ID != wt.ID && isFailedLeftover(h) && sameOwner(h, wt) {
			failed = append(failed, h)
		}
	}
	if len(failed) == 0 {
		return false
	}
	res, err := removeFailedCreates(ctx, m, []string{repoPath}, failed)
	if err != nil {
		logging.InfoContext(ctx, "Could not ask the machine to remove a failed create's checkout; the create is not retried",
			"error", err, "branch", wt.Branch, "worktreeID", wt.ID)
		return false
	}
	for _, c := range res.Checkouts {
		if !c.Removed {
			logging.InfoContext(ctx, "A failed create's checkout holds the branch and was kept",
				"path", c.Path, "reason", c.Kept, "failedWorktreeID", c.WorktreeID, "worktreeID", wt.ID)
		}
	}
	return res.removed()
}

// sameOwner reports whether a and b were created on the same machine: a
// FAILED row's checkout can only be on the machine it was sent to.
func sameOwner(a, b *db.Worktree) bool {
	return a.DaemonID != nil && b.DaemonID != nil && *a.DaemonID == *b.DaemonID
}

// RemoveFailedCreatesOn asks daemonID, which just connected, to remove the
// checkouts its recent FAILED workspaces left behind, and records each
// workspace with nothing left as cleaned up so it is not asked about again.
// It returns the number of checkouts removed. Best effort: a machine that
// does not answer, or a daemon too old to know the command, is logged and
// the next connect asks again.
func RemoveFailedCreatesOn(ctx context.Context, store FailedCreateStore, m Machine, daemonID string) int {
	if daemonID == "" {
		return 0
	}
	rows, err := store.ListFailedWorktreesForDaemon(ctx, daemonID, time.Now().Add(-failedCreateWindow), failedCreateLimit)
	if err != nil {
		logging.WarnContext(ctx, "failed-create cleanup: could not list the machine's failed workspaces", "error", err, "daemonID", daemonID)
		return 0
	}
	byProject := map[string][]*db.Worktree{}
	var order []string
	for _, w := range rows {
		if !isFailedLeftover(w) {
			continue
		}
		if _, ok := byProject[w.ProjectID]; !ok {
			order = append(order, w.ProjectID)
		}
		byProject[w.ProjectID] = append(byProject[w.ProjectID], w)
	}
	if len(order) == 0 {
		return 0
	}
	installs, err := store.ListProjectDaemonsForDaemon(ctx, daemonID)
	if err != nil {
		logging.WarnContext(ctx, "failed-create cleanup: could not list the machine's checkouts", "error", err, "daemonID", daemonID)
		return 0
	}
	checkouts := map[string]string{}
	for _, pd := range installs {
		if pd.Path != "" && (pd.InstallState == "" || pd.InstallState == core.ProjectInstallInstalled) {
			checkouts[pd.ProjectID] = pd.Path
		}
	}

	removed := 0
	for _, projectID := range order {
		checkout := checkouts[projectID]
		if checkout == "" {
			// No checkout of the project on this machine to look in: its
			// workspaces' repositories are not here.
			continue
		}
		repos, err := store.ListReposByProject(ctx, projectID)
		if err != nil {
			logging.WarnContext(ctx, "failed-create cleanup: could not list the project's repositories", "error", err, "projectID", projectID)
			continue
		}
		// The project's repositories are where its creates checked out:
		// with none on record there is nowhere to look, and nothing is
		// concluded.
		repoPaths := make([]string, 0, len(repos))
		for _, r := range repos {
			if r == nil {
				continue
			}
			repoPaths = append(repoPaths, filepath.Join(checkout, r.RelativePath))
		}
		if len(repoPaths) == 0 {
			logging.InfoContext(ctx, "failed-create cleanup: the project has no repositories on record; its failed workspaces stay pending",
				"projectID", projectID, "daemonID", daemonID)
			continue
		}
		failed := byProject[projectID]
		res, err := removeFailedCreates(ctx, m, repoPaths, failed)
		if err != nil {
			// An older daemon has no such command, and a machine that went
			// away again cannot answer: either way, nothing was removed.
			logging.InfoContext(ctx, "failed-create cleanup: the machine did not remove failed creates' checkouts; the next connect asks again",
				"error", err, "daemonID", daemonID, "projectID", projectID)
			return removed
		}
		for _, c := range res.Checkouts {
			if c.Removed {
				removed++
			}
		}
		ids := make([]string, len(failed))
		for i, w := range failed {
			ids[i] = w.ID
		}
		for _, id := range res.settled(ids, repoPaths) {
			if err := store.MergeWorktreeCleanupMetadata(ctx, id, func(meta *db.CleanupMetadata, _ bool) bool {
				if meta.DirectoryDeleted {
					return false
				}
				meta.DirectoryDeleted = true
				return true
			}); err != nil {
				logging.WarnContext(ctx, "failed-create cleanup: could not record a failed workspace as cleaned up", "error", err, "worktreeID", id)
			}
		}
	}
	if removed > 0 {
		logging.InfoContext(ctx, "failed-create cleanup: removed checkouts that failed creates left behind", "daemonID", daemonID, "removed", removed)
	}
	return removed
}
