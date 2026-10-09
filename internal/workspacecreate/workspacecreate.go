// Copyright (c) 2025 Reliant Labs

// Package workspacecreate is THE place a worktree workspace is created: the
// directory <HOME>/.reliant/worktrees/<project-slug>/<name>-<8hex>/ holding one
// git checkout per nested repo of the project, tracked by a single worktree
// row whose Path is the workspace root.
//
// The UI (gRPC WorktreeService), workflows (create_worktree activity) and
// agents (the LLM worktree tool) must all create worktrees through this
// package, so every worktree lands in the same layout with the same rollback,
// reclaim-lock and base-branch bookkeeping. Do not reimplement the checkout
// loop elsewhere.
//
// Creation is split in two so a caller may return early: Insert writes the
// CREATING row, Finish does the daemon work and settles the row to ACTIVE or
// FAILED. Synchronous callers call both in a row. Neither emits a client
// refetch; the caller does, because only it knows whom to notify.
//
// This is a leaf package: it knows a daemon only through the one-method
// Machine, so it can be imported from internal/llm/tools without touching
// toolexec, grpc/services or the workflow packages.
package workspacecreate

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	repopkg "github.com/reliant-labs/reliant/internal/repo"
	"github.com/reliant-labs/reliant/internal/worktreepath"
)

// commandTimeoutMs bounds each daemon command; a git checkout of a large repo
// is the slow case.
const commandTimeoutMs int32 = 120_000

// Machine sends one command to the ONE daemon a workspace is created on. All
// of a workspace's checkouts, cleanups and the copy must reach the same
// machine, so the implementation is bound to the owner daemon up front.
// resp may be nil when the caller ignores the response.
type Machine interface {
	Send(ctx context.Context, commandType string, payload, resp any, timeoutMs int32) error
}

// Store is the slice of db.Repository creation writes through.
type Store interface {
	CreateWorktree(ctx context.Context, worktree *db.Worktree) error
	UpdateWorktree(ctx context.Context, worktree *db.Worktree) error
	ArchiveWorktree(ctx context.Context, id string) error
	GetLiveWorktreeByName(ctx context.Context, projectID, name string) (*db.Worktree, error)
	ListWorktreesByBranch(ctx context.Context, projectID, branch string) ([]*db.Worktree, error)
}

// NameTakenError says a live (unarchived) worktree already holds the name.
// errors.Is(err, core.ErrWorktreeNameTaken) matches it, and also matches the
// bare error the store returns when a concurrent create wins the race (Holder
// is then nil).
type NameTakenError struct {
	Name   string
	Holder *db.Worktree
}

func (e *NameTakenError) Error() string {
	return fmt.Sprintf("a worktree named '%s' already exists in this project", e.Name)
}

func (e *NameTakenError) Is(target error) bool { return target == core.ErrWorktreeNameTaken }

// BranchHeldError says the branch belongs to another worktree of the project
// whose branch (and possibly checkout) must survive, so it cannot be reused.
// Forcing does not override it: creation resets or deletes the branch.
type BranchHeldError struct {
	Branch string
	Holder *db.Worktree
}

func (e *BranchHeldError) Error() string {
	state := "worktree"
	if e.Holder.DeletedAt != nil {
		state = "archived worktree"
	}
	return fmt.Sprintf("branch '%s' belongs to the %s '%s' and would be reset; pass a different branch", e.Branch, state, e.Holder.Name)
}

// Request describes the workspace to create. Callers list (and adopt) the
// project's repos themselves and resolve the owner daemon.
type Request struct {
	Project *db.Project
	// Repos are the project's nested repos; one checkout is made for each.
	Repos []*core.Repo
	// Name is the human name, also the workspace directory's prefix.
	Name string
	// Branch is the new branch. Empty defaults to worktree/<name>-<unix>.
	Branch string
	// BaseBranch is the global base; empty lets the daemon auto-detect.
	BaseBranch string
	// BaseBranches overrides BaseBranch per repo ID.
	BaseBranches map[string]string
	// Force cleans up a stale branch of the same name before each checkout.
	Force bool
	// CopyPaths are exact paths, relative to the workspace root, carried over
	// from CopySource (gitignored files such as .env). Callers validate them
	// with copypath.CleanAll.
	CopyPaths []string
	// CopySource is where CopyPaths come from; "" means Project.Path.
	CopySource string
	// ChatID, when set, records the chat the workspace was created for.
	ChatID *string
	// OwnerDaemonID is recorded on the row so tool execution routes back to
	// the machine the checkouts live on.
	OwnerDaemonID string
	// IdempotencyKey, when set, is stored on the row (see
	// db.Repository.GetWorktreeByIdempotencyKey; the caller checks it).
	IdempotencyKey *string
	// WorkspaceID, when set, is the workspace directory name under the
	// worktrees root (see worktreepath.WorkspaceDirName). Adopt needs it up
	// front because it moves a checkout into the directory before Finish.
	WorkspaceID string
	// Existing lists repos whose checkout already exists (an adopted
	// worktree). Finish makes no checkout for them and records their branch
	// and base. Setting it also puts Finish in adopt mode for the OTHER repos:
	// they check out Branch as-is when it exists and are never reset (see
	// ExistingCheckout).
	Existing map[string]ExistingCheckout
}

