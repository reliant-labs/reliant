package worktree

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/machinewake"
	"github.com/reliant-labs/reliant/internal/workspacecreate"
	"github.com/reliant-labs/reliant/internal/worktreepath"
)

// =============================================================================
// Import/Discovery Operations
// =============================================================================

// ImportWorktree adopts a git worktree made outside Reliant. The path must be
// a linked worktree the daemon currently reports for repo_id that no row
// tracks and that is not Reliant's own; the client's word is never enough.
//
//   - Single-repo project: the row is registered in place (Path = the checkout).
//     Nothing moves and nothing is created. It is NOT locked: the reclaimer
//     only settles rows under the daemon's worktrees root (anything else is
//     "foreign" and left alone), so a lock would protect nothing.
//   - Multi-repo project: the checkout is moved to <workspace>/<repo rel> with
//     `git worktree move`, then the other repos get checkouts on the same
//     branch, exactly like a UI-made workspace. It needs confirm_move. If any
//     step after the move fails the checkout is moved back (see
//     workspacecreate.ExistingCheckout) and the row ends FAILED.
func (s *Service) ImportWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.ImportWorktreeRequest],
) (*connect.Response[reliantv1.ImportWorktreeResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.Path == "" {
		return nil, svcerr.InvalidArgument("path is required")
	}
	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}
	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}
	project, err := s.deps.Repo.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}
	// An empty registry is reconciled against the machine the import would
	// be placed on; a populated one reaches no daemon before confirm_move.
	repos, err := s.listProjectRepos(ctx, userID, project, func() (machinewake.Target, error) {
		return s.placeNewWorktree(ctx, userID, project.ID, req.Msg.ChatId, nil, req.Msg.DaemonId, "import")
	})
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*core.Repo, len(repos))
	for _, r := range repos {
		byID[r.ID] = r
	}
	var repo *core.Repo
	switch {
	case req.Msg.RepoId != "":
		if repo = byID[req.Msg.RepoId]; repo == nil {
			return nil, svcerr.WithMessage(svcerr.ErrNotFound, "repo not in project")
		}
	case len(repos) == 1:
		repo = repos[0]
	default:
		return nil, svcerr.InvalidArgument("repo_id is required in multi-repo projects")
	}
	moves := importMoves(byID)
	if moves && !req.Msg.ConfirmMove {
		return nil, svcerr.InvalidArgument("importing this worktree moves its directory into a new workspace; confirm the move (confirm_move) to continue")
	}

	placement, err := s.placeNewWorktree(ctx, userID, project.ID, req.Msg.ChatId, nil, req.Msg.DaemonId, "import")
	if err != nil {
		return nil, err
	}
	if err := s.wakeIfAsleep(ctx, userID, placement, project.Path); err != nil {
		return nil, err
	}
	ownerDaemonID := placement.DaemonID

	// Re-discover on the daemon and take the entry from there.
	exclude, err := s.trackedWorktreePaths(ctx, project.ID, placement.DaemonID)
	if err != nil {
		return nil, err
	}
	repoPath := filepath.Join(project.Path, repo.RelativePath)
	var found struct {
		Worktrees []discoveredEntry `json:"worktrees"`
	}
	payload := map[string]any{"repos": []discoverRepoRef{{RepoID: repo.ID, Path: repoPath}}, "exclude_roots": exclude}
	if err := s.sendToMachine(ctx, userID, placement, "worktree.discover_repos", payload, &found, worktreeReadCommandTimeoutMs); err != nil {
		return nil, importWorktreeError("validate path", err)
	}
	want := filepath.Clean(req.Msg.Path)
	var entry *discoveredEntry
	for i := range found.Worktrees {
		if !found.Worktrees[i].Prunable && filepath.Clean(found.Worktrees[i].Path) == want {
			entry = &found.Worktrees[i]
			break
		}
	}
	if entry == nil {
		return nil, svcerr.InvalidArgument(fmt.Sprintf("%s is not an untracked linked worktree of repo '%s'; refresh the list and try again", req.Msg.Path, repo.Name))
	}
	if entry.Branch == "" {
		return nil, svcerr.FailedPrecondition(fmt.Sprintf("%s has a detached HEAD, so there is no branch to adopt; create one in it first (git switch -c <branch>) and try again", entry.Path))
	}

	name := entry.Name
	if req.Msg.Name != nil && *req.Msg.Name != "" {
		name = *req.Msg.Name
	}
	var chatID *string
	if req.Msg.ChatId != nil && *req.Msg.ChatId != "" {
		chatID = req.Msg.ChatId
	}
	createReq := workspacecreate.Request{
		Project:       project,
		Repos:         repos,
		Name:          name,
		Branch:        entry.Branch,
		ChatID:        chatID,
		OwnerDaemonID: ownerDaemonID,
	}
	if moves {
		createReq.WorkspaceID = worktreepath.WorkspaceDirName(project.Name, name)
	}
	worktree, err := workspacecreate.Insert(ctx, s.deps.Repo, createReq)
	if err != nil {
		return nil, insertWorktreeError(err, name)
	}
	machine := worktreeMachine{s: s, userID: userID, daemonID: ownerDaemonID}

	if !moves {
		var def struct {
			DefaultBranch string `json:"default_branch"`
		}
		_ = machine.Send(ctx, "worktree.get_default_branch", map[string]string{"repo_path": repoPath}, &def, worktreeReadCommandTimeoutMs)
		createReq.Existing = map[string]workspacecreate.ExistingCheckout{
			repo.ID: {Path: entry.Path, Base: def.DefaultBranch},
		}
		if err := workspacecreate.Finish(ctx, s.deps.Repo, machine, createReq, worktree); err != nil {
			s.emitWorktreeChanged(ctx, userID, project.ID, worktree.ID)
			return nil, importWorktreeError("import worktree", err)
		}
		s.emitWorktreeChanged(ctx, userID, project.ID, worktree.ID)
		return connect.NewResponse(&reliantv1.ImportWorktreeResponse{Worktree: worktreeToProto(worktree)}), nil
	}

	var moved struct {
		Success      bool   `json:"success"`
		WorktreePath string `json:"worktree_path"`
		BaseBranch   string `json:"base_branch"`
		Error        string `json:"error"`
	}
	moveErr := machine.Send(ctx, "worktree.adopt_move", map[string]string{
		"repo_path":    repoPath,
		"src":          entry.Path,
		"workspace_id": createReq.WorkspaceID,
		"sub_path":     repo.RelativePath,
		"worktree_id":  worktree.ID,
	}, &moved, workspaceMoveTimeoutMs)
	if moveErr != nil || !moved.Success {
		// Nothing has moved (git refused, or the command never ran): the row
		// is FAILED with no path and the user's checkout is where it was.
		worktree.Status = int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED)
		worktree.Path = ""
		worktree.UpdatedAt = time.Now().UTC()
		if err := s.deps.Repo.UpdateWorktree(ctx, worktree); err != nil {
			logging.ErrorContext(ctx, "Failed to mark adopted worktree failed", "error", err, "worktreeID", worktree.ID)
		}
		s.emitWorktreeChanged(ctx, userID, project.ID, worktree.ID)
		if moveErr != nil {
			return nil, importWorktreeError("move the worktree", moveErr)
		}
		return nil, svcerr.FailedPrecondition(fmt.Sprintf("could not move %s: %s", entry.Path, moved.Error))
	}

	createReq.Existing = map[string]workspacecreate.ExistingCheckout{
		repo.ID: {Path: moved.WorktreePath, Base: moved.BaseBranch, OriginalPath: entry.Path},
	}
	// The other repos' checkouts take as long as a create, so finish in the
	// background like CreateWorktree; the row stays CREATING until then.
	bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), worktreeCreateBudget)
	pending := *worktree
	go func() {
		defer cancel()
		s.finishWorktreeCreate(bgCtx, userID, createReq, &pending)
	}()
	return connect.NewResponse(&reliantv1.ImportWorktreeResponse{Worktree: worktreeToProto(worktree)}), nil
}

