package project

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// ListRepositoriesForDaemon returns the projects cloned on the given daemon,
// joined with project metadata (name + remote_url) so admin callers can
// render a table without per-row GetProject calls. Filtered to projects
// owned by the calling user.
func (s *Service) ListRepositoriesForDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.ListRepositoriesForDaemonRequest],
) (*connect.Response[reliantv1.ListRepositoriesForDaemonResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if req.Msg.DaemonId == "" {
		return nil, svcerr.InvalidArgument("daemon_id is required")
	}
	rows, err := s.deps.Installs.ListProjectDaemonsForDaemon(ctx, req.Msg.DaemonId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list repositories"))
	}
	out := make([]*reliantv1.DaemonRepository, 0, len(rows))
	for _, pd := range rows {
		project, err := s.deps.Projects.GetProjectWithUserCheck(ctx, pd.ProjectID, userID)
		if err != nil {
			// Either the project belongs to a different user or it was
			// hard-deleted while still leaving the FK-cascaded row. Skip
			// quietly so the admin table doesn't show orphans.
			continue
		}
		branch := ""
		if pd.DefaultBranch != nil {
			branch = *pd.DefaultBranch
		} else if project.DefaultBranch != nil {
			branch = *project.DefaultBranch
		}
		remoteURL := ""
		if project.RemoteURL != nil {
			remoteURL = *project.RemoteURL
		}
		out = append(out, &reliantv1.DaemonRepository{
			ProjectId: project.ID,
			Name:      project.Name,
			RemoteUrl: remoteURL,
			Branch:    branch,
			Path:      pd.Path,
			ClonedAt:  pd.ClonedAt.Format(time.RFC3339),
		})
	}
	return connect.NewResponse(&reliantv1.ListRepositoriesForDaemonResponse{
		Repositories: out,
	}), nil
}
