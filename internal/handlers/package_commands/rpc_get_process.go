package package_commands

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemoncmd"
)

// GetProcess returns one package-command process from its machine.
func (s *Service) GetProcess(
	ctx context.Context,
	req *connect.Request[reliantv1.GetPackageProcessRequest],
) (*connect.Response[reliantv1.GetPackageProcessResponse], error) {
	if req.Msg.ProcessId == "" {
		return nil, svcerr.InvalidArgument("process_id is required")
	}
	userID, err := daemoncmd.UserID(ctx)
	if err != nil {
		return nil, err
	}
	daemonID, err := s.requestedMachine(ctx, userID, req.Msg.DaemonId)
	if err != nil {
		return nil, err
	}
	listed, err := s.listOn(ctx, userID, daemonID)
	if err != nil {
		return nil, err
	}
	for _, p := range listed {
		if p.Id == req.Msg.ProcessId {
			return connect.NewResponse(&reliantv1.GetPackageProcessResponse{Process: p}), nil
		}
	}
	return nil, svcerr.NotFound("process")
}
