// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/osutil"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// =============================================================================
// worktree.ensure
// =============================================================================
//
// Runs before a batch of a chat's tools (and when the user asks for a repair):
// is the directory they are about to run in still there? A worktree directory
// can vanish from under the daemon — deleted by the user, a GC, a crash, a
// volume that detached — and every tool, shell and background process bound
// to it then failed before running, with no way out. So:
//
//   - present: nothing to do (a stat per checkout).
//   - missing, and a worktree reliant created: recreate it in place from its
//     branch, one checkout per nested repo, with the same code that restores
//     an archived workspace (recreateWorktree).
//   - missing, and not recreatable: name another directory that exists — the
//     project's main checkout, else $HOME — so the call still runs.
//
// Every one of those changes is something the model must be told: it believes
// it is working in a directory that it is not. The daemon remembers the latest
// change per workspace and hands it to each audience (thread) once, so a
// branch of the chat — a new thread sharing the worktree — hears about a
// repair it did not trigger, and nobody hears it twice.

func init() {
	RegisterCommand(toolexec.WorkspaceEnsureCommand, handleWorktreeEnsure)
}

// workspaceNoticeTTL bounds how long a change is still reported to threads
// that have not run since it happened.
const workspaceNoticeTTL = 24 * time.Hour

var sharedWorkspaceEnsurer = newWorkspaceEnsurer()

func handleWorktreeEnsure(ctx context.Context, payload []byte) ([]byte, error) {
	var req toolexec.WorkspaceEnsureRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	return json.Marshal(sharedWorkspaceEnsurer.ensure(ctx, req))
}

type workspaceEnsurer struct {
	mu     sync.Mutex
	byPath map[string]*workspaceRecord
	now    func() time.Time
	home   func() (string, error)
}

func newWorkspaceEnsurer() *workspaceEnsurer {
	return &workspaceEnsurer{
		byPath: map[string]*workspaceRecord{},
		now:    time.Now,
		home:   os.UserHomeDir,
	}
}

// workspaceRecord is what the daemon knows about one workspace directory. Its
// lock also serializes ensures of that directory: two threads of one chat (or
// a chat and its branch) run batches concurrently, and must not both recreate
// the same checkout.
type workspaceRecord struct {
	mu     sync.Mutex
	notice *toolexec.WorkspaceNotice
	told   map[string]bool
}

func (e *workspaceEnsurer) record(path string) *workspaceRecord {
	e.mu.Lock()
	defer e.mu.Unlock()
	rec := e.byPath[path]
	if rec == nil {
		rec = &workspaceRecord{}
		e.byPath[path] = rec
	}
	return rec
}

// announce replaces the workspace's latest change. Nobody has been told it.
func (r *workspaceRecord) announce(n toolexec.WorkspaceNotice) {
	r.notice = &n
	r.told = map[string]bool{}
}

// noticeFor hands the latest change to audience, once. An empty audience is
// told nothing and consumes nothing.
func (r *workspaceRecord) noticeFor(audience string, now time.Time) *toolexec.WorkspaceNotice {
	if r.notice == nil {
		return nil
	}
	if now.Sub(r.notice.At) > workspaceNoticeTTL {
		r.notice, r.told = nil, nil
		return nil
	}
	if audience == "" || r.told[audience] {
		return nil
	}
	r.told[audience] = true
	n := *r.notice
	return &n
}

