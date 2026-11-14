// yours: scaffolded once, never touched again — forge will not overwrite this file.
package project_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

// moveOrgs: test-user administers acme and is a member of globex; initech
// does not have them. acme is their default.
type moveOrgs struct{ down bool }

func (m moveOrgs) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	if m.down {
		return false, true, errors.New("control-plane is down")
	}
	return userID == "test-user" && (orgID == "acme" || orgID == "globex"), true, nil
}

func (m moveOrgs) RoleFresh(_ context.Context, userID, orgID string) (string, bool, error) {
	if m.down {
		return "", false, errors.New("control-plane is down")
	}
	if userID != "test-user" {
		return "", true, nil
	}
	return map[string]string{"acme": "admin", "globex": "member"}[orgID], true, nil
}

func (m moveOrgs) DefaultOrg(context.Context, string) (string, error) { return "acme", nil }

// TestPreviewProjectOrgMove_Generated holds who may preview a move and what
// the preview says: the authority rules of a move (authz.ProjectOrgMove) as
// connect codes and reasons, and the blockers and effects passed through.
func TestPreviewProjectOrgMove_Generated(t *testing.T) {
	t.Parallel()

	acme := "acme"
	projects := map[string]*db.Project{
		"p-mine":   {ID: "p-mine", UserID: "test-user", OrgID: &acme},
		"p-theirs": {ID: "p-theirs", UserID: "someone-else", OrgID: &acme},
		"p-legacy": {ID: "p-legacy", UserID: "test-user"},
	}
	store := &project.MockRepository{
		GetProjectFunc: func(_ context.Context, id string) (*db.Project, error) {
			if p, ok := projects[id]; ok {
				return p, nil
			}
			return nil, svcerr.ErrNotFound
		},
		ProjectOrgMoveBlockersFunc: func(context.Context, string) (*core.ProjectOrgMoveBlockers, error) {
			return &core.ProjectOrgMoveBlockers{
				Chats:      []core.ProjectOrgMoveBlockedChat{{ChatID: "c-1", Title: "release v2", Activity: int32(pb.ChatActivity_CHAT_ACTIVITY_AWAITING_INPUT)}},
				ChatsTotal: 1, LiveRuns: 2,
			}, nil
		},
		ProjectOrgMoveEffectsFunc: func(_ context.Context, _, to string) (*core.ProjectOrgMoveEffects, error) {
			return &core.ProjectOrgMoveEffects{
				Chats: 3, Runs: 7, Triggers: 2, TriggersToPause: 1, SharesToClear: 1, OrgBindingsToClear: 1,
				TriggersNeedingSetup: []core.ProjectOrgMoveTriggerSetup{{TriggerID: "t-1", Name: "triage", Reason: core.ProjectOrgMoveSetupOrgConnection}},
				Machines:             []core.ProjectOrgMoveMachine{{DaemonID: "d-1", Name: "Dev box", Attached: true, PinnedChats: 2}},
				ForgeProjectName:     "shop",
			}, nil
		},
	}
	svc := project.NewTestProject(t, project.WithProjectDeps(project.Deps{Repo: store}))
	person := func(m authz.Membership) context.Context {
		return authz.WithCredentialKind(authz.WithMembership(project.AuthedContext(t), m), authz.CredentialKindPerson)
	}
	req := func(projectID, to string) *connect.Request[pb.PreviewProjectOrgMoveRequest] {
		return connect.NewRequest(&pb.PreviewProjectOrgMoveRequest{ProjectId: projectID, ToOrgId: to})
	}

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.PreviewProjectOrgMoveRequest, pb.PreviewProjectOrgMoveResponse]{
		{
			Name: "the owner, an admin of the source and a member of the target",
			Req:  req("p-mine", "globex"),
			Ctx:  person(moveOrgs{}),
			Check: func(t *testing.T, resp *connect.Response[pb.PreviewProjectOrgMoveResponse]) {
				m := resp.Msg
				if m.GetFromOrgId() != "acme" || m.GetToOrgId() != "globex" {
					t.Fatalf("orgs = %q → %q", m.GetFromOrgId(), m.GetToOrgId())
				}
				b := m.GetBlockers()
				if b.GetChatsTotal() != 1 || b.GetLiveRuns() != 2 || len(b.GetChats()) != 1 ||
					b.GetChats()[0].GetTitle() != "release v2" || b.GetChats()[0].GetActivity() != pb.ChatActivity_CHAT_ACTIVITY_AWAITING_INPUT {
					t.Fatalf("blockers = %v", b)
				}
				e := m.GetEffects()
				if e.GetChats() != 3 || e.GetRuns() != 7 || e.GetTriggers() != 2 || e.GetTriggersToPause() != 1 ||
					e.GetSharesToClear() != 1 || e.GetOrgBindingsToClear() != 1 || e.GetForgeProjectName() != "shop" {
					t.Fatalf("effects = %v", e)
				}
				if len(e.GetTriggersNeedingSetup()) != 1 || e.GetTriggersNeedingSetup()[0].GetReason() != core.ProjectOrgMoveSetupOrgConnection {
					t.Fatalf("triggers needing setup = %v", e.GetTriggersNeedingSetup())
				}
				if len(e.GetMachines()) != 1 || !e.GetMachines()[0].GetAttached() || e.GetMachines()[0].GetPinnedChats() != 2 || e.GetMachines()[0].InTargetOrg != nil {
					t.Fatalf("machines = %v", e.GetMachines())
				}
				if e.GetControlPlaneEffectsKnown() || e.SecretAccessToRevoke != nil || e.TargetFunded != nil {
					t.Fatalf("control-plane's effects must read unknown until control-plane answers them: %v", e)
				}
			},
		},
		{
			Name: "a project not yet assigned an org moves from the owner's default",
			Req:  req("p-legacy", "globex"),
			Ctx:  person(moveOrgs{}),
			Check: func(t *testing.T, resp *connect.Response[pb.PreviewProjectOrgMoveResponse]) {
				if resp.Msg.GetFromOrgId() != "acme" {
					t.Fatalf("from = %q, want the default org acme", resp.Msg.GetFromOrgId())
				}
			},
		},
		{
			Name:    "another member's project, even to an admin of its org",
			Req:     req("p-theirs", "globex"),
			Ctx:     person(moveOrgs{}),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "a project that does not exist",
			Req:     req("p-missing", "globex"),
			Ctx:     person(moveOrgs{}),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "a machine or CLI credential",
			Req:     req("p-mine", "globex"),
			Ctx:     authz.WithCredentialKind(authz.WithMembership(project.AuthedContext(t), moveOrgs{}), authz.CredentialKindSoftware),
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name:    "a target the owner is not a member of",
			Req:     req("p-mine", "initech"),
			Ctx:     person(moveOrgs{}),
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name:    "the org the project is already in",
			Req:     req("p-mine", "acme"),
			Ctx:     person(moveOrgs{}),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "control-plane is down",
			Req:     req("p-mine", "globex"),
			Ctx:     person(moveOrgs{down: true}),
			WantErr: connect.CodeUnavailable,
		},
	}, svc.PreviewProjectOrgMove)
}

