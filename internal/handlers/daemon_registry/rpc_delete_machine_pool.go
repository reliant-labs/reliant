package daemon_registry

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// DeleteMachinePool deletes one of the caller's pools; its machines and their
// labels are untouched. Refused while a trigger runs on it.
func (s *Service) DeleteMachinePool(
	ctx context.Context,
	req *connect.Request[pb.DeleteMachinePoolRequest],
) (*connect.Response[pb.DeleteMachinePoolResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}
	if req.Msg.GetName() == "" {
		return nil, svcerr.InvalidArgument("name is required")
	}
	err := s.deps.Repo.DeleteMachinePoolByName(ctx, userID, req.Msg.GetName())
	switch {
	case errors.Is(err, core.ErrMachinePoolNotFound):
		return nil, svcerr.NotFound("machine pool")
	case errors.Is(err, core.ErrMachinePoolInUse):
		return nil, svcerr.FailedPrecondition("a trigger runs on this pool: move the trigger to a machine or another pool first")
	case err != nil:
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to delete the machine pool"))
	}
	return connect.NewResponse(&pb.DeleteMachinePoolResponse{}), nil
}
