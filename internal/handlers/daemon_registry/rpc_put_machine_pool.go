package daemon_registry

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/machinepool"
)

// PutMachinePool creates the caller's pool of that name or replaces its
// definition. Members are not written: they are the caller's machines whose
// labels match the selector.
func (s *Service) PutMachinePool(
	ctx context.Context,
	req *connect.Request[pb.PutMachinePoolRequest],
) (*connect.Response[pb.PutMachinePoolResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}
	if err := requirePoolWriter(ctx, "define a machine pool"); err != nil {
		return nil, err
	}
	spec := req.Msg.GetPool()
	if spec == nil {
		return nil, svcerr.InvalidArgument("pool is required")
	}
	pool := &core.MachinePool{
		UserID:            userID,
		Name:              spec.GetName(),
		Selector:          spec.GetSelector(),
		MinMembers:        int(spec.GetMinMembers()),
		MaxMembers:        int(spec.GetMaxMembers()),
		MemberSize:        spec.GetMemberSize(),
		MaxRunsPerMember:  int(spec.GetMaxRunsPerMember()),
		MemberIdleTimeout: spec.GetMemberIdleTimeout(),
	}
	if err := machinepool.NormalizeSpec(pool); err != nil {
		return nil, err
	}
	stored, err := s.deps.Repo.PutMachinePool(ctx, pool)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to write the machine pool"))
	}
	s.applyMemberIdleTimeouts(ctx, userID, nil)
	out, err := s.poolToProto(ctx, stored)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.PutMachinePoolResponse{Pool: out}), nil
}