// importWorktreeError is the error a failed ImportWorktree step returns:
//
//   - a machine found asleep is being woken (machinewake): returned as it is,
//     for the client to retry once the machine is up;
//   - the machine's state explains it — none, starting, not connected, not
//     answering — so the state picks the code, prefixed with what was being
//     done (daemonerr.MachineState);
//   - the user's to fix (a branch checked out elsewhere, a base that does
//     not exist): its own code and the sentence saying what to change;
//   - a verdict already made (a deadline, a store's NotFound) keeps its code;
//   - anything else is ours: Internal, saying only that doing failed — the
//     cause stays off the wire and reaches the log through the interceptor.
//
// The machine states used to be Internal too, so a user with no machine
// connected was told the server had failed.
func importWorktreeError(doing string, err error) error {
	if machinewake.IsWaking(err) {
		return err
	}
	if serr, ok := daemonerr.MachineState(doing, err); ok {
		return serr
	}
	if svcerr.IsUserError(err) {
		return svcerr.Wrap(err)
	}
	return svcerr.WrapOr(err, svcerr.Internal(fmt.Sprintf("failed to %s", doing)))
}

// workspaceMoveTimeoutMs bounds adopt_move: a rename on one filesystem, but
// git also rewrites the worktree's records.
const workspaceMoveTimeoutMs int32 = 60_000

