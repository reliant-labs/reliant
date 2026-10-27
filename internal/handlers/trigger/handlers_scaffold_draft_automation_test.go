// yours: scaffolded once, never touched again — forge will not overwrite this file.
package trigger_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"

	trigger "github.com/reliant-labs/reliant/internal/handlers/trigger"
)

type draftAutomationFake struct {
	got   trigger.DraftRequest
	draft *pb.AutomationDraft
	err   error
}

func (f *draftAutomationFake) DraftAutomation(_ context.Context, in trigger.DraftRequest) (*pb.AutomationDraft, error) {
	f.got = in
	return f.draft, f.err
}

func draftAutomationRepo(id string) *trigger.MockRepository {
	return &trigger.MockRepository{
		GetProjectWithUserCheckFunc: func(_ context.Context, projectID, _ string) (*db.Project, error) {
			if projectID != id {
				return nil, svcerr.WithMessage(svcerr.ErrNotFound, "project not found")
			}
			return &db.Project{ID: id}, nil
		},
	}
}

// DraftAutomation proposes; it never creates. The draft comes back as the
// drafter wrote it, and the request's workflow and zone reach the drafter.
func TestDraftAutomation_Generated(t *testing.T) {
	t.Parallel()

	draft := &pb.AutomationDraft{
		Definition: &pb.TriggerDefinition{Message: "Summarise failed CI", Source: &pb.TriggerDefinition_Schedule{
			Schedule: &pb.ScheduleSource{Cron: []string{"0 9 * * 1-5"}, Timezone: "America/New_York"},
		}},
		Summary: "Every weekday at 9:00, summarise failed CI",
	}
	drafter := &draftAutomationFake{draft: draft}
	svc := trigger.NewTestTrigger(t, trigger.WithTriggerDeps(trigger.Deps{Repo: draftAutomationRepo("proj-1"), Drafter: drafter}))
	noDrafter := trigger.NewTestTrigger(t, trigger.WithTriggerDeps(trigger.Deps{Repo: draftAutomationRepo("proj-1")}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.DraftAutomationRequest, pb.DraftAutomationResponse]{
		{
			Name: "drafts",
			Req: connect.NewRequest(&pb.DraftAutomationRequest{
				Description: " every weekday at 9, summarise failed CI ",
				ProjectId:   "proj-1",
				Workflow:    "ci-digest",
				Timezone:    "America/New_York",
			}),
			Ctx: trigger.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.DraftAutomationResponse]) {
				assert.Equal(t, "Every weekday at 9:00, summarise failed CI", resp.Msg.GetDraft().GetSummary())
				assert.Equal(t, "every weekday at 9, summarise failed CI", drafter.got.Description)
				assert.Equal(t, "ci-digest", drafter.got.Workflow)
				assert.Equal(t, "America/New_York", drafter.got.Timezone)
				assert.Equal(t, "test-user", drafter.got.UserID)
			},
		},
		{
			Name:    "empty description",
			Req:     connect.NewRequest(&pb.DraftAutomationRequest{Description: "  ", ProjectId: "proj-1"}),
			Ctx:     trigger.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "someone else's project",
			Req:     connect.NewRequest(&pb.DraftAutomationRequest{Description: "every hour", ProjectId: "proj-2"}),
			Ctx:     trigger.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "unknown time zone",
			Req:     connect.NewRequest(&pb.DraftAutomationRequest{Description: "every hour", ProjectId: "proj-1", Timezone: "Mars/Olympus"}),
			Ctx:     trigger.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.DraftAutomation)

	// A deployment that cannot draft says so; the dialog is still filled by hand.
	_, err := noDrafter.DraftAutomation(trigger.AuthedContext(t), connect.NewRequest(&pb.DraftAutomationRequest{Description: "every hour", ProjectId: "proj-1"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, svcerr.Code(err))
}
