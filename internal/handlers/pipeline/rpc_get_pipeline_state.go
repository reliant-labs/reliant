package pipeline

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// stateBatches is how many of the newest batches GetPipelineState returns.
const stateBatches = 5

// GetPipelineState returns an enrolled repository (by id, or by owner/name
// among the enrolments of the caller's organizations), its PRs counted by
// pipeline state, its newest batches and the newest verdict on its base
// branch.
func (s *Service) GetPipelineState(
	ctx context.Context,
	req *connect.Request[pb.GetPipelineStateRequest],
) (*connect.Response[pb.GetPipelineStateResponse], error) {
	if err := s.enabled(); err != nil {
		return nil, svcerr.Wrap(err)
	}
	userID, err := callerID(ctx)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	var repo *core.PipelineRepo
	if id := strings.TrimSpace(req.Msg.GetRepoId()); id != "" {
		repo, err = s.readRepo(ctx, userID, id)
	} else {
		repo, err = s.readRepoByName(ctx, userID, req.Msg.GetRepository())
	}
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	counts, err := s.deps.Store.CountPRPipelinesByState(ctx, repo.ID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	batches, err := s.deps.Store.LatestQueueBatches(ctx, repo.ID, stateBatches)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	health, err := s.deps.Store.LatestMainHealth(ctx, repo.ID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	out := &pb.GetPipelineStateResponse{Repository: repoProto(repo), MainHealth: mainHealthProto(health)}
	for _, c := range counts {
		out.PrStates = append(out.PrStates, &pb.PipelineStateCount{State: string(c.State), Count: int32(c.Count)})
	}
	for _, b := range batches {
		out.LatestBatches = append(out.LatestBatches, batchProto(b))
	}
	return connect.NewResponse(out), nil
}

// readRepoByName is the one enrolment last observed as owner/name in an
// organization the caller is a member of.
func (s *Service) readRepoByName(ctx context.Context, userID, name string) (*core.PipelineRepo, error) {
	if strings.TrimSpace(name) == "" {
		return nil, svcerr.InvalidArgument("repo_id or repository is required")
	}
	repository, err := repositoryArg(name)
	if err != nil {
		return nil, err
	}
	all, err := s.deps.Store.ListPipelineReposByRepository(ctx, providerGitHub, repository)
	if err != nil {
		return nil, err
	}
	var readable []*core.PipelineRepo
	for _, r := range all {
		switch err := orgs.RequireMember(ctx, userID, r.OrgID); {
		case err == nil:
			readable = append(readable, r)
		case svcerr.Code(err) == connect.CodeUnavailable:
			return nil, err
		}
	}
	switch len(readable) {
	case 0:
		return nil, svcerr.NotFound("pipeline repository " + repository)
	case 1:
		return readable[0], nil
	default:
		return nil, svcerr.FailedPrecondition("more than one enrolment was last seen as " + repository + "; name it by repo_id")
	}
}
