package workflow

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// workflowReads are the WorkflowService rpcs that change nothing. Every other
// rpc must have a case in TestEveryWorkflowMutationActsInItsOrg.
var workflowReads = map[string]bool{
	"ListWorkflows": true, "GetWorkflow": true, "ValidateWorkflow": true, "ExportWorkflow": true,
}

// leftAcmeWorkflowSource is workflowOrgSource after the caller left acme.
type leftAcmeWorkflowSource struct{}

func (leftAcmeWorkflowSource) ResolveOrgMembership(_ context.Context, _, orgID string) (accesstokenclient.OrgMembership, error) {
	if orgID == "" {
		return accesstokenclient.OrgMembership{OrgID: "home", Member: true}, nil
	}
	return accesstokenclient.OrgMembership{OrgID: orgID, Member: orgID == "home"}, nil
}

// Every mutating WorkflowService rpc acts in the org of the saved workflow it
// changes or reads from: a caller who has left the org, and a credential bound
// to another org, are refused, and no saved workflow is created, changed or
// deleted. The rpcs are enumerated from the service descriptor, so a new
// mutating rpc fails this test until it has a case here.
func TestEveryWorkflowMutationActsInItsOrg(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	userID := "org-rpcs-" + uuid.NewString()[:8]
	live := orgs.New(workflowOrgSource{})
	memberCtx := authz.WithMembership(context.WithValue(context.Background(), auth.UserIDContextKey, userID), live)
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	projectID := "org-rpcs-project-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(memberCtx, &db.Project{
		ID: projectID, UserID: userID, Name: "P", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	acme := "acme"
	const definition = "name: acme-flow\nentry: [echo]\nnodes:\n  - id: echo\n    type: run\n    command: \"echo hi\"\n"
	draft := &db.WorkflowDraft{
		ID: uuid.NewString(), UserID: userID, OrgID: &acme, Name: "acme-flow", Slug: "acme-flow",
		Definition: definition, Status: db.WorkflowDraftStatusComplete, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, repo.CreateWorkflowDraft(memberCtx, draft))
	edited := "name: acme-flow\nentry: [echo]\nnodes:\n  - id: echo\n    type: run\n    command: \"echo hijacked\"\n"

	var svc *Service
	calls := map[string]func(ctx context.Context) error{
		"SaveWorkflow": func(ctx context.Context) error {
			_, err := svc.SaveWorkflow(ctx, connect.NewRequest(&reliantv1.SaveWorkflowRequest{
				ProjectId: projectID, DraftId: &draft.ID, Workflow: mustParse(t, edited),
			}))
			return err
		},
		"SaveWorkflow over its name": func(ctx context.Context) error {
			_, err := svc.SaveWorkflow(ctx, connect.NewRequest(&reliantv1.SaveWorkflowRequest{
				ProjectId: projectID, Workflow: mustParse(t, edited),
			}))
			return err
		},
		"DeleteWorkflow": func(ctx context.Context) error {
			_, err := svc.DeleteWorkflow(ctx, connect.NewRequest(&reliantv1.DeleteWorkflowRequest{ProjectId: projectID, Name: "acme-flow"}))
			return err
		},
		"ImportWorkflow": func(ctx context.Context) error {
			_, err := svc.ImportWorkflow(ctx, connect.NewRequest(&reliantv1.ImportWorkflowRequest{
				ProjectId: projectID, YamlContent: []byte(edited), Scope: reliantv1.ConfigScope_CONFIG_SCOPE_GLOBAL, Overwrite: true,
			}))
			return err
		},
		"SetWorkflowVisibility": func(ctx context.Context) error {
			_, err := svc.SetWorkflowVisibility(ctx, connect.NewRequest(&reliantv1.SetWorkflowVisibilityRequest{
				ProjectId: projectID, Slug: "acme-flow", IsHidden: true,
			}))
			return err
		},
		"CopyWorkflow": func(ctx context.Context) error {
			_, err := svc.CopyWorkflow(ctx, connect.NewRequest(&reliantv1.CopyWorkflowRequest{ProjectId: projectID, SourceSlug: "acme-flow"}))
			return err
		},
		"CreateWorkflowDraft": func(ctx context.Context) error {
			_, err := svc.CreateWorkflowDraft(ctx, connect.NewRequest(&reliantv1.CreateWorkflowDraftRequest{
				ProjectId: projectID, Title: "In Acme", OrgId: &acme,
			}))
			return err
		},
		// The caller's own saved workflow is no orphan of acme's; what this
		// case pins is that a caller outside acme cannot reach it at all.
		"ForkOrphanedWorkflow": func(ctx context.Context) error {
			_, err := svc.ForkOrphanedWorkflow(ctx, connect.NewRequest(&reliantv1.ForkOrphanedWorkflowRequest{
				WorkflowId: draft.ID, ProjectId: projectID,
			}))
			return err
		},
		"SetWorkflowStatus": func(ctx context.Context) error {
			_, err := svc.SetWorkflowStatus(ctx, connect.NewRequest(&reliantv1.SetWorkflowStatusRequest{
				ProjectId: projectID, DraftId: draft.ID, Status: reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_DRAFT,
			}))
			return err
		},
	}

	methods := reliantv1.File_reliant_v1_workflow_proto.Services().ByName("WorkflowService").Methods()
	for i := range methods.Len() {
		name := string(methods.Get(i).Name())
		if !workflowReads[name] && calls[name] == nil {
			t.Errorf("WorkflowService.%s is neither a read nor covered here: add a case that it acts in its org", name)
		}
	}

	snapshot := func() string {
		t.Helper()
		drafts, err := repo.ListWorkflowDraftsByUser(memberCtx, userID)
		require.NoError(t, err)
		sort.Slice(drafts, func(i, j int) bool { return drafts[i].ID < drafts[j].ID })
		out, err := json.Marshal(drafts)
		require.NoError(t, err)
		return string(out)
	}

	removed := orgs.New(leftAcmeWorkflowSource{})
	callers := map[string]struct {
		ctx      context.Context
		resolver *orgs.Resolver
	}{
		"a removed member": {
			authz.WithMembership(context.WithValue(context.Background(), auth.UserIDContextKey, userID), removed), removed,
		},
		"a credential bound to another org": {
			auth.WithMachineToken(memberCtx, &fat.Principal{OrgID: "home", ActingUserID: userID}), live,
		},
	}
	for who, caller := range callers {
		svc = NewTestWorkflow(t, WithWorkflowDeps(Deps{Repo: repo, Orgs: caller.resolver}))
		for name, call := range calls {
			before := snapshot()
			err := call(caller.ctx)
			code := svcerr.Code(err)
			require.Truef(t, code == connect.CodeNotFound || code == connect.CodePermissionDenied || code == connect.CodeAlreadyExists,
				"%s: %s was not refused for its org: %v", who, name, err)
			require.Equal(t, before, snapshot(), "%s: %s changed what is stored", who, name)
		}
	}
}
