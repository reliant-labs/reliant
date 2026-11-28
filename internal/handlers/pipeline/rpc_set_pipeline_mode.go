package pipeline

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// SetPipelineMode sets an enrolled repository's mode (off, shadow or on), a
// compare-and-set on the version the caller read: a lost race is Aborted.
// Off is unenrolling: nothing acts, and the project may move to another org
// again (G1). A repository whose project has left the org it was enrolled
// in is never turned back on.
func (s *Service) SetPipelineMode(
	ctx context.Context,
	req *connect.Request[pb.SetPipelineModeRequest],
) (*connect.Response[pb.SetPipelineModeResponse], error) {
	if err := s.enabled(); err != nil {
		return nil, svcerr.Wrap(err)
	}
	userID, err := callerID(ctx)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	mode := core.PipelineRepoMode(strings.TrimSpace(req.Msg.GetMode()))
	if !slices.Contains(core.AllPipelineRepoModes, mode) {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("mode must be off, shadow or on"))
	}
	repo, err := s.deps.Store.GetPipelineRepo(ctx, strings.TrimSpace(req.Msg.GetRepoId()))
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	if repo == nil {
		return nil, svcerr.Wrap(svcerr.NotFound("pipeline repository"))
	}
	w, err := requirePipelineAdmin(ctx, userID, repo.OrgID, "change a repository's pipeline mode")
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	if mode != core.PipelineRepoModeOff {
		project, err := s.deps.Store.GetProject(ctx, repo.ProjectID)
		if err != nil {
			return nil, svcerr.Wrap(err)
		}
		if project.OrgID == nil || *project.OrgID != repo.OrgID {
			return nil, svcerr.Wrap(svcerr.FailedPrecondition(
				"the project is no longer in the organization " + repo.Repository + " was enrolled in"))
		}
	}
	from := repo.Mode
	updated, err := s.deps.Store.SetPipelineRepoMode(ctx, repo.ID, req.Msg.GetExpectedVersion(), mode)
	if errors.Is(err, core.ErrPipelineVersionConflict) {
		return nil, svcerr.Wrap(svcerr.Aborted(fmt.Sprintf(
			"%s changed since you read it (expected version %d); read it again and retry", repo.Repository, req.Msg.GetExpectedVersion())))
	}
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	s.deps.Logger.InfoContext(ctx, "pipeline: repository mode set",
		"repo_id", updated.ID, "repository", updated.Repository, "org_id", updated.OrgID, "project_id", updated.ProjectID,
		"from", string(from), "to", string(updated.Mode), "version", updated.Version,
		"user_id", w.userID, "credential", w.credential, "token_id", w.tokenID)
	return connect.NewResponse(&pb.SetPipelineModeResponse{Repository: repoProto(updated)}), nil
}
