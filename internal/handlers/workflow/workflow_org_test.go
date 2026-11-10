package workflow

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// workflowOrgSource: every caller is a live member of acme and home, not of
// globex; home is the default.
type workflowOrgSource struct{}

func (workflowOrgSource) ResolveOrgMembership(_ context.Context, _, orgID string) (accesstokenclient.OrgMembership, error) {
	if orgID == "" {
		return accesstokenclient.OrgMembership{OrgID: "home", Member: true}, nil
	}
	return accesstokenclient.OrgMembership{OrgID: orgID, Member: orgID == "acme" || orgID == "home"}, nil
}

func requireOrgRefusal(t *testing.T, err error, reason string) {
	t.Helper()
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "error: %v", err)
	assert.Equal(t, reason, svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader), "error: %v", err)
}

// A saved workflow is created in the org its request acts in, verified
// through the real resolver: an org the caller is not in, or another org than
// a machine credential's, is refused and stores nothing. A list naming an org
// shows that org's saved workflows only; one naming none shows them all.
func TestSavedWorkflows_OrgIsVerifiedAndListed(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	userID := "org-user-" + uuid.NewString()[:8]
	resolver := orgs.New(workflowOrgSource{})
	// A request through a listener carries the membership authz decides with.
	ctx := authz.WithMembership(context.WithValue(context.Background(), auth.UserIDContextKey, userID), resolver)
	now := time.Now().UTC()
	projectID := "org-project-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "P", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	svc := NewTestWorkflow(t, WithWorkflowDeps(Deps{Repo: repo, Orgs: resolver}))
	acme, home, globex := "acme", "home", "globex"
	machine := auth.WithMachineToken(ctx, &fat.Principal{OrgID: acme, ActingUserID: userID})

	create := func(ctx context.Context, title string, org *string) (*reliantv1.CreateWorkflowDraftResponse, error) {
		resp, err := svc.CreateWorkflowDraft(ctx, connect.NewRequest(&reliantv1.CreateWorkflowDraftRequest{
			ProjectId: projectID, Title: title, OrgId: org,
		}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	save := func(ctx context.Context, name string, org *string) error {
		_, err := svc.SaveWorkflow(ctx, connect.NewRequest(&reliantv1.SaveWorkflowRequest{
			ProjectId: projectID, OrgId: org,
			Workflow: mustParse(t, "name: "+name+"\nentry: [echo]\nnodes:\n  - id: echo\n    type: run\n    command: \"echo hi\"\n"),
		}))
		return err
	}

	_, err := create(ctx, "Not Mine", &globex)
	requireOrgRefusal(t, err, orgs.ReasonNotOrgMember)
	_, err = create(machine, "Elsewhere", &home)
	requireOrgRefusal(t, err, orgs.ReasonOrgMismatch)
	requireOrgRefusal(t, save(ctx, "not-mine", &globex), orgs.ReasonNotOrgMember)
	requireOrgRefusal(t, save(machine, "elsewhere", &home), orgs.ReasonOrgMismatch)
	stored, err := repo.ListWorkflowDraftsByUser(ctx, userID)
	require.NoError(t, err)
	require.Empty(t, stored, "a refused create stores no saved workflow")

	_, err = create(ctx, "Acme Flow", &acme)
	require.NoError(t, err)
	require.NoError(t, save(ctx, "home-flow", nil), "naming none acts in the default org")
	_, err = create(machine, "Machine Flow", nil)
	require.NoError(t, err, "a machine credential acts in its own org")
	stored, err = repo.ListWorkflowDraftsByUser(ctx, userID)
	require.NoError(t, err)
	orgOf := map[string]string{}
	for _, d := range stored {
		require.NotNil(t, d.OrgID, d.Slug)
		orgOf[d.Slug] = *d.OrgID
	}
	assert.Equal(t, map[string]string{"acme-flow": acme, "home-flow": home, "machine-flow": acme}, orgOf)

	includeDrafts := true // the saved workflows here are drafts
	listed := func(org *string) []string {
		t.Helper()
		resp, err := svc.ListWorkflows(ctx, connect.NewRequest(&reliantv1.ListWorkflowsRequest{ProjectId: projectID, OrgId: org, IncludeHidden: &includeDrafts}))
		require.NoError(t, err)
		var slugs []string
		for _, w := range resp.Msg.Workflows {
			if _, ok := orgOf[w.Filename]; ok && w.Source == "user" {
				slugs = append(slugs, w.Filename)
			}
		}
		return slugs
	}
	assert.ElementsMatch(t, []string{"acme-flow", "machine-flow"}, listed(&acme))
	assert.ElementsMatch(t, []string{"home-flow"}, listed(&home))
	assert.ElementsMatch(t, []string{"acme-flow", "home-flow", "machine-flow"}, listed(nil))
	_, err = svc.ListWorkflows(ctx, connect.NewRequest(&reliantv1.ListWorkflowsRequest{ProjectId: projectID, OrgId: &globex}))
	requireOrgRefusal(t, err, orgs.ReasonNotOrgMember)
}