func (e *workspaceEnsurer) ensure(ctx context.Context, req toolexec.WorkspaceEnsureRequest) toolexec.WorkspaceEnsureResponse {
	if req.Path == "" {
		return toolexec.WorkspaceEnsureResponse{Status: toolexec.WorkspacePresent}
	}
	root := filepath.Clean(req.Path)
	rec := e.record(root)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	now := e.now()

	checkouts := ensureCheckouts(req)
	missing := missingCheckouts(root, checkouts)
	rootOK := osutil.ValidateWorkingDir(root) == nil

	if rootOK && len(missing) == 0 {
		// Back after a fallback or with its missing checkouts restored (a
		// volume re-attached, the user rebuilt it): calls run here again, and
		// whoever was moved elsewhere has to know.
		if prev := rec.notice; prev != nil && (prev.Kind == toolexec.NoticeFallback || prev.Kind == toolexec.NoticeIncomplete) {
			rec.announce(toolexec.WorkspaceNotice{
				Kind:                toolexec.NoticeRestored,
				WorkspacePath:       root,
				RunningIn:           root,
				PreviouslyRunningIn: prev.RunningIn,
				Branch:              req.Branch,
				At:                  now,
			})
		}
		return toolexec.WorkspaceEnsureResponse{Status: toolexec.WorkspacePresent, Path: root, Notice: rec.noticeFor(req.Audience, now)}
	}

	reason := "it is not a worktree reliant created, and only those are recreated"
	if req.Repair && len(checkouts) == 0 {
		reason = "no repositories were described to recreate it from"
	}
	if req.Repair && len(missing) > 0 {
		// Recreate exactly what is missing: when the root is gone that is
		// every checkout, but a single deleted nested repo is rebuilt alone.
		var ok bool
		if ok, reason = repairWorkspace(ctx, root, req, missing); ok {
			logging.Info("worktree.ensure: recreated a missing workspace from its branch",
				"path", root, "branch", req.Branch, "worktree_id", req.WorktreeID, "checkouts", checkoutRels(missing))
			rec.announce(toolexec.WorkspaceNotice{
				Kind:          toolexec.NoticeRepaired,
				WorkspacePath: root,
				RunningIn:     root,
				Branch:        req.Branch,
				Checkouts:     checkoutRels(missing),
				At:            now,
			})
			return toolexec.WorkspaceEnsureResponse{Status: toolexec.WorkspaceRepaired, Path: root, Notice: rec.noticeFor(req.Audience, now)}
		}
	}

	// Some checkouts are there: the workspace is still the right place to
	// run, only incomplete. Moving the chat elsewhere would cost it the ones
	// that survived.
	if rootOK && len(missing) < len(checkouts) {
		rels := checkoutRels(missing)
		if prev := rec.notice; prev == nil || prev.Kind != toolexec.NoticeIncomplete || !slices.Equal(prev.Checkouts, rels) {
			logging.Warn("worktree.ensure: workspace is missing checkouts that could not be recreated",
				"path", root, "branch", req.Branch, "missing", rels, "reason", reason)
			rec.announce(toolexec.WorkspaceNotice{
				Kind:          toolexec.NoticeIncomplete,
				WorkspacePath: root,
				RunningIn:     root,
				Branch:        req.Branch,
				Checkouts:     rels,
				Reason:        reason,
				At:            now,
			})
		}
		return toolexec.WorkspaceEnsureResponse{Status: toolexec.WorkspaceIncomplete, Path: root, Detail: reason, Notice: rec.noticeFor(req.Audience, now)}
	}

	dir := e.fallbackDir(req.FallbackPath, root)
	if prev := rec.notice; prev == nil || prev.Kind != toolexec.NoticeFallback || prev.RunningIn != dir {
		logging.Warn("worktree.ensure: workspace is missing and could not be recreated; running elsewhere",
			"path", root, "branch", req.Branch, "fallback", dir, "reason", reason)
		rec.announce(toolexec.WorkspaceNotice{
			Kind:          toolexec.NoticeFallback,
			WorkspacePath: root,
			RunningIn:     dir,
			Branch:        req.Branch,
			Reason:        reason,
			At:            now,
		})
	}
	return toolexec.WorkspaceEnsureResponse{Status: toolexec.WorkspaceFallback, Path: dir, Detail: reason, Notice: rec.noticeFor(req.Audience, now)}
}

