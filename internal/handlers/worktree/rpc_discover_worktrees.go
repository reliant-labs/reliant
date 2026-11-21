package worktree

import (
	"context"
	"path/filepath"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/machinewake"
	"github.com/reliant-labs/reliant/internal/worktreepath"
)

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
	// A background poll (the sidebar hint) never wakes a suspended machine:
	// it answers Unavailable and the hint stays hidden.
	if req.Msg.Background {
		ctx = machinewake.Suppress(ctx)
	}

	// The same machine a new workspace for this project would be placed on,
	// and the project's checkout there.
	placement, err := s.placeNewWorktree(ctx, userID, project.ID, nil, nil, req.Msg.DaemonId, "discover")
	if err != nil {
		return nil, err
	}
	target, err := s.withCheckout(ctx, userID, project, placement, "list its git worktrees")
	if err != nil {
		return nil, err
	}
	placement = target.machine
	refs, byID, err := s.projectRepoRefs(ctx, project, target.checkout)
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
