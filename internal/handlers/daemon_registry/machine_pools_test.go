package daemon_registry_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	daemon_registry "github.com/reliant-labs/reliant/internal/handlers/daemon_registry"
)

func TestSetMachineLabels(t *testing.T) {
	t.Parallel()
	var stored map[string]string
	svc := daemon_registry.NewTestDaemonRegistry(t, daemon_registry.WithDaemonRegistryDeps(daemon_registry.Deps{
		Repo: &daemon_registry.MockRepository{
			SetDaemonLabelsFunc: func(_ context.Context, userID, id string, labels map[string]string) (bool, error) {
				if userID != "test-user" || id != "d-1" {
					return false, nil
				}
				stored = labels
				return true, nil
			},
			GetDaemonFunc: func(context.Context, string) (*db.Daemon, error) {
				return &db.Daemon{ID: "d-1", UserID: "test-user", Labels: stored}, nil
			},
		},
	}))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.SetMachineLabelsRequest, pb.SetMachineLabelsResponse]{
		{Name: "unauthenticated", Req: connect.NewRequest(&pb.SetMachineLabelsRequest{DaemonId: "d-1"}), Ctx: context.Background(), WantErr: connect.CodeUnauthenticated},
		{Name: "no daemon id", Req: connect.NewRequest(&pb.SetMachineLabelsRequest{}), Ctx: daemon_registry.AuthedContext(t), WantErr: connect.CodeInvalidArgument},
		{Name: "a label that is not a slug", Req: connect.NewRequest(&pb.SetMachineLabelsRequest{DaemonId: "d-1", Labels: map[string]string{"Pool": "CI CD"}}), Ctx: daemon_registry.AuthedContext(t), WantErr: connect.CodeInvalidArgument},
		{Name: "not the caller's machine", Req: connect.NewRequest(&pb.SetMachineLabelsRequest{DaemonId: "other", Labels: map[string]string{"pool": "cicd"}}), Ctx: daemon_registry.AuthedContext(t), WantErr: connect.CodeNotFound},
		{
			Name: "labels are stored and returned",
			Req:  connect.NewRequest(&pb.SetMachineLabelsRequest{DaemonId: "d-1", Labels: map[string]string{"pool": "cicd"}}),
			Ctx:  daemon_registry.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.SetMachineLabelsResponse]) {
				if got := resp.Msg.GetDaemon().GetLabels()["pool"]; got != "cicd" {
					t.Fatalf("labels = %v", resp.Msg.GetDaemon().GetLabels())
				}
			},
		},
	}, svc.SetMachineLabels)
}

