// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// worktree.remove_failed_create removes the checkouts a worktree.create left
// behind when its answer never reached the server (reliant #2413, owner
// decision 17 (a)). The server gave up on such a create — its 2m wait ran out
// on a saturated machine, or the connection dropped — and marked the
// workspace FAILED, but the daemon finished the checkout and locked it for the
// worktree id. Nothing records its path, and it holds its branch, so the
// user's retry failed with "branch '…' is already checked out at …".
//
// The server is the side that knows the truth: it names the FAILED rows (id
// and branch) and the repositories to look in. The daemon removes a checkout
// only when every one of these holds, and otherwise keeps it and says why:
//
//   - it is a linked worktree of one of the named repositories, under this
//     machine's worktrees root (<HOME>/.reliant/worktrees), and git has it
//     locked with exactly LockReason(<id>) for a FAILED id the server named —
//     the lock only worktree.create writes, after its checkout finished. A
//     lock for anything else (another row, an archive fence, git's own
//     "initializing", a person's) is never touched;
//   - it is on the branch the row recorded;
//   - it is untouched since git made it: every entry of its HEAD reflog is
//     the commit it was created at, written by `git worktree add` itself;
//   - it is clean: no modified, staged, untracked or ignored files, and no
//     index entries hidden by skip-worktree or assume-unchanged;
//   - nothing in it is only there: no commit its HEAD, its reflog or its
//     branch's reflog reaches is missing from every other branch, remote and
//     tag, and it has no per-worktree refs;
//   - no process uses it.
//
// Removal is `git worktree unlock` then `git worktree remove` without
// --force, so git itself refuses a checkout that changed since the checks.
// The branch is never deleted. A checkout git refuses to remove is locked
// again as it was.

const cmdRemoveFailedCreate = "worktree.remove_failed_create"

func init() {
	RegisterCommand(cmdRemoveFailedCreate, handleWorktreeRemoveFailedCreate)
}

// failedCreate is one worktree row the server recorded FAILED.
type failedCreate struct {
	WorktreeID string `json:"worktree_id"`
	Branch     string `json:"branch"`
}

type removeFailedCreateRequest struct {
	// RepoPaths are the main checkouts of the project's repositories on this
	// machine; only their linked worktrees are looked at.
	RepoPaths []string       `json:"repo_paths"`
	Creates   []failedCreate `json:"creates"`
}

// failedCreateCheckout is one checkout found locked for a named id.
type failedCreateCheckout struct {
	WorktreeID string `json:"worktree_id"`
	RepoPath   string `json:"repo_path"`
	Path       string `json:"path"`
	Removed    bool   `json:"removed"`
	// Kept says why the checkout was not removed; empty when Removed.
	Kept string `json:"kept,omitempty"`
}

type removeFailedCreateResponse struct {
	Checkouts []failedCreateCheckout `json:"checkouts"`
	// Checked are the repositories whose worktrees were listed: every
	// checkout locked for a named id in them is in Checkouts. The server
	// concludes a row has nothing left only from repositories named here, so
	// a path this daemon skipped, or an older daemon that does not report
	// it, settles nothing.
	Checked []string `json:"checked,omitempty"`
	// Unchecked are the repositories whose worktrees could not be listed:
	// nothing can be concluded about them.
	Unchecked []string `json:"unchecked,omitempty"`
}

// failedCreateTimeout bounds one request; process inspection and git status
// over a large checkout are the slow parts.
const failedCreateTimeout = 2 * time.Minute

func handleWorktreeRemoveFailedCreate(ctx context.Context, payload []byte) ([]byte, error) {
	var req removeFailedCreateRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, failedCreateTimeout)
	defer cancel()
	resp := removeFailedCreates(ctx, filepath.Join(home, ".reliant", "worktrees"), req, checkoutInUse)
	return json.Marshal(resp)
}

