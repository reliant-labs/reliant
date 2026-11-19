package daemon_registry_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	daemon_registry "github.com/reliant-labs/reliant/internal/handlers/daemon_registry"
)

// personContext is the test user signed in to the app: what the API edge
// installs for a web or desktop session.
func personContext(t *testing.T) context.Context {
	return auth.WithCredential(daemon_registry.AuthedContext(t), auth.CredentialSession)
}

// asCredential is the test user through credential c, with access-token
// principal p when c is one (nil otherwise). It starts from the bare user
// id, not AuthedContext, which is already an app session.
func asCredential(c auth.Credential, p *fat.Principal) func(*testing.T) context.Context {
	return func(*testing.T) context.Context {
		ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
		if c != auth.CredentialUnknown {
			ctx = auth.WithCredential(ctx, c)
		}
		if p != nil {
			ctx = auth.WithMachineToken(ctx, p)
		}
		return ctx
	}
}

// credentialCases is every kind of caller the three pool writes can see, and
// whether it may write. Owning the machine or pool is not enough: only a
// person, or an agent's call a person approved on its card, writes.
var credentialCases = []struct {
	name string
	ctx  func(*testing.T) context.Context
	ok   bool
}{
	{"app session", personContext, true},
	{"self-host shared key", asCredential(auth.CredentialSharedKey, nil), true},
	{"agent call with an approved card", func(t *testing.T) context.Context {
		// What the worker's machineController passes: the user on the ctx,
		// no request credential, and the card the tool verified.
		return auth.WithApprovedCard(context.WithValue(context.Background(), auth.UserIDContextKey, "test-user"), "call-1")
	}, true},
	{"cli login or api token", asCredential(auth.CredentialAccessToken, &fat.Principal{Scopes: fat.SetOf(fat.ScopeReliantAPI)}), false},
	{"the machine's own daemon credential", asCredential(auth.CredentialAccessToken, &fat.Principal{
		Scopes: fat.SetOf(fat.ScopeDaemonConnect), AllScopes: true, Resource: &fat.Resource{Kind: fat.ResourceDaemon, ID: "d-1"},
	}), false},
	{"oauth client", asCredential(auth.CredentialOAuthClient, nil), false},
	{"agent or run with no approved card (unclassified)", asCredential(auth.CredentialUnknown, nil), false},
	{"cli login claiming an empty card", func(t *testing.T) context.Context {
		return auth.WithApprovedCard(asCredential(auth.CredentialAccessToken, &fat.Principal{Scopes: fat.SetOf(fat.ScopeReliantAPI)})(t), "")
	}, false},
}

// requirePersonRefusal fails unless err is the person_required refusal.
func requirePersonRefusal(t *testing.T, err error) {
	t.Helper()
	if svcerr.Code(err) != connect.CodePermissionDenied {
		t.Fatalf("err = %v, want PermissionDenied", err)
	}
	if got := svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader); got != auth.ReasonPersonRequired {
		t.Fatalf("reason = %q, want %q", got, auth.ReasonPersonRequired)
	}
}

// poolWriterRepo records every write that reached the store; the test user
// owns machine d-1 and pool cicd, and nothing else.
func poolWriterRepo(writes *[]string) *daemon_registry.MockRepository {
	return &daemon_registry.MockRepository{
		SetDaemonLabelsFunc: func(_ context.Context, userID, id string, _ map[string]string) (bool, error) {
			if userID != "test-user" || id != "d-1" {
				return false, nil
			}
			*writes = append(*writes, "label "+id)
			return true, nil
		},
		GetDaemonFunc: func(_ context.Context, id string) (*db.Daemon, error) {
			return &db.Daemon{ID: id, UserID: "test-user"}, nil
		},
		PutMachinePoolFunc: func(_ context.Context, p *core.MachinePool) (*core.MachinePool, error) {
			*writes = append(*writes, "put "+p.Name+" for "+p.UserID)
			return p, nil
		},
		DeleteMachinePoolByNameFunc: func(_ context.Context, userID, name string) error {
			if userID != "test-user" || name != "cicd" {
				return core.ErrMachinePoolNotFound
			}
			*writes = append(*writes, "delete "+name)
			return nil
		},
		ListMachinePoolsForUserFunc: func(context.Context, string) ([]*core.MachinePool, error) { return nil, nil },
		ListMachinePoolMembersFunc: func(context.Context, *core.MachinePool, string) ([]core.MachinePoolMemberRow, error) {
			return nil, nil
		},
		ListDaemonsByUserIDFunc: func(context.Context, string) ([]*db.Daemon, error) { return nil, nil },
	}
}

