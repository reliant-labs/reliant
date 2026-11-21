package worktree

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/machinewake"
	"github.com/reliant-labs/reliant/internal/projectcheckout"
)

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

// projectRepoRefs lists the project's repos with their paths in checkout, the
// project's checkout on the machine asked. A repo with relative path "" or
// "." is the checkout itself.
func (s *Service) projectRepoRefs(ctx context.Context, project *db.Project, checkout projectcheckout.Target) ([]discoverRepoRef, map[string]*core.Repo, error) {
	repos, err := s.deps.Repo.ListReposByProject(ctx, project.ID)
	if err != nil {
		return nil, nil, svcerr.WrapOr(err, svcerr.Internal("failed to list repos for project"))
	}
	refs := make([]discoverRepoRef, 0, len(repos))
	byID := make(map[string]*core.Repo, len(repos))
	for _, r := range repos {
		refs = append(refs, discoverRepoRef{RepoID: r.ID, Path: checkout.Join(r.RelativePath)})
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
