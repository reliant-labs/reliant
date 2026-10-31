package daemon_registry_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
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

// recordingIdleTimeouts records the idle timeouts set at the control plane.
type recordingIdleTimeouts struct{ set map[string]string }

func (r *recordingIdleTimeouts) SetDaemonIdleTimeout(_ context.Context, _, daemonID, idleTimeout string) error {
	r.set[daemonID] = idleTimeout
	return nil
}

// A pool's members sleep on its member_idle_timeout: writing the pool sets
// every managed member's, and labelling a machine into a pool sets that
// machine's. A machine in two pools takes the shorter; a machine in none, or
// a self-hosted one (it never sleeps), is left alone.
func TestPoolMembersTakeTheMemberIdleTimeout(t *testing.T) {
	auth.SetUserJWT("test-user", "jwt-test-user")
	managed, selfHosted := "managed", "self_hosted"
	daemons := []*db.Daemon{
		{ID: "d-ci", DaemonType: &managed, Labels: map[string]string{"pool": "cicd"}},
		{ID: "d-both", DaemonType: &managed, Labels: map[string]string{"pool": "cicd", "gpu": "yes"}},
		{ID: "d-none", DaemonType: &managed, Labels: map[string]string{"pool": "other"}},
		{ID: "d-local", DaemonType: &selfHosted, Labels: map[string]string{"pool": "cicd"}},
	}
	pools := []*core.MachinePool{
		{ID: "p-ci", Name: "cicd", Selector: map[string]string{"pool": "cicd"}, MemberIdleTimeout: "10m"},
		{ID: "p-gpu", Name: "gpu", Selector: map[string]string{"gpu": "yes"}, MemberIdleTimeout: "5m"},
	}
	repo := &daemon_registry.MockRepository{
		PutMachinePoolFunc: func(_ context.Context, p *core.MachinePool) (*core.MachinePool, error) { return p, nil },
		ListMachinePoolsForUserFunc: func(context.Context, string) ([]*core.MachinePool, error) {
			return pools, nil
		},
		ListMachinePoolMembersFunc: func(context.Context, *core.MachinePool, string) ([]core.MachinePoolMemberRow, error) {
			return nil, nil
		},
		ListDaemonsByUserIDFunc: func(context.Context, string) ([]*db.Daemon, error) { return daemons, nil },
		SetDaemonLabelsFunc:     func(context.Context, string, string, map[string]string) (bool, error) { return true, nil },
		GetDaemonFunc:           func(_ context.Context, id string) (*db.Daemon, error) { return &db.Daemon{ID: id}, nil },
	}
	set := &recordingIdleTimeouts{set: map[string]string{}}
	svc := daemon_registry.NewTestDaemonRegistry(t, daemon_registry.WithDaemonRegistryDeps(daemon_registry.Deps{Repo: repo, IdleTimeouts: set}))

	if _, err := svc.PutMachinePool(daemon_registry.AuthedContext(t), connect.NewRequest(&pb.PutMachinePoolRequest{
		Pool: &pb.MachinePoolSpec{Name: "cicd", MemberIdleTimeout: "10m"},
	})); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"d-ci": "10m", "d-both": "5m"}
	if len(set.set) != len(want) || set.set["d-ci"] != "10m" || set.set["d-both"] != "5m" {
		t.Fatalf("after PutMachinePool: set %v, want %v", set.set, want)
	}

	set.set = map[string]string{}
	if _, err := svc.SetMachineLabels(daemon_registry.AuthedContext(t), connect.NewRequest(&pb.SetMachineLabelsRequest{
		DaemonId: "d-ci", Labels: map[string]string{"pool": "cicd"},
	})); err != nil {
		t.Fatal(err)
	}
	if len(set.set) != 1 || set.set["d-ci"] != "10m" {
		t.Fatalf("after SetMachineLabels: set %v, want only d-ci at 10m", set.set)
	}
}
