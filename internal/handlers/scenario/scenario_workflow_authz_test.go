package scenario_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/handlers/scenario"
)

type scenarioMembership struct{ active bool }

func (m scenarioMembership) IsMember(context.Context, string, string) (bool, bool, error) {
	return m.active, true, nil
}

// An owned scenario is not an authorization capability for its associated
// workflow. Denial must happen before parsing or simulating the definition.
func TestRunScenarioChecksAssociatedWorkflowAccess(t *testing.T) {
	t.Parallel()
	orgID := "org"
	for _, tc := range []struct {
		name   string
		draft  *db.WorkflowDraft
		active bool
		want   connect.Code
	}{
		{"missing", nil, true, connect.CodeNotFound},
		{"foreign owner", &db.WorkflowDraft{UserID: "other"}, true, connect.CodeNotFound},
		{"removed member", &db.WorkflowDraft{UserID: "test-user", OrgID: &orgID}, false, connect.CodeNotFound},
		// Empty definitions deliberately fail parsing: authorized callers
		// reach the parser, while denied ones never consume the definition.
		{"current member", &db.WorkflowDraft{UserID: "test-user", OrgID: &orgID}, true, connect.CodeInvalidArgument},
		{"personal workflow", &db.WorkflowDraft{UserID: "test-user"}, false, connect.CodeInvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			drafts := &scenario.MockDrafts{GetWorkflowDraftFunc: func(_ context.Context, id string) (*db.WorkflowDraft, error) {
				require.Equal(t, ownWorkflowDraft, id)
				return tc.draft, nil
			}}
			svc := scenario.NewTestScenario(t, withDeps(scenario.Deps{Repo: newScenarioRepo(&fixture{}), Drafts: drafts}))
			ctx := authz.WithMembership(scenario.AuthedContext(t), scenarioMembership{active: tc.active})
			_, err := svc.RunScenario(ctx, connect.NewRequest(&pb.RunScenarioRequest{ScenarioId: ownScenarioID}))
			require.Error(t, err)
			require.Equal(t, tc.want, svcerr.Code(err), "err = %v", err)
		})
	}
}