// discoverRepoRef is a project repo as the daemon's discover/prune commands
// take it.
type discoverRepoRef struct {
	RepoID string `json:"repo_id"`
	Path   string `json:"path"`
}

// importMoves reports whether importing a worktree of this project moves the
// directory (a multi-repo project, or a repo below the project root) rather
// than registering it in place.
func importMoves(repos map[string]*core.Repo) bool {
	if len(repos) > 1 {
		return true
	}
	for _, r := range repos {
		if rel := filepath.Clean(r.RelativePath); rel != "." && rel != "" {
			return true
		}
	}
	return false
}

// trackedWorktreePaths lists every path a row of the project holds on
// daemonID, archived included, for the daemon to leave out of discovery: a
// tracked checkout is not offered for import again. A path is one machine's:
// a row another machine owns says nothing about the directory at the same
// absolute path here, so it is not excluded. A row that records no owner
// might be this machine's, so it is (conservatively) excluded. daemonID ""
// (default resolution) excludes every row.
func (s *Service) trackedWorktreePaths(ctx context.Context, projectID, daemonID string) ([]string, error) {
	tracked, err := s.deps.Repo.ListWorktrees(ctx, db.WorktreeFilters{
		ProjectID: &projectID, IncludeArchived: true, Limit: 100000,
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list worktrees"))
	}
	paths := make([]string, 0, len(tracked))
	for _, wt := range tracked {
		if wt.Path == "" {
			continue
		}
		if owner := daemonplacement.WorktreeOwner(wt); daemonID != "" && owner != "" && owner != daemonID {
			continue
		}
		paths = append(paths, wt.Path)
	}
	return paths, nil
}

type discoveredEntry struct {
	RepoID         string `json:"repo_id"`
	Path           string `json:"path"`
	Name           string `json:"name"`
	Branch         string `json:"branch"`
	Head           string `json:"head"`
	Locked         bool   `json:"locked"`
	Prunable       bool   `json:"prunable"`
	PrunableReason string `json:"prunable_reason"`
}

// projectRepoRefs lists the project's repos with their checkout paths on the
// daemon. A repo with relative path "" or "." is the project root itself.
func (s *Service) projectRepoRefs(ctx context.Context, project *db.Project) ([]discoverRepoRef, map[string]*core.Repo, error) {
	repos, err := s.deps.Repo.ListReposByProject(ctx, project.ID)
	if err != nil {
		return nil, nil, svcerr.WrapOr(err, svcerr.Internal("failed to list repos for project"))
	}
	refs := make([]discoverRepoRef, 0, len(repos))
	byID := make(map[string]*core.Repo, len(repos))
	for _, r := range repos {
		refs = append(refs, discoverRepoRef{RepoID: r.ID, Path: filepath.Join(project.Path, r.RelativePath)})
		byID[r.ID] = r
	}
	return refs, byID, nil
}

func staleToProto(e discoveredEntry, byID map[string]*core.Repo) *reliantv1.StaleWorktree {
	st := &reliantv1.StaleWorktree{Path: e.Path, RepoId: e.RepoID, Reason: e.PrunableReason}
	if r := byID[e.RepoID]; r != nil {
		st.RepoName = r.Name
	}
	return st
}

// DiscoverWorktrees lists the git worktrees of each of the project's repos
// that Reliant does not track, and git's stale records separately. It is
// read-only.
func (s *Service) DiscoverWorktrees(
	ctx context.Context,
	req *connect.Request[reliantv1.DiscoverWorktreesRequest],
) (*connect.Response[reliantv1.DiscoverWorktreesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}
	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}
	project, err := s.deps.Repo.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}
	refs, byID, err := s.projectRepoRefs(ctx, project)
	if err != nil {
		return nil, err
	}

	// A background poll (the sidebar hint) never wakes a suspended machine:
	// it answers Unavailable and the hint stays hidden.
	if req.Msg.Background {
		ctx = machinewake.Suppress(ctx)
	}

	// The same machine a new workspace for this project would be placed on.
	placement, err := s.placeNewWorktree(ctx, userID, project.ID, nil, nil, req.Msg.DaemonId, "discover")
	if err != nil {
		return nil, err
	}
	exclude, err := s.trackedWorktreePaths(ctx, project.ID, placement.DaemonID)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Worktrees     []discoveredEntry `json:"worktrees"`
		WorktreesRoot string            `json:"worktrees_root"`
	}
	payload := map[string]any{"repos": refs, "exclude_roots": exclude}
	if err := s.sendToMachine(ctx, userID, placement, "worktree.discover_repos", payload, &resp, worktreeReadCommandTimeoutMs); err != nil {
		if machinewake.IsWaking(err) {
			return nil, err
		}
		if req.Msg.Background && daemonerr.Unreachable(err) {
			return nil, svcerr.Unavailable("your machine is not connected")
		}
		return nil, daemonerr.Error("list git worktrees", err)
	}

	// Importing moves the checkout when the workspace layout cannot hold it
	// in place: more than one repo, or a repo below the project root.
	moves := importMoves(byID)

	out := &reliantv1.DiscoverWorktreesResponse{}
	if resp.WorktreesRoot != "" {
		out.WorkspacesRoot = filepath.Join(resp.WorktreesRoot, worktreepath.ProjectDirName(project.Name))
	}
	for _, e := range resp.Worktrees {
		if e.Prunable {
			out.Stale = append(out.Stale, staleToProto(e, byID))
			continue
		}
		d := &reliantv1.DiscoveredWorktree{
			Path: e.Path, Name: e.Name, Branch: e.Branch, RepoId: e.RepoID,
			Head: e.Head, Locked: e.Locked, MovesOnImport: moves,
		}
		if r := byID[e.RepoID]; r != nil {
			d.RepoName = r.Name
		}
		out.Discovered = append(out.Discovered, d)
	}
	return connect.NewResponse(out), nil
}

