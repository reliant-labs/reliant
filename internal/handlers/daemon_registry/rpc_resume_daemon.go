package daemon_registry

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
)

func (s *Service) ResumeDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.ResumeDaemonRequest],
) (*connect.Response[reliantv1.ResumeDaemonResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}

	if req.Msg.DaemonId == "" {
		return nil, svcerr.InvalidArgument("daemon_id is required")
	}

	// Verify ownership.
	daemon, err := s.deps.Repo.GetDaemon(ctx, req.Msg.DaemonId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get daemon"))
	}
	if !authz.CanWriteMachine(ctx, userID, daemon) {
		return nil, svcerr.NotFound("daemon")
	}

	// If the daemon currently has a fresh attachment row, it's already routable.
	attached, err := s.attachedDaemonSet(ctx, userID)
	if err != nil {
		return nil, err
	}
	if attached[daemon.ID] != nil {
		return connect.NewResponse(&reliantv1.ResumeDaemonResponse{Resumed: true}), nil
	}

	// This service cannot wake a machine: resuming a suspended managed daemon
	// is control-plane's (controlplane.v1.DaemonService/ResumeDaemon, which
	// the router's EnsureAwake and the web client call directly). Nothing
	// overrides this RPC any more — control-plane deleted its
	// reliant.v1.DaemonRegistryService adapter (docs/design/one-daemon-list.md).
	return connect.NewResponse(&reliantv1.ResumeDaemonResponse{
		Resumed:      false,
		ErrorMessage: fmt.Sprintf("daemon %s has no active attachment; automatic resume not available in OSS mode", req.Msg.DaemonId),
	}), nil
}
