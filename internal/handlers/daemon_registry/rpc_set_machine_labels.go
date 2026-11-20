package daemon_registry

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/machinepool"
)

// SetMachineLabels replaces one of the caller's machines' durable labels.
func (s *Service) SetMachineLabels(
	ctx context.Context,
	req *pb.SetMachineLabelsRequest,
) (*pb.SetMachineLabelsResponse, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}
	msg := req
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
	return &pb.SetMachineLabelsResponse{Daemon: daemonToProto(d, nil)}, nil
}
