package daemon_registry

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/diskwatch"
)

func (s *Service) GetDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.GetDaemonRequest],
) (*connect.Response[reliantv1.GetDaemonResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}
	if req.Msg.GetDaemonId() == "" {
		return nil, svcerr.InvalidArgument("daemon_id is required")
	}

	daemon, err := s.deps.Repo.GetDaemon(ctx, req.Msg.GetDaemonId())
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get daemon"))
	}
	if !authz.CanReadMachine(ctx, userID, daemon) {
		return nil, svcerr.NotFound("daemon")
	}

	attached, err := s.attachedDaemonSet(ctx, userID)
	if err != nil {
		return nil, err
	}
	info := daemonToProto(daemon, attached[daemon.ID])
	info.LocalModels = localModelsFromJSON(s.storedLocalModels(ctx, userID)[daemon.ID])
	info.Disk = diskwatch.Decode(s.storedDiskReports(ctx, userID)[daemon.ID])
	return connect.NewResponse(&reliantv1.GetDaemonResponse{Daemon: info}), nil
}
