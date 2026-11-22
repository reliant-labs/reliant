package worktree

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
)

func (s *Service) ListWorktreeRecoveries(ctx context.Context, req *connect.Request[pb.ListWorktreeRecoveriesRequest]) (*connect.Response[pb.ListWorktreeRecoveriesResponse], error) {
	userID := auth.MustGetUserID(ctx)
	rows, err := s.deps.Recoveries.ListWorktreeRecoveries(ctx, userID, req.Msg.GetProjectId())
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	// A recovery of a project the caller may no longer read (an org they
	// left, a credential's other org) is not listed. One whose project is
	// gone stays (a recovery outlives its source by design), decided as a row
	// of no org: the owner's, reached by a credential only through its
	// owner's default org.
	readable := map[string]bool{}
	rows = authz.Visible(ctx, userID, rows, func(ctx context.Context, userID string, row *core.WorktreeRecovery) bool {
		if row.ProjectID == "" {
			return authz.CanReadOwn(ctx, userID, nil)
		}
		ok, seen := readable[row.ProjectID]
		if !seen {
			project, getErr := s.deps.Repo.GetProject(ctx, row.ProjectID)
			ok = (svcerr.IsNotFound(getErr) && authz.CanReadOwn(ctx, userID, nil)) || (getErr == nil && authz.CanReadProject(ctx, userID, project))
			readable[row.ProjectID] = ok
		}
		return ok
	})
	out := &pb.ListWorktreeRecoveriesResponse{}
	for _, row := range rows {
		if req.Msg.GetSourceWorktreeId() == "" || row.SourceWorktreeID == req.Msg.GetSourceWorktreeId() {
			out.Recoveries = append(out.Recoveries, recoveryToProto(row))
		}
	}
	return connect.NewResponse(out), nil
}