// removeFailedCreates is the command against an explicit worktrees root.
// inUse says why a process may be using a checkout, or "" (checkoutInUse;
// tests pass their own, because what a host's other processes let us read is
// not theirs to control).
func removeFailedCreates(ctx context.Context, root string, req removeFailedCreateRequest, inUse func(context.Context, string) string) removeFailedCreateResponse {
	resp := removeFailedCreateResponse{Checkouts: []failedCreateCheckout{}}
	byReason := make(map[string]failedCreate, len(req.Creates))
	for _, c := range req.Creates {
		if c.WorktreeID == "" || c.Branch == "" {
			continue
		}
		byReason[worktreereclaim.LockReason(c.WorktreeID)] = c
	}
	if len(byReason) == 0 {
		return resp
	}
	// The root must exist and is compared resolved: a checkout is judged by
	// where it really is ($HOME may be a symlink).
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return resp
	}
	realRoot := comparablePath(root)

	type candidate struct {
		repo   string
		entry  porcelainWorktree
		create failedCreate
	}
	var found []candidate
	// listed is, per repository already looked at, whether its worktrees
	// were listed: a repeated path is reported as its first was.
	listed := map[string]bool{}
	for _, repo := range req.RepoPaths {
		if repo == "" || !filepath.IsAbs(repo) {
			resp.Unchecked = append(resp.Unchecked, repo)
			continue
		}
		if ok, seen := listed[comparablePath(repo)]; seen {
			if ok {
				resp.Checked = append(resp.Checked, repo)
			} else {
				resp.Unchecked = append(resp.Unchecked, repo)
			}
			continue
		}
		entries, err := listLockedWorktrees(ctx, repo)
		listed[comparablePath(repo)] = err == nil
		if err != nil {
			logging.WarnContext(ctx, "worktree.remove_failed_create: could not list the repository's worktrees",
				"repo_path", repo, "error", err)
			resp.Unchecked = append(resp.Unchecked, repo)
			continue
		}
		resp.Checked = append(resp.Checked, repo)
		for _, e := range entries {
			if c, ok := byReason[e.LockReason]; ok && e.Locked {
				found = append(found, candidate{repo: repo, entry: e, create: c})
			}
		}
	}
	// Deepest first: a checkout nested in another (a repo inside a repo) is
	// removed before its parent is judged, or the parent would read dirty.
	sort.SliceStable(found, func(i, j int) bool {
		return strings.Count(filepath.Clean(found[i].entry.Path), string(filepath.Separator)) >
			strings.Count(filepath.Clean(found[j].entry.Path), string(filepath.Separator))
	})
	for _, c := range found {
		out := failedCreateCheckout{WorktreeID: c.create.WorktreeID, RepoPath: c.repo, Path: c.entry.Path}
		if why := keepFailedCreateCheckout(ctx, realRoot, c.repo, c.entry, c.create.Branch, inUse); why != "" {
			out.Kept = why
		} else if err := removeFailedCreateCheckout(ctx, realRoot, c.repo, c.entry.Path, worktreereclaim.LockReason(c.create.WorktreeID)); err != nil {
			out.Kept = err.Error()
		} else {
			out.Removed = true
		}
		if out.Removed {
			logging.InfoContext(ctx, "worktree.remove_failed_create: removed a checkout a failed create left behind",
				"worktree_id", out.WorktreeID, "path", out.Path, "repo_path", out.RepoPath, "branch", c.create.Branch)
		} else {
			logging.InfoContext(ctx, "worktree.remove_failed_create: kept a checkout a failed create left behind",
				"worktree_id", out.WorktreeID, "path", out.Path, "repo_path", out.RepoPath, "reason", out.Kept)
		}
		resp.Checkouts = append(resp.Checkouts, out)
	}
	return resp
}

// listLockedWorktrees lists repo's linked worktrees. A path that is not
// itself a repository's main checkout is an error: `git -C` would otherwise
// walk up into an enclosing repository and report ITS worktrees.
func listLockedWorktrees(ctx context.Context, repo string) ([]porcelainWorktree, error) {
	if fi, err := os.Stat(filepath.Join(repo, ".git")); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%s is not the main checkout of a repository", repo)
	}
	out, err := failedCreateGit(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	all := parseWorktreePorcelain(out)
	if len(all) > 0 {
		all = all[1:] // the first block is the main checkout
	}
	return all, nil
}