// ExistingCheckout is a repo checkout that already exists at Path.
//
// Adopt-mode rules for the project's other repos: if Branch already exists in
// that repo it is checked out as it is (never `-B`, never forced), because a
// branch of that name there is somebody's work; git itself refuses when it is
// checked out elsewhere, and that fails the adopt with a message saying so.
// If it does not exist it is created from the repo's base branch.
type ExistingCheckout struct {
	// Path is where the checkout is now.
	Path string
	// Base is the base branch to record for the repo.
	Base string
	// OriginalPath is where the checkout was before the adopt MOVED it. Empty
	// means it was registered in place and never moved. When set, a failed
	// Finish moves it back instead of leaving it in a half-built workspace.
	OriginalPath string
}

// Insert persists the CREATING row, before any daemon work. The returned row
// carries the ID the daemon writes into each checkout's reclaim lock reason.
// Its Path stays empty until Finish learns where the checkouts landed.
func Insert(ctx context.Context, store Store, req Request) (*db.Worktree, error) {
	branch := req.Branch
	if branch == "" {
		branch = fmt.Sprintf("worktree/%s-%d", req.Name, time.Now().Unix())
	}
	if err := claimName(ctx, store, req.Project.ID, req.Name, branch); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	owner := req.OwnerDaemonID
	wt := &db.Worktree{
		ID:             uuid.New().String(),
		Name:           req.Name,
		Branch:         branch,
		BaseBranch:     req.BaseBranch,
		ProjectID:      req.Project.ID,
		ChatID:         req.ChatID,
		DaemonID:       &owner,
		IdempotencyKey: req.IdempotencyKey,
		Status:         int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_CREATING),
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActive:     now,
	}
	if err := store.CreateWorktree(ctx, wt); err != nil {
		return nil, err
	}
	return wt, nil
}

// claimName is the one name-availability rule. A live row holding the name
// blocks the create, except a FAILED one: nothing of it survives (Finish rolled
// its checkouts back and left no path), so a retry archives it and proceeds.
// Archived rows do not hold names.
//
// It also refuses a branch another row of the project is entitled to keep.
// The daemon creates with `git worktree add -B`, which resets an existing
// branch to the base, and force deletes it first; either would destroy the
// unmerged commits of an archived worktree whose checkout or branch the sweep
// has not (or will not) remove. Only a FAILED row, which never held work, and a
// row whose branch is recorded as deleted release theirs.
func claimName(ctx context.Context, store Store, projectID, name, branch string) error {
	live, err := store.GetLiveWorktreeByName(ctx, projectID, name)
	if err != nil {
		return fmt.Errorf("look up worktree name: %w", err)
	}
	failed := int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED)
	if live != nil && live.Status != failed {
		return &NameTakenError{Name: name, Holder: live}
	}

	holders, err := store.ListWorktreesByBranch(ctx, projectID, branch)
	if err != nil {
		return fmt.Errorf("look up worktree branch: %w", err)
	}
	for _, h := range holders {
		if h.Status == failed || (h.CleanupMetadata != nil && h.CleanupMetadata.BranchDeleted) {
			continue
		}
		return &BranchHeldError{Branch: branch, Holder: h}
	}

	if live != nil {
		if err := store.ArchiveWorktree(ctx, live.ID); err != nil {
			return fmt.Errorf("archive failed worktree '%s': %w", name, err)
		}
	}
	return nil
}

