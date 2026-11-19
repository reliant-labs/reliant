package daemon_registry

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/diskwatch"
)

func (s *Service) ListDaemons(
	ctx context.Context,
	req *connect.Request[reliantv1.ListDaemonsRequest],
) (*connect.Response[reliantv1.ListDaemonsResponse], error) {
	_ = req

	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}

	// Trigger daemon connection check and wait briefly for it to register
	// before querying the DB. No-op in cloud mode where the daemon is external.
	if s.deps.Router != nil {
		_, _ = s.deps.Router.IsDaemonOnline(ctx, userID, nil)
	}

	daemons, err := s.deps.Repo.ListDaemonsByUserID(ctx, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list daemons"))
	}

	attached, err := s.attachedDaemonSet(ctx, userID)
	if err != nil {
		return nil, err
	}
	inventories := s.storedLocalModels(ctx, userID)
	disks := s.storedDiskReports(ctx, userID)

	resp := &reliantv1.ListDaemonsResponse{Daemons: make([]*reliantv1.DaemonInfo, 0, len(daemons))}
	for _, d := range daemons {
		info := daemonToProto(d, attached[d.ID])
		info.LocalModels = localModelsFromJSON(inventories[d.ID])
		info.Disk = diskwatch.Decode(disks[d.ID])
		resp.Daemons = append(resp.Daemons, info)
	}

	return connect.NewResponse(resp), nil
}