// keepFailedCreateCheckout returns why the checkout must be kept, or "" when
// it is provably the untouched leftover of a failed create.
func keepFailedCreateCheckout(ctx context.Context, root, repo string, e porcelainWorktree, branch string, inUse func(context.Context, string) string) string {
	p := comparablePath(e.Path)
	rel, err := filepath.Rel(root, p)
	if err != nil || !filepath.IsLocal(rel) || rel == "." {
		return fmt.Sprintf("it is not under this machine's worktrees directory %s", root)
	}
	if e.Bare || e.Detached || e.Branch != branch {
		return fmt.Sprintf("it is not on the branch %q the failed workspace recorded", branch)
	}
	if _, err := os.Lstat(e.Path); errors.Is(err, fs.ErrNotExist) {
		// Only git's record is left; removing it removes no file. The
		// branch and its commits are judged below all the same.
		return keepForHistory(ctx, repo, e.Path, branch, false)
	} else if err != nil {
		return fmt.Sprintf("could not read it: %v", err)
	}
	if !isOwnLinkedCheckout(ctx, repo, e.Path) {
		return "it is not a linked worktree of the repository"
	}
	if why := keepForHistory(ctx, repo, e.Path, branch, true); why != "" {
		return why
	}
	status, err := failedCreateGit(ctx, e.Path, "--no-optional-locks", "status", "--porcelain=v1",
		"--ignored=matching", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return fmt.Sprintf("could not read its status: %v", err)
	}
	if status != "" {
		return "it has uncommitted, untracked or ignored files"
	}
	flags, err := failedCreateGit(ctx, e.Path, "ls-files", "-v")
	if err != nil {
		return fmt.Sprintf("could not read its index: %v", err)
	}
	for _, line := range strings.Split(flags, "\n") {
		// Lower case is assume-unchanged, S is skip-worktree: edits git
		// status does not show.
		if line != "" && (line[0] == 'S' || (line[0] >= 'a' && line[0] <= 'z')) {
			return "it has files hidden from git status (skip-worktree or assume-unchanged)"
		}
	}
	if why := inUse(ctx, p); why != "" {
		return why
	}
	return ""
}

// isOwnLinkedCheckout reports whether checkout's .git is a gitfile naming a
// registration in repo's own worktrees directory: a linked worktree of repo,
// not a repository or a submodule in its own right.
func isOwnLinkedCheckout(ctx context.Context, repo, checkout string) bool {
	fi, err := os.Lstat(filepath.Join(checkout, ".git"))
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	admin := registrationOf(ctx, repo, checkout)
	if admin == "" {
		return false
	}
	common, err := failedCreateGit(ctx, repo, "rev-parse", "--git-common-dir")
	if err != nil {
		return false
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(repo, common)
	}
	return sameCheckoutPath(filepath.Dir(admin), filepath.Join(common, "worktrees"))
}

