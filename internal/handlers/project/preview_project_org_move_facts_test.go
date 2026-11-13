package project_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

// previewWithFacts previews p-mine (acme → globex, deploying as "shop" on
// d-1 and d-2) with control-plane's half answered by facts.
func previewWithFacts(t *testing.T, facts project.MoveFacts) (*pb.ProjectOrgMoveEffects, error) {
	t.Helper()
	acme := "acme"
	store := &project.MockRepository{
		GetProjectFunc: func(context.Context, string) (*db.Project, error) {
			return &db.Project{ID: "p-mine", UserID: "test-user", OrgID: &acme}, nil
		},
		ProjectOrgMoveBlockersFunc: func(context.Context, string) (*core.ProjectOrgMoveBlockers, error) {
			return &core.ProjectOrgMoveBlockers{}, nil
		},
		ProjectOrgMoveEffectsFunc: func(context.Context, string, string) (*core.ProjectOrgMoveEffects, error) {
			return &core.ProjectOrgMoveEffects{
				ForgeProjectName: "shop",
				Machines:         []core.ProjectOrgMoveMachine{{DaemonID: "d-1"}, {DaemonID: "d-2"}},
			}, nil
		},
	}
	svc := project.NewTestProject(t, project.WithProjectDeps(project.Deps{Repo: store, MoveFacts: facts}))
	ctx := authz.WithCredentialKind(authz.WithMembership(project.AuthedContext(t), moveOrgs{}), authz.CredentialKindPerson)
	resp, err := svc.PreviewProjectOrgMove(ctx, connect.NewRequest(&pb.PreviewProjectOrgMoveRequest{ProjectId: "p-mine", ToOrgId: "globex"}))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetEffects(), nil
}

// Control-plane's half fills the preview: secret access to revoke, the hosted
// project staying behind, the target's funding and each machine's org, asked
// as the owner, about the project's own deploy name and machines.
func TestPreviewProjectOrgMove_ControlPlaneFacts(t *testing.T) {
	t.Parallel()
	funded := false
	var asked controlplane.ProjectOrgMovePreviewQuery
	facts := &project.MockMoveFacts{
		PreviewProjectOrgMoveFunc: func(_ context.Context, q controlplane.ProjectOrgMovePreviewQuery) (*controlplane.ProjectOrgMovePreview, error) {
			asked = q
			return &controlplane.ProjectOrgMovePreview{
				SecretAccessToRevoke:  3,
				HostedProjectsStaying: []string{"shop"},
				TargetFunded:          &funded,
				InTargetOrg:           map[string]bool{"d-1": true},
			}, nil
		},
	}
	e, err := previewWithFacts(t, facts)
	require.NoError(t, err)
	assert.Equal(t, controlplane.ProjectOrgMovePreviewQuery{
		ProjectID: "p-mine", FromOrgID: "acme", ToOrgID: "globex", ActorUserID: "test-user",
		ForgeProjectName: "shop", DaemonIDs: []string{"d-1", "d-2"},
	}, asked)
	assert.True(t, e.GetControlPlaneEffectsKnown())
	require.NotNil(t, e.SecretAccessToRevoke)
	assert.EqualValues(t, 3, e.GetSecretAccessToRevoke())
	assert.Equal(t, []string{"shop"}, e.GetHostedProjectsStaying())
	require.NotNil(t, e.TargetFunded)
	assert.False(t, e.GetTargetFunded())
	require.Len(t, e.GetMachines(), 2)
	require.NotNil(t, e.GetMachines()[0].InTargetOrg)
	assert.True(t, e.GetMachines()[0].GetInTargetOrg())
	assert.Nil(t, e.GetMachines()[1].InTargetOrg, "a machine control-plane did not place stays unknown")
}

// Without control-plane's answer the preview still answers, with those facts
// unknown: no client (self-hosted), a control plane that predates the read,
// or an outage.
func TestPreviewProjectOrgMove_ControlPlaneFactsFallBackToUnknown(t *testing.T) {
	t.Parallel()
	for name, facts := range map[string]project.MoveFacts{
		"no control plane": nil,
		"an older control plane": &project.MockMoveFacts{PreviewProjectOrgMoveFunc: func(context.Context, controlplane.ProjectOrgMovePreviewQuery) (*controlplane.ProjectOrgMovePreview, error) {
			return nil, controlplane.ErrProjectOrgMoveUnsupported
		}},
		"an outage": &project.MockMoveFacts{PreviewProjectOrgMoveFunc: func(context.Context, controlplane.ProjectOrgMovePreviewQuery) (*controlplane.ProjectOrgMovePreview, error) {
			return nil, errors.New("dial tcp: connection refused")
		}},
	} {
		t.Run(name, func(t *testing.T) {
			e, err := previewWithFacts(t, facts)
			require.NoError(t, err)
			assert.False(t, e.GetControlPlaneEffectsKnown())
			assert.Nil(t, e.SecretAccessToRevoke)
			assert.Nil(t, e.TargetFunded)
			assert.Empty(t, e.GetHostedProjectsStaying())
			for _, m := range e.GetMachines() {
				assert.Nil(t, m.InTargetOrg)
			}
		})
	}
}

// Control-plane's fresh read of the person's authority wins over reliant's
// cached one: no longer an admin of the source, or no longer a member of the
// target, is the preview's refusal too.
func TestPreviewProjectOrgMove_ControlPlaneRefusalIsTheRefusal(t *testing.T) {
	t.Parallel()
	for reason, want := range map[string]string{
		controlplane.ReasonMoveSourceAdminRequired:     project.ReasonSourceAdminRequired,
		controlplane.ReasonMoveTargetMembershipMissing: project.ReasonNotOrgMember,
	} {
		t.Run(reason, func(t *testing.T) {
			_, err := previewWithFacts(t, &project.MockMoveFacts{PreviewProjectOrgMoveFunc: func(context.Context, controlplane.ProjectOrgMovePreviewQuery) (*controlplane.ProjectOrgMovePreview, error) {
				return nil, &controlplane.ProjectOrgMoveError{Code: "permission_denied", Reason: reason}
			}})
			ce := svcerr.ToConnect(err)
			require.NotNil(t, ce)
			assert.Equal(t, connect.CodePermissionDenied, ce.Code())
			assert.Equal(t, want, ce.Meta().Get(svcerr.ReasonHeader))
		})
	}
}
