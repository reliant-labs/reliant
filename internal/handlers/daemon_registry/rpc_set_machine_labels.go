package daemon_registry

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/machinepool"
)

// SetMachineLabels replaces one of the caller's machines' durable labels.
//
// A label decides which machine pools a machine is in, and so where
// pool-bound runs execute: only a person signed in to the app sets one, or an
// agent's call a person approved on its card (requirePoolWriter). A daemon's
// own connect-time labels (`reliant daemon start --label`) do not come here.
func (s *Service) SetMachineLabels(
	ctx context.Context,
	req *connect.Request[pb.SetMachineLabelsRequest],
) (*connect.Response[pb.SetMachineLabelsResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}
	if err := requirePoolWriter(ctx, "label a machine"); err != nil {
		return nil, err
	}
	msg := req.Msg
	if msg.GetDaemonId() == "" {
		return nil, svcerr.InvalidArgument("daemon_id is required")
	}
	if err := machinepool.ValidateLabels(msg.GetLabels(), "labels"); err != nil {
		return nil, err
	}
	found, err := s.deps.Repo.SetDaemonLabels(ctx, userID, msg.GetDaemonId(), msg.GetLabels())
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to set machine labels"))
	}
	if !found {
		return nil, svcerr.NotFound("machine")
	}
	s.applyMemberIdleTimeouts(ctx, userID, []string{msg.GetDaemonId()})
	d, err := s.deps.Repo.GetDaemon(ctx, msg.GetDaemonId())
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read the machine"))
	}
	return connect.NewResponse(&pb.SetMachineLabelsResponse{Daemon: daemonToProto(d, nil)}), nil
}
