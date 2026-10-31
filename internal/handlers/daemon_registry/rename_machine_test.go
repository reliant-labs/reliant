package daemon_registry_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	daemon_registry "github.com/reliant-labs/reliant/internal/handlers/daemon_registry"
)

// TestRenameMachine: a rename goes to the control plane first (it owns the
// name), by id, as the caller's session, and then onto the registry row the
// list, pickers, triggers and list_machines read. The control plane's refusal
// reaches the caller in its own words; a refused rename writes nothing here.
func TestRenameMachine(t *testing.T) {
	t.Parallel()
	auth.SetUserJWT("test-user", "session-jwt")
	managed := "managed"

	var mu sync.Mutex
	var cpCalls []string
	var upserts []db.DaemonIdentity
	names := &daemon_registry.MockMachineNames{
		RenameDaemonFunc: func(_ context.Context, jwt, daemonID, name string) error {
			mu.Lock()
			defer mu.Unlock()
			if jwt != "session-jwt" {
				return connect.NewError(connect.CodeUnauthenticated, errors.New("missing owner in context"))
			}
			if name == "cicd" {
				return connect.NewError(connect.CodeAlreadyExists, errors.New(`a machine named "cicd" already exists; choose another name`))
			}
			cpCalls = append(cpCalls, daemonID+"="+name)
			return nil
		},
	}
	repo := &daemon_registry.MockRepository{
		GetDaemonFunc: func(_ context.Context, id string) (*db.Daemon, error) {
			if id != "d-1" {
				return &db.Daemon{ID: id, UserID: "someone-else", Name: "theirs"}, nil
			}
			return &db.Daemon{ID: "d-1", UserID: "test-user", Name: "cloud-2xl", DaemonType: &managed}, nil
		},
		UpsertDaemonIdentityFunc: func(_ context.Context, id db.DaemonIdentity) (bool, error) {
			mu.Lock()
			defer mu.Unlock()
			upserts = append(upserts, id)
			return true, nil
		},
	}
	svc := daemon_registry.NewTestDaemonRegistry(t, daemon_registry.WithDaemonRegistryDeps(daemon_registry.Deps{Repo: repo, Names: names}))
	noCP := daemon_registry.NewTestDaemonRegistry(t, daemon_registry.WithDaemonRegistryDeps(daemon_registry.Deps{Repo: repo}))

	req := func(id, name string) *connect.Request[pb.RenameMachineRequest] {
		return connect.NewRequest(&pb.RenameMachineRequest{DaemonId: id, Name: name})
	}
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.RenameMachineRequest, pb.RenameMachineResponse]{
		{Name: "unauthenticated", Req: req("d-1", "big-box"), Ctx: context.Background(), WantErr: connect.CodeUnauthenticated},
		{Name: "no daemon id", Req: req("", "big-box"), Ctx: daemon_registry.AuthedContext(t), WantErr: connect.CodeInvalidArgument},
		{Name: "uppercase refused", Req: req("d-1", "Big-Box"), Ctx: daemon_registry.AuthedContext(t), WantErr: connect.CodeInvalidArgument},
		{Name: "empty refused", Req: req("d-1", "  "), Ctx: daemon_registry.AuthedContext(t), WantErr: connect.CodeInvalidArgument},
		{Name: "not the caller's machine", Req: req("d-2", "big-box"), Ctx: daemon_registry.AuthedContext(t), WantErr: connect.CodeNotFound},
		{Name: "a name another machine has", Req: req("d-1", "cicd"), Ctx: daemon_registry.AuthedContext(t), WantErr: connect.CodeAlreadyExists},
		{
			Name: "renamed at the control plane, then on the registry row",
			Req:  req("d-1", " big-box "),
			Ctx:  daemon_registry.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.RenameMachineResponse]) {
				if got := resp.Msg.GetDaemon(); got.GetName() != "big-box" || got.GetDaemonId() != "d-1" {
					t.Fatalf("response daemon = %s/%q, want d-1/big-box", got.GetDaemonId(), got.GetName())
				}
			},
		},
	}, svc.RenameMachine)

	if _, err := noCP.RenameMachine(daemon_registry.AuthedContext(t), req("d-1", "big-box")); !errors.Is(err, svcerr.ErrUnimplemented) {
		t.Fatalf("without a control plane: %v, want Unimplemented", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(cpCalls) != 1 || cpCalls[0] != "d-1=big-box" {
		t.Fatalf("control plane renames = %v, want exactly [d-1=big-box]", cpCalls)
	}
	if len(upserts) != 1 {
		t.Fatalf("registry writes = %+v, want exactly one (refused renames write nothing)", upserts)
	}
	if u := upserts[0]; u.DaemonID != "d-1" || u.UserID != "test-user" || u.Name != "big-box" || u.DaemonType != "managed" {
		t.Fatalf("registry write = %+v, want d-1 / test-user / big-box / managed", u)
	}
}