// keepForHistory returns why the checkout's history must be kept: it was used
// after git made it, or it reaches a commit no other branch, remote or tag
// holds (removing the checkout would leave that commit only on a branch the
// next create resets, or on no ref at all). withCheckout is false when only
// git's record of the checkout is left.
func keepForHistory(ctx context.Context, repo, checkout, branch string, withCheckout bool) string {
	tips := []string{"refs/heads/" + branch}
	branchLog, err := failedCreateGit(ctx, repo, "reflog", "show", "--format=%H", "refs/heads/"+branch, "--")
	if err != nil {
		return fmt.Sprintf("could not read the branch's history: %v", err)
	}
	tips = append(tips, strings.Fields(branchLog)...)
	if withCheckout {
		head, err := failedCreateGit(ctx, checkout, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return fmt.Sprintf("could not read its HEAD: %v", err)
		}
		// A checkout nobody used has the reflog `git worktree add` writes and
		// nothing else: entries at the commit it was made at, with no subject
		// or "reset: moving to HEAD" (git 2.43). A commit, checkout, rebase,
		// merge or reset anyone made since adds an entry this refuses.
		headLog, err := failedCreateGit(ctx, checkout, "reflog", "show", "--format=%H %gs", "HEAD", "--")
		if err != nil || headLog == "" {
			return "its history cannot be read, so it cannot be shown to be unused"
		}
		for _, line := range strings.Split(headLog, "\n") {
			sha, subject, _ := strings.Cut(line, " ")
			if sha != head || (subject != "" && subject != "reset: moving to HEAD") {
				return "it was used after it was created"
			}
		}
		refs, err := failedCreateGit(ctx, checkout, "for-each-ref", "--format=%(refname)", "refs/worktree/", "refs/bisect/")
		if err != nil {
			return fmt.Sprintf("could not read its refs: %v", err)
		}
		if refs != "" {
			return "it has refs of its own (refs/worktree or refs/bisect)"
		}
		tips = append(tips, head)
	}
	// Patterns given to --exclude before --branches are branch names, not
	// full refs.
	args := append([]string{"rev-list", "--count"}, tips...)
	args = append(args, "--not", "--exclude="+branch, "--branches", "--remotes", "--tags")
	count, err := failedCreateGit(ctx, repo, args...)
	if err != nil {
		return fmt.Sprintf("could not compare its commits with the repository's branches: %v", err)
	}
	if count != "0" {
		return fmt.Sprintf("it holds %s commit(s) that are on no other branch, remote or tag", count)
	}
	return ""
}

// checkoutInUse returns why a process may be using the checkout, or "" when
// none is. On Linux every same-owner process's cwd, executable, open and
// mapped files are inspected (worktreereclaim.CheckProcessUse). Elsewhere
// only working directories can be listed (lsof); that is enough here,
// because a checkout that reaches this check is clean and holds nothing that
// is not on another ref, so removing it loses no work. Not being able to tell
// is a reason to keep it.
func checkoutInUse(ctx context.Context, checkout string) string {
	err := worktreereclaim.CheckProcessUse(ctx, []string{checkout})
	if err == nil {
		return ""
	}
	if !errors.Is(err, worktreereclaim.ErrProcessUseUnsupported) {
		return fmt.Sprintf("a process may be using it: %v", err)
	}
	cwds, err := worktreereclaim.ProcessCwds(ctx)
	if err != nil {
		return fmt.Sprintf("could not tell whether a process is using it: %v", err)
	}
	for _, cwd := range cwds {
		if pathWithin(comparablePath(cwd), checkout) {
			return "a process is running in it"
		}
	}
	return ""
}

// removeFailedCreateCheckout unlocks and removes one checkout, then the
// directories above it the removal left empty, up to the project directory
// under root. If git refuses, the lock is put back as it was.
func removeFailedCreateCheckout(ctx context.Context, root, repo, checkout, reason string) error {
	if _, err := failedCreateGit(ctx, repo, "worktree", "unlock", checkout); err != nil {
		return fmt.Errorf("could not unlock it: %w", err)
	}
	if _, err := failedCreateGit(ctx, repo, "worktree", "remove", checkout); err != nil {
		if _, lockErr := failedCreateGit(ctx, repo, "worktree", "lock", "--reason", reason, checkout); lockErr != nil {
			logging.WarnContext(ctx, "worktree.remove_failed_create: could not lock a kept checkout again",
				"path", checkout, "error", lockErr)
		}
		return fmt.Errorf("git refused to remove it: %w", err)
	}
	// <root>/<project>/<name>-<id>[/<sub_path>]: everything below the project
	// directory was this workspace's. os.Remove refuses a non-empty one.
	for dir := filepath.Dir(comparablePath(checkout)); ; dir = filepath.Dir(dir) {
		rel, err := filepath.Rel(root, dir)
		if err != nil || !filepath.IsLocal(rel) || !strings.Contains(filepath.ToSlash(rel), "/") {
			break
		}
		if os.Remove(dir) != nil {
			break
		}
	}
	return nil
}

// failedCreateGit runs git in dir and returns its trimmed stdout, or an error
// carrying its stderr.
func failedCreateGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("git %s: %s", args[0], msg)
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}