// Each refusal names its reason, so the web can say what to do about it: ask
// an admin, pick an org the owner is in, sign in as a person.
func TestPreviewProjectOrgMove_RefusalReasons(t *testing.T) {
	t.Parallel()
	acme, globex := "acme", "globex"
	projects := map[string]*db.Project{
		"p-acme":   {ID: "p-acme", UserID: "test-user", OrgID: &acme},
		"p-globex": {ID: "p-globex", UserID: "test-user", OrgID: &globex},
	}
	store := &project.MockRepository{
		GetProjectFunc: func(_ context.Context, id string) (*db.Project, error) { return projects[id], nil },
	}
	svc := project.NewTestProject(t, project.WithProjectDeps(project.Deps{Repo: store}))
	person := authz.WithCredentialKind(authz.WithMembership(project.AuthedContext(t), moveOrgs{}), authz.CredentialKindPerson)
	software := authz.WithCredentialKind(authz.WithMembership(project.AuthedContext(t), moveOrgs{}), authz.CredentialKindSoftware)
	for _, tc := range []struct {
		name, projectID, to string
		ctx                 context.Context
		code                connect.Code
		reason              string
	}{
		// A member-only owner: the org's admin role is its consent.
		{"a plain member of the source", "p-globex", "acme", person, connect.CodePermissionDenied, project.ReasonSourceAdminRequired},
		{"a target the owner is not in", "p-acme", "initech", person, connect.CodePermissionDenied, project.ReasonNotOrgMember},
		{"the org it is already in", "p-acme", "acme", person, connect.CodeInvalidArgument, project.ReasonSameOrg},
		{"an agent's credential", "p-acme", "globex", software, connect.CodePermissionDenied, auth.ReasonPersonRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.PreviewProjectOrgMove(tc.ctx, connect.NewRequest(&pb.PreviewProjectOrgMoveRequest{ProjectId: tc.projectID, ToOrgId: tc.to}))
			ce := svcerr.ToConnect(err)
			if ce == nil || ce.Code() != tc.code || ce.Meta().Get(svcerr.ReasonHeader) != tc.reason {
				t.Fatalf("got %v (reason %q), want %v %q", err, ce.Meta().Get(svcerr.ReasonHeader), tc.code, tc.reason)
			}
		})
	}
}
