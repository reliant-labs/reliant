package daemon_registry

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// ListMachinePools returns the caller's pools with their members as they are
// now.
func (s *Service) ListMachinePools(
	ctx context.Context,
	req *connect.Request[pb.ListMachinePoolsRequest],
) (*connect.Response[pb.ListMachinePoolsResponse], error) {
	_ = req
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}
	pools, err := s.deps.Repo.ListMachinePoolsForUser(ctx, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list machine pools"))
	}
	resp := &pb.ListMachinePoolsResponse{Pools: make([]*pb.MachinePool, 0, len(pools))}
	for _, p := range pools {
		out, err := s.poolToProto(ctx, p)
		if err != nil {
			return nil, err
		}
		resp.Pools = append(resp.Pools, out)
	}
	return connect.NewResponse(resp), nil
}

// poolToProto renders a pool with its members. Members are read with no
// project: state and live runs are project-independent; the per-project
// catalog check is placement's, at launch.
func (s *Service) poolToProto(ctx context.Context, p *core.MachinePool) (*pb.MachinePool, error) {
	rows, err := s.deps.Repo.ListMachinePoolMembers(ctx, p, "")
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list the pool's members"))
	}
	out := &pb.MachinePool{
		Id: p.ID,
		Spec: &pb.MachinePoolSpec{
			Name: p.Name, Selector: p.Selector,
			MinMembers: uint32(p.MinMembers), MaxMembers: uint32(p.MaxMembers),
			MemberSize: p.MemberSize, MaxRunsPerMember: uint32(p.MaxRunsPerMember),
			MemberIdleTimeout: p.MemberIdleTimeout,
		},
		CreatedAt: timestamppb.New(p.CreatedAt),
		UpdatedAt: timestamppb.New(p.UpdatedAt),
	}
	usable := 0
	for _, r := range rows {
		out.Members = append(out.Members, &pb.MachinePoolMember{
			DaemonId: r.DaemonID, Name: r.Name, State: r.Phase, LiveRuns: uint32(r.LiveRuns), Size: r.Size,
		})
		if r.Phase != "failed" {
			usable++
		}
	}
	if len(rows) == 0 {
		out.Warnings = append(out.Warnings, "no machine has labels matching the selector, so runs on this pool wait")
	}
	if p.MinMembers > 0 && usable < p.MinMembers {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d usable member(s); min_members is %d", usable, p.MinMembers))
	}
	return out, nil
}