func TestPutAndListMachinePools(t *testing.T) {
	t.Parallel()
	var put *core.MachinePool
	repo := &daemon_registry.MockRepository{
		PutMachinePoolFunc: func(_ context.Context, p *core.MachinePool) (*core.MachinePool, error) {
			copied := *p
			copied.ID = "pool-1"
			put = &copied
			return put, nil
		},
		ListMachinePoolsForUserFunc: func(context.Context, string) ([]*core.MachinePool, error) {
			return []*core.MachinePool{put}, nil
		},
		ListMachinePoolMembersFunc: func(context.Context, *core.MachinePool, string) ([]core.MachinePoolMemberRow, error) {
			return []core.MachinePoolMemberRow{{DaemonID: "d-1", Name: "cicd", Phase: "connected", LiveRuns: 2}}, nil
		},
	}
	svc := daemon_registry.NewTestDaemonRegistry(t, daemon_registry.WithDaemonRegistryDeps(daemon_registry.Deps{Repo: repo}))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.PutMachinePoolRequest, pb.PutMachinePoolResponse]{
		{Name: "no pool", Req: connect.NewRequest(&pb.PutMachinePoolRequest{}), Ctx: daemon_registry.AuthedContext(t), WantErr: connect.CodeInvalidArgument},
		{Name: "bad name", Req: connect.NewRequest(&pb.PutMachinePoolRequest{Pool: &pb.MachinePoolSpec{Name: "CI CD"}}), Ctx: daemon_registry.AuthedContext(t), WantErr: connect.CodeInvalidArgument},
		{Name: "min above max", Req: connect.NewRequest(&pb.PutMachinePoolRequest{Pool: &pb.MachinePoolSpec{Name: "cicd", MinMembers: 3, MaxMembers: 2}}), Ctx: daemon_registry.AuthedContext(t), WantErr: connect.CodeInvalidArgument},
		{
			Name: "a pool with no selector selects pool=<name>, and shows its members",
			Req:  connect.NewRequest(&pb.PutMachinePoolRequest{Pool: &pb.MachinePoolSpec{Name: "cicd", MinMembers: 2, MaxRunsPerMember: 6}}),
			Ctx:  daemon_registry.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.PutMachinePoolResponse]) {
				p := resp.Msg.GetPool()
				if p.GetSpec().GetSelector()["pool"] != "cicd" || put.UserID != "test-user" || put.MaxRunsPerMember != 6 {
					t.Fatalf("stored %+v, returned %+v", put, p.GetSpec())
				}
				if len(p.GetMembers()) != 1 || p.GetMembers()[0].GetLiveRuns() != 2 {
					t.Fatalf("members = %+v", p.GetMembers())
				}
				if len(p.GetWarnings()) != 1 {
					t.Fatalf("warnings = %v, want the unmet min_members", p.GetWarnings())
				}
			},
		},
	}, svc.PutMachinePool)
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListMachinePoolsRequest, pb.ListMachinePoolsResponse]{
		{Name: "unauthenticated", Req: connect.NewRequest(&pb.ListMachinePoolsRequest{}), Ctx: context.Background(), WantErr: connect.CodeUnauthenticated},
		{
			Name: "lists the caller's pools",
			Req:  connect.NewRequest(&pb.ListMachinePoolsRequest{}),
			Ctx:  daemon_registry.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ListMachinePoolsResponse]) {
				if len(resp.Msg.GetPools()) != 1 || resp.Msg.GetPools()[0].GetId() != "pool-1" {
					t.Fatalf("pools = %+v", resp.Msg.GetPools())
				}
			},
		},
	}, svc.ListMachinePools)
}

func TestDeleteMachinePool(t *testing.T) {
	t.Parallel()
	svc := daemon_registry.NewTestDaemonRegistry(t, daemon_registry.WithDaemonRegistryDeps(daemon_registry.Deps{
		Repo: &daemon_registry.MockRepository{
			DeleteMachinePoolByNameFunc: func(_ context.Context, _, name string) error {
				switch name {
				case "used":
					return core.ErrMachinePoolInUse
				case "gone":
					return core.ErrMachinePoolNotFound
				}
				return nil
			},
		},
	}))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.DeleteMachinePoolRequest, pb.DeleteMachinePoolResponse]{
		{Name: "no name", Req: connect.NewRequest(&pb.DeleteMachinePoolRequest{}), Ctx: daemon_registry.AuthedContext(t), WantErr: connect.CodeInvalidArgument},
		{Name: "a trigger runs on it", Req: connect.NewRequest(&pb.DeleteMachinePoolRequest{Name: "used"}), Ctx: daemon_registry.AuthedContext(t), WantErr: connect.CodeFailedPrecondition},
		{Name: "missing", Req: connect.NewRequest(&pb.DeleteMachinePoolRequest{Name: "gone"}), Ctx: daemon_registry.AuthedContext(t), WantErr: connect.CodeNotFound},
		{Name: "deleted", Req: connect.NewRequest(&pb.DeleteMachinePoolRequest{Name: "cicd"}), Ctx: daemon_registry.AuthedContext(t), Check: func(*testing.T, *connect.Response[pb.DeleteMachinePoolResponse]) {}},
	}, svc.DeleteMachinePool)
}