// Finish performs the daemon-side creation for a row from Insert and settles
// it to ACTIVE or FAILED. It mutates wt, so a caller that also reads the row
// concurrently must hand it a copy.
//
// Creation is all-or-nothing on disk: if any repo fails, every checkout that
// succeeded and the workspace root are removed and the row is kept as FAILED
// (with no path) so the failure stays visible. A failed copy_files does not
// fail the workspace. The returned error is the reason for FAILED.
func Finish(ctx context.Context, store Store, m Machine, req Request, wt *db.Worktree) error {
	project, repos := req.Project, req.Repos
	workspaceID := req.WorkspaceID
	if workspaceID == "" {
		workspaceID = worktreepath.WorkspaceDirName(project.Name, wt.Name)
	}
	adopting := len(req.Existing) > 0
	globalBase := req.BaseBranch

	type repoCreateResult struct {
		repo         *core.Repo
		worktreePath string
		baseBranch   string
	}
	successes := make([]repoCreateResult, 0, len(repos))

	// Declared before fail() so the rollback can remove the workspace root.
	// Assigned from the first successful per-repo create below.
	var workspaceRoot string

	fail := func(reason error) error {
		// A moved checkout goes back FIRST, and is never deleted: it is the
		// user's work. If it cannot go back, the workspace root is left alone
		// (it holds that checkout) and the reason says where it is.
		restoreFailed := false
		for _, repo := range repos {
			ex, ok := req.Existing[repo.ID]
			if !ok || ex.OriginalPath == "" {
				continue
			}
			var rr struct {
				Success bool   `json:"success"`
				Error   string `json:"error"`
			}
			err := m.Send(ctx, "worktree.adopt_restore", map[string]string{
				"repo_path": filepath.Join(project.Path, repo.RelativePath),
				"src":       ex.OriginalPath,
				"dest":      ex.Path,
			}, &rr, commandTimeoutMs)
			if err != nil || !rr.Success {
				restoreFailed = true
				detail := rr.Error
				if err != nil {
					detail = err.Error()
				}
				logging.Error("Failed to move adopted checkout back", "error", detail, "from", ex.Path, "to", ex.OriginalPath)
				reason = fmt.Errorf("%w; the adopted checkout could not be moved back and is still at %s (%s)", reason, ex.Path, detail)
			}
		}
		for _, s := range successes {
			if _, adopted := req.Existing[s.repo.ID]; adopted {
				continue
			}
			repoPath := filepath.Join(project.Path, s.repo.RelativePath)
			_ = m.Send(ctx, "worktree.delete_directory", map[string]string{
				"project_path":  repoPath,
				"worktree_path": s.worktreePath,
			}, nil, commandTimeoutMs)
		}
		// The per-repo deletes clear <root>/<repo.rel>, which leaves the root
		// behind for a multi-repo workspace — an empty directory that looks
		// like a workspace to anything that stats it. Empty-only, for the same
		// reason as the daemon-side rollback.
		// An in-place adopt's workspace root IS the user's checkout.
		inPlace := false
		for _, ex := range req.Existing {
			if ex.OriginalPath == "" {
				inPlace = true
			}
		}
		if workspaceRoot != "" && !restoreFailed && !inPlace {
			_ = m.Send(ctx, "worktree.delete_directory", map[string]string{
				"project_path":  project.Path,
				"worktree_path": workspaceRoot,
			}, nil, commandTimeoutMs)
		}

		logging.Error("Worktree creation failed", "error", reason, "worktreeID", wt.ID)
		wt.Status = int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED)
		// A FAILED row must carry NO path: filepreview.ResolveBasePath returns an
		// existing row's path as-is, so a path to directories just deleted
		// would fail every scoped filesystem RPC. Empty is the value it
		// recognizes.
		wt.Path = ""
		wt.UpdatedAt = time.Now().UTC()
		if err := store.UpdateWorktree(ctx, wt); err != nil {
			logging.Error("Failed to mark worktree failed", "error", err, "worktreeID", wt.ID)
		}
		return reason
	}

	// createRepo checks out ONE repo. It runs concurrently with its siblings,
	// so it touches nothing shared.
	createRepo := func(repo *core.Repo) (repoCreateResult, error) {
		repoPath := filepath.Join(project.Path, repo.RelativePath)

		// Per-repo override > global > daemon auto-detect (empty).
		repoBase := globalBase
		if v, ok := req.BaseBranches[repo.ID]; ok && v != "" {
			repoBase = v
		}

		if ex, ok := req.Existing[repo.ID]; ok {
			return repoCreateResult{repo: repo, worktreePath: ex.Path, baseBranch: ex.Base}, nil
		}

		if req.Force && !adopting {
			// Stale-branch cleanup; the workspace dir itself is fresh per create.
			_ = m.Send(ctx, "worktree.force_cleanup", map[string]string{
				"project_path":  repoPath,
				"worktree_path": "",
				"branch":        wt.Branch,
			}, nil, commandTimeoutMs)
		}

		type createReq struct {
			ProjectPath string `json:"project_path"`
			WorkspaceID string `json:"workspace_id"`
			SubPath     string `json:"sub_path"`
			Name        string `json:"name"`
			Branch      string `json:"branch"`
			BaseBranch  string `json:"base_branch"`
			Force       bool   `json:"force"`
			WorktreeID  string `json:"worktree_id"`
			ReuseBranch bool   `json:"reuse_branch,omitempty"`
		}
		var createResp struct {
			Success      bool   `json:"success"`
			WorktreePath string `json:"worktree_path"`
			BaseBranch   string `json:"base_branch,omitempty"`
			Error        string `json:"error,omitempty"`
		}

		err := m.Send(ctx, "worktree.create", createReq{
			WorktreeID:  wt.ID,
			ProjectPath: repoPath,
			WorkspaceID: workspaceID,
			SubPath:     repo.RelativePath,
			Name:        wt.Name,
			Branch:      wt.Branch,
			BaseBranch:  repoBase,
			Force:       req.Force && !adopting,
			ReuseBranch: adopting,
		}, &createResp, commandTimeoutMs)
		if err != nil {
			logging.Error("Failed to create git worktree via daemon", "error", err, "repo", repo.ID)
			return repoCreateResult{}, fmt.Errorf("failed to create worktree for repo %s: %w", repo.Name, err)
		}
		if !createResp.Success {
			logging.Error("Failed to create git worktree", "error", createResp.Error, "repo", repo.ID)
			if adopting && (strings.Contains(createResp.Error, "already checked out") || strings.Contains(createResp.Error, "already used by worktree")) {
				return repoCreateResult{}, fmt.Errorf("branch '%s' is already checked out in another worktree of repo %s; the adopt needs it free (it is never reset or forced)", wt.Branch, repo.Name)
			}
			return repoCreateResult{}, gitWorktreeError(createResp.Error, createResp.WorktreePath, wt.Branch, repoBase)
		}
		return repoCreateResult{
			repo:         repo,
			worktreePath: createResp.WorktreePath,
			baseBranch:   firstNonEmpty(createResp.BaseBranch, repoBase),
		}, nil
	}

	// Repos are checked out CONCURRENTLY: each is an independent
	// `git worktree add` into its own subdirectory, so the wait is the slowest
	// repo instead of the sum (measured 6.6s -> 2.8s on three repos). Do not
	// ALSO enable git's parallel checkout; measured slower on top of this.
	//
	//   - A repo nested inside another registered repo waits for its parent
	//     (repopkg.CheckoutWaves): git refuses to populate a parent into a
	//     directory that already exists. Unrelated repos form a single wave.
	//
	//   - A failure does NOT cancel its siblings. Cancelling would only stop
	//     the server waiting; the daemon's git keeps running and the server
	//     never learns the path — the orphaned directory the all-or-nothing
	//     rollback exists to prevent. Every sibling finishes so each success
	//     is known and torn down.
	results := make([]repoCreateResult, len(repos))
	errs := make([]error, len(repos))
	ran := make([]bool, len(repos))
	var firstErr error
	for _, wave := range repopkg.CheckoutWaves(repos) {
		var wg sync.WaitGroup
		for _, i := range wave {
			ran[i] = true
			wg.Go(func() {
				results[i], errs[i] = createRepo(repos[i])
			})
		}
		wg.Wait()

		for _, i := range wave {
			if errs[i] != nil {
				firstErr = errs[i]
				break
			}
		}
		if firstErr != nil {
			break
		}
	}
	// Collected in listed order, not finish order, so the workspace root and
	// display base come from the same repo on every run.
	for i := range repos {
		if ran[i] && errs[i] == nil {
			successes = append(successes, results[i])
		}
	}

	// The workspace root comes from the first success: the daemon returned
	// <HOME>/.reliant/worktrees/<workspace_id>[/<repo.rel>], so strip the
	// trailing repo.RelativePath. Resolved BEFORE fail() so the rollback can
	// remove the root of a partially created workspace.
	if len(successes) > 0 {
		first := successes[0]
		if rel := filepath.Clean(first.repo.RelativePath); rel == "." || rel == "" {
			workspaceRoot = first.worktreePath
		} else {
			workspaceRoot = strings.TrimSuffix(first.worktreePath,
				string(filepath.Separator)+rel)
			if workspaceRoot == first.worktreePath {
				workspaceRoot = filepath.Dir(first.worktreePath)
			}
		}
	}
	if firstErr != nil {
		return fail(firstErr)
	}

	// Carry over the requested paths once for the whole workspace, root to
	// root: the workspace mirrors the project's layout, so `reliant/.env`
	// lands in the reliant checkout and a root-level `.env` at the root.
	//
	// A failed copy does not fail the workspace: the checkouts are complete
	// and usable, and a missing .env is something the user can see and fix.
	if len(req.CopyPaths) > 0 {
		copySource := firstNonEmpty(req.CopySource, project.Path)
		var copyResp struct {
			Copied  []string          `json:"copied"`
			Missing []string          `json:"missing"`
			Failed  map[string]string `json:"failed"`
			Error   string            `json:"error"`
		}
		err := m.Send(ctx, "worktree.copy_paths", map[string]any{
			"source_root": copySource,
			"dest_root":   workspaceRoot,
			"paths":       req.CopyPaths,
		}, &copyResp, commandTimeoutMs)
		switch {
		case err != nil:
			logging.Error("Failed to copy paths into worktree", "error", err, "worktreeID", wt.ID)
		case copyResp.Error != "" || len(copyResp.Failed) > 0:
			logging.Error("Some paths were not copied into worktree", "error", copyResp.Error,
				"failed", copyResp.Failed, "worktreeID", wt.ID)
		case len(copyResp.Missing) > 0:
			logging.Info("Requested copy paths not present in source", "missing", copyResp.Missing,
				"source", copySource, "worktreeID", wt.ID)
		}
	}

	// BaseBranch is the resolved value of the first repo, for display.
	// BaseBranches records every successful repo's resolved base — what
	// CreatePR consults so a repo whose default is `master` doesn't get a PR
	// opened against `main`.
	displayBase := ""
	baseBranches := make(map[string]string, len(successes))
	if len(successes) > 0 {
		displayBase = successes[0].baseBranch
	}
	for _, s := range successes {
		if s.baseBranch != "" {
			baseBranches[s.repo.ID] = s.baseBranch
		}
	}
	// Single-repo legacy: BaseBranch alone is canonical, BaseBranches stays nil
	// so the column is NULL.
	if len(baseBranches) <= 1 {
		baseBranches = nil
	}

	wt.Path = workspaceRoot
	wt.BaseBranch = displayBase
	wt.BaseBranches = baseBranches
	wt.Status = int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)
	wt.UpdatedAt = time.Now().UTC()
	wt.LastActive = wt.UpdatedAt

	if err := store.UpdateWorktree(ctx, wt); err != nil {
		// The checkouts exist on disk but the row cannot record them, so the
		// directories would be unreachable. Tear them down and mark failed.
		return fail(fmt.Errorf("failed to record worktree: %w", err))
	}
	return nil
}

// gitWorktreeError turns daemon git output into a user-facing reason.
func gitWorktreeError(output, worktreePath, branch, baseBranch string) error {
	switch {
	case strings.Contains(output, "already exists") && strings.Contains(output, "not empty"):
		return fmt.Errorf("worktree path '%s' already exists and is not empty; enable force to override", worktreePath)
	case strings.Contains(output, "already checked out"):
		return fmt.Errorf("branch '%s' is already checked out in another worktree; enable force to override or choose a different branch", branch)
	case strings.Contains(output, "is not a valid"):
		return fmt.Errorf("base branch '%s' does not exist; please choose a valid branch", baseBranch)
	case strings.Contains(output, "fatal: invalid reference"):
		return fmt.Errorf("invalid branch or reference '%s'; please check the branch name", baseBranch)
	case strings.Contains(output, "Not a git repository"):
		return fmt.Errorf("project directory is not a valid git repository; please initialize git first")
	default:
		msg := strings.TrimSpace(output)
		if msg == "" {
			msg = "unknown git error"
		}
		return fmt.Errorf("git worktree creation failed: %s", msg)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
