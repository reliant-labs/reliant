package pipeline

import (
	"context"
	"errors"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// EnrolRepository enrols a repository of the project's organization, mode
// off. The caller owns the project and is an owner or admin of its org. The provider keys are the caller's own fresh GitHub access grant to
// the repository (integration_event_access): the App installation id and the
// repository's stable id, the same values every webhook delivery, and so
// every pr_records row, carries. Re-enrolling under the same project
// refreshes the keys and the name (idempotent); another project's enrolment
// is AlreadyExists.
func (s *Service) EnrolRepository(
	ctx context.Context,
	req *connect.Request[pb.EnrolRepositoryRequest],
) (*connect.Response[pb.EnrolRepositoryResponse], error) {
	if err := s.enabled(); err != nil {
		return nil, svcerr.Wrap(err)
	}
	userID, err := callerID(ctx)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	provider := strings.TrimSpace(req.Msg.GetProvider())
	if provider == "" {
		provider = providerGitHub
	}
	if provider != providerGitHub {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("provider must be github, the only one the pipeline supports"))
	}
	repository, err := repositoryArg(req.Msg.GetRepository())
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	project, err := s.deps.Store.GetProject(ctx, strings.TrimSpace(req.Msg.GetProjectId()))
	if err != nil && !errors.Is(err, svcerr.ErrNotFound) {
		return nil, svcerr.Wrap(err)
	}
	// The project's owner enrols its repositories: a viewer of a shared
	// project is answered as an outsider is.
	if project == nil || !authz.CanWriteProject(ctx, userID, project) {
		return nil, svcerr.Wrap(svcerr.NotFound("project"))
	}
	if project.OrgID == nil || *project.OrgID == "" {
		return nil, svcerr.Wrap(svcerr.FailedPrecondition("the project belongs to no organization; the pipeline enrols an organization's repositories"))
	}
	orgID := *project.OrgID
	w, err := requirePipelineAdmin(ctx, userID, orgID, "enrol a repository in Reliant's pipeline")
	if err != nil {
		return nil, svcerr.Wrap(err)
	}

	grants, err := s.deps.Store.FreshIntegrationAccessByLabel(ctx, userID, providerGitHub, repository, s.now().Add(-core.IntegrationAccessFreshness))
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	var ids []string
	for _, g := range grants {
		if !slices.Contains(ids, g.ResourceKey) {
			ids = append(ids, g.ResourceKey)
		}
	}
	switch {
	case len(ids) == 0:
		return nil, svcerr.Wrap(svcerr.WithReason(svcerr.FailedPrecondition(
			"your GitHub access does not reach "+repository+": install Reliant's GitHub App on it and connect GitHub "+
				"in Reliant, then try again in a minute"), ReasonNoRepositoryAccess))
	case len(ids) > 1:
		return nil, svcerr.Wrap(svcerr.FailedPrecondition(
			"more than one repository you can reach was last seen as " + repository + "; try again once GitHub access refreshes"))
	}
	// One repository; through more than one installation only while an App
	// moves between accounts. The grants are ordered, so the choice is stable.
	g := grants[0]
	repo, err := s.deps.Store.UpsertPipelineRepo(ctx, core.PipelineRepoEnrolment{
		OrgID: orgID, ProjectID: project.ID, Provider: providerGitHub,
		AccountKey: g.AccountKey, RepositoryID: g.ResourceKey, Repository: g.ResourceLabel,
		CreatedByUserID: userID,
	})
	if errors.Is(err, core.ErrPipelineRepoEnrolledElsewhere) {
		return nil, svcerr.Wrap(svcerr.WithReason(svcerr.AlreadyExists(
			repository+" is enrolled in Reliant's pipeline by another project"), ReasonEnrolledElsewhere))
	}
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	s.deps.Logger.InfoContext(ctx, "pipeline: repository enrolled",
		"repo_id", repo.ID, "repository", repo.Repository, "repository_id", repo.RepositoryID, "account_key", repo.AccountKey,
		"org_id", orgID, "project_id", project.ID, "mode", string(repo.Mode), "version", repo.Version,
		"user_id", w.userID, "credential", w.credential, "token_id", w.tokenID)
	return connect.NewResponse(&pb.EnrolRepositoryResponse{Repository: repoProto(repo)}), nil
}
