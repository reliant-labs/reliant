package daemon_registry

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// RequestDaemonRestart is deliberately admin-only. Ordinary API keys and
// machine credentials authenticate with role authenticated, never admin.
func (s *Service) RequestDaemonRestart(ctx context.Context, req *connect.Request[pb.RequestDaemonRestartRequest]) (*connect.Response[pb.RequestDaemonRestartResponse], error) {
	if _, ok := auth.GetUserIDFromContext(ctx); !ok {
		return nil, svcerr.ErrUnauthenticated
	}
	if role, _ := ctx.Value(auth.UserRoleContextKey).(string); role != "admin" {
		return nil, svcerr.PermissionDenied("admin role required")
	}
	if req.Msg.DaemonId == "" || strings.TrimSpace(req.Msg.Reason) == "" {
		return nil, svcerr.InvalidArgument("daemon_id and reason are required")
	}
	daemon, err := s.deps.Repo.GetDaemon(ctx, req.Msg.DaemonId)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	if daemon == nil {
		return nil, svcerr.NotFound("daemon")
	}
	if s.deps.Router == nil {
		return nil, svcerr.Unavailable("no daemon router configured")
	}
	status, err := s.deps.Router.RequestDaemonRestart(ctx, daemon.UserID, daemon.ID, &pb.RestartRequested{Reason: req.Msg.Reason, DeadlineUnixMs: req.Msg.DeadlineUnixMs})
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	return connect.NewResponse(&pb.RequestDaemonRestartResponse{RestartStatus: status}), nil
}
