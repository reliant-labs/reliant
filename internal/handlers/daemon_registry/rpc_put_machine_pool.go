package daemon_registry

import (
	"context"
	"fmt"
	"time"

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
	spec := req.Msg.GetPool()
	if spec == nil {
		return nil, svcerr.InvalidArgument("pool is required")
	}
	if !labelSlug.MatchString(spec.GetName()) {
		return nil, svcerr.InvalidArgument("pool name: a lowercase slug (a-z, 0-9, '.', '_', '-'), at most 63 characters")
	}
	selector := spec.GetSelector()
	if len(selector) == 0 {
		selector = machinepool.DefaultSelector(spec.GetName())
	}
	if err := validateLabels(selector, "selector"); err != nil {
		return nil, err
	}
	if spec.GetMaxMembers() > 0 && spec.GetMinMembers() > spec.GetMaxMembers() {
		return nil, svcerr.InvalidArgument(fmt.Sprintf("min_members (%d) exceeds max_members (%d)", spec.GetMinMembers(), spec.GetMaxMembers()))
	}
	idleTimeout := spec.GetMemberIdleTimeout()
	if idleTimeout == "" {
		idleTimeout = core.DefaultMemberIdleTimeout
	}
	if d, err := time.ParseDuration(idleTimeout); err != nil || d < time.Minute || d > 4*time.Hour {
		return nil, svcerr.InvalidArgument("member_idle_timeout: a duration between 1m and 4h, e.g. \"10m\"")
	}
	stored, err := s.deps.Repo.PutMachinePool(ctx, &core.MachinePool{
		MemberIdleTimeout: idleTimeout,
		UserID:            userID,
		Name:              spec.GetName(),
		Selector:          selector,
		MinMembers:        int(spec.GetMinMembers()),
		MaxMembers:        int(spec.GetMaxMembers()),
		MemberSize:        spec.GetMemberSize(),
		MaxRunsPerMember:  int(spec.GetMaxRunsPerMember()),
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to write the machine pool"))
	}
	out, err := s.poolToProto(ctx, stored)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.PutMachinePoolResponse{Pool: out}), nil
}