func TestMachinePoolWritesNeedAPerson(t *testing.T) {
	t.Parallel()
	for _, tc := range credentialCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var writes []string
			svc := daemon_registry.NewTestDaemonRegistry(t, daemon_registry.WithDaemonRegistryDeps(daemon_registry.Deps{Repo: poolWriterRepo(&writes)}))
			ctx := tc.ctx(t)

			_, labelErr := svc.SetMachineLabels(ctx, connect.NewRequest(&pb.SetMachineLabelsRequest{DaemonId: "d-1", Labels: map[string]string{"pool": "cicd"}}))
			_, putErr := svc.PutMachinePool(ctx, connect.NewRequest(&pb.PutMachinePoolRequest{Pool: &pb.MachinePoolSpec{Name: "cicd"}}))
			_, deleteErr := svc.DeleteMachinePool(ctx, connect.NewRequest(&pb.DeleteMachinePoolRequest{Name: "cicd"}))

			if !tc.ok {
				requirePersonRefusal(t, labelErr)
				requirePersonRefusal(t, putErr)
				requirePersonRefusal(t, deleteErr)
				if len(writes) != 0 {
					t.Fatalf("a refused caller reached the store: %v", writes)
				}
				return
			}
			for name, err := range map[string]error{"SetMachineLabels": labelErr, "PutMachinePool": putErr, "DeleteMachinePool": deleteErr} {
				if err != nil {
					t.Fatalf("%s = %v, want nil", name, err)
				}
			}
			want := []string{"label d-1", "put cicd for test-user", "delete cicd"}
			if len(writes) != len(want) || writes[0] != want[0] || writes[1] != want[1] || writes[2] != want[2] {
				t.Fatalf("writes = %v, want %v", writes, want)
			}
		})
	}
}

// A person, or an approved card, still acts only on their own machines and
// pools: another user's are not found.
func TestMachinePoolWritesStayOnTheCallersOwn(t *testing.T) {
	t.Parallel()
	approved := func(t *testing.T) context.Context {
		return auth.WithApprovedCard(context.WithValue(context.Background(), auth.UserIDContextKey, "test-user"), "call-1")
	}
	for name, ctx := range map[string]func(*testing.T) context.Context{"app session": personContext, "approved card": approved} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var writes []string
			svc := daemon_registry.NewTestDaemonRegistry(t, daemon_registry.WithDaemonRegistryDeps(daemon_registry.Deps{Repo: poolWriterRepo(&writes)}))
			_, err := svc.SetMachineLabels(ctx(t), connect.NewRequest(&pb.SetMachineLabelsRequest{DaemonId: "someone-elses", Labels: map[string]string{"pool": "cicd"}}))
			if svcerr.Code(err) != connect.CodeNotFound {
				t.Fatalf("labelling another user's machine = %v, want NotFound", err)
			}
			_, err = svc.DeleteMachinePool(ctx(t), connect.NewRequest(&pb.DeleteMachinePoolRequest{Name: "someone-elses"}))
			if svcerr.Code(err) != connect.CodeNotFound {
				t.Fatalf("deleting another user's pool = %v, want NotFound", err)
			}
			if len(writes) != 0 {
				t.Fatalf("writes = %v, want none", writes)
			}
		})
	}
}