// ensureCheckouts lists the checkouts a workspace is made of, parents before
// the repos nested inside them (a nested checkout lands inside its parent's,
// and git will not populate a parent into a directory that already exists).
// Nil when the request describes none, which means only the root is checked.
func ensureCheckouts(req toolexec.WorkspaceEnsureRequest) []toolexec.WorkspaceEnsureRepo {
	if !req.Repair {
		return nil
	}
	out := slices.Clone(req.Repos)
	slices.SortStableFunc(out, func(a, b toolexec.WorkspaceEnsureRepo) int {
		return relDepth(a.Rel) - relDepth(b.Rel)
	})
	return out
}

func relDepth(rel string) int {
	rel = strings.Trim(filepath.ToSlash(rel), "/")
	if rel == "" {
		return 0
	}
	return strings.Count(rel, "/") + 1
}

// missingCheckouts are those with no git checkout at their path. A directory
// without a .git in it is not a checkout: tools run there see nothing of the
// branch.
func missingCheckouts(root string, checkouts []toolexec.WorkspaceEnsureRepo) []toolexec.WorkspaceEnsureRepo {
	var missing []toolexec.WorkspaceEnsureRepo
	for _, c := range checkouts {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(c.Rel), ".git")); err != nil {
			missing = append(missing, c)
		}
	}
	return missing
}

func checkoutRels(checkouts []toolexec.WorkspaceEnsureRepo) []string {
	rels := make([]string, 0, len(checkouts))
	for _, c := range checkouts {
		rels = append(rels, filepath.ToSlash(c.Rel))
	}
	return rels
}

// repairWorkspace recreates the given checkouts of root from req.Branch, or
// says why it cannot. It refuses rather than guesses: a repository that is
// gone, or a directory where a checkout belongs that holds files of its own,
// is left exactly as it is.
func repairWorkspace(ctx context.Context, root string, req toolexec.WorkspaceEnsureRequest, checkouts []toolexec.WorkspaceEnsureRepo) (bool, string) {
	if req.Branch == "" {
		return false, "the workspace has no branch recorded"
	}
	repos := make([]worktreeRecreateRepo, 0, len(checkouts))
	for _, c := range checkouts {
		if _, err := os.Stat(filepath.Join(c.RepoPath, ".git")); err != nil {
			return false, fmt.Sprintf("the project's repository %s is missing on this machine", c.RepoPath)
		}
		dest := filepath.Join(root, filepath.FromSlash(c.Rel))
		// Where the checkout belongs there is a directory without one. Empty,
		// it is ours to replace; with files in it, it is somebody's.
		if entries, err := os.ReadDir(dest); err == nil {
			if len(entries) > 0 {
				return false, fmt.Sprintf("%s exists but is not a git checkout, and was left alone", dest)
			}
			if err := os.Remove(dest); err != nil {
				return false, fmt.Sprintf("could not clear the empty directory %s: %v", dest, err)
			}
		}
		repos = append(repos, worktreeRecreateRepo{RepoPath: c.RepoPath, Rel: c.Rel})
	}
	resp := recreateWorktree(ctx, worktreeRecreateRequest{
		WorktreePath: root,
		Branch:       req.Branch,
		WorktreeID:   req.WorktreeID,
		Repos:        repos,
	})
	if !resp.Success {
		return false, resp.Error
	}
	return true, ""
}

// fallbackDir is where calls run when their workspace is gone: the preferred
// directory (the project's main checkout), else $HOME, else the temp dir —
// the first that is a usable directory and is not the missing workspace.
func (e *workspaceEnsurer) fallbackDir(preferred, missing string) string {
	candidates := []string{preferred}
	if home, err := e.home(); err == nil {
		candidates = append(candidates, home)
	}
	candidates = append(candidates, os.TempDir())
	for _, c := range candidates {
		if c == "" {
			continue
		}
		c = filepath.Clean(c)
		if c == missing {
			continue
		}
		if osutil.ValidateWorkingDir(c) == nil {
			return c
		}
	}
	return os.TempDir()
}
