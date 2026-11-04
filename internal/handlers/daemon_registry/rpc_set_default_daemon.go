package daemon_registry

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// SetDefaultDaemon makes one of the caller's machines their default, or
// clears it for an empty daemon_id. Default resolution (toolexec, and so
// daemonplacement's chat pins and worktree placement) reads it from the
// daemon row on its next request; nothing already pinned moves.
func (s *Service) SetDefaultDaemon(
	ctx context.Context,
	req *connect.Request[pb.SetDefaultDaemonRequest],
) (*connect.Response[pb.SetDefaultDaemonResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}
	if err := s.deps.Repo.SetDefaultDaemon(ctx, userID, req.Msg.GetDaemonId()); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to set default machine"))
	}
	return connect.NewResponse(&pb.SetDefaultDaemonResponse{}), nil
}
