package daemon_registry

import (
	"context"
	"slices"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

func (s *Service) ResolveDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.ResolveDaemonRequest],
) (*connect.Response[reliantv1.ResolveDaemonResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}

	daemons, err := s.deps.Repo.ListDaemonsByUserID(ctx, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("listing daemons"))
	}

	attached, err := s.attachedDaemonSet(ctx, userID)
	if err != nil {
		return nil, err
	}

	var best *db.Daemon
	// A request that names no machine gets the user's default, in any state,
	// exactly as toolexec's default resolution does.
	if req.Msg.DaemonId == "" && req.Msg.DaemonName == "" {
		if i := slices.IndexFunc(daemons, func(d *db.Daemon) bool { return d.IsDefault }); i >= 0 {
			best = daemons[i]
		}
	}
	for _, d := range daemons {
		if best != nil && best.IsDefault && req.Msg.DaemonId == "" && req.Msg.DaemonName == "" {
			break // the default chosen above
		}
		if req.Msg.DaemonId != "" && d.ID != req.Msg.DaemonId {
			continue
		}
		if req.Msg.DaemonName != "" && !daemonAnswersToName(d, req.Msg.DaemonName) {
			continue
		}
		// Prefer attached (routable) daemons, but accept any match.
		if best == nil || attached[d.ID] != nil {
			best = d
		}
		if attached[d.ID] != nil {
			break
		}
	}

	if best == nil {
		return connect.NewResponse(&reliantv1.ResolveDaemonResponse{Found: false}), nil
	}

	return connect.NewResponse(&reliantv1.ResolveDaemonResponse{
		Daemon: daemonToProto(best, attached[best.ID]),
		Found:  true,
	}), nil
}