// PruneWorktrees removes git's records of the project's worktrees whose
// directories no longer exist. Only an explicit request does this.
func (s *Service) PruneWorktrees(
	ctx context.Context,
	req *connect.Request[reliantv1.PruneWorktreesRequest],
) (*connect.Response[reliantv1.PruneWorktreesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}
	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}
	project, err := s.deps.Repo.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}
	refs, byID, err := s.projectRepoRefs(ctx, project)
	if err != nil {
		return nil, err
	}
	placement, err := s.placeNewWorktree(ctx, userID, project.ID, nil, nil, req.Msg.DaemonId, "prune")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Pruned []discoveredEntry `json:"pruned"`
		Errors []string          `json:"errors"`
	}
	if err := s.sendToMachine(ctx, userID, placement, "worktree.prune", map[string]any{"repos": refs}, &resp, worktreeDaemonCommandTimeoutMs); err != nil {
		if machinewake.IsWaking(err) {
			return nil, err
		}
		return nil, daemonerr.Error("prune git worktrees", err)
	}
	for _, msg := range resp.Errors {
		logging.WarnContext(ctx, "worktree prune error", "projectID", project.ID, "error", msg)
	}
	if len(resp.Pruned) == 0 && len(resp.Errors) > 0 {
		return nil, svcerr.FailedPrecondition(fmt.Sprintf("git error: %s", resp.Errors[0]))
	}
	out := &reliantv1.PruneWorktreesResponse{}
	for _, e := range resp.Pruned {
		out.Pruned = append(out.Pruned, staleToProto(e, byID))
	}
	if len(out.Pruned) == 0 {
		activity.Skip(ctx) // git had no stale records: nothing was pruned
	}
	return connect.NewResponse(out), nil
}
