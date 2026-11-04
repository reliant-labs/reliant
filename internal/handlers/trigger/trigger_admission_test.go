package trigger

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const admissionWorkflow = `name: triage
triggers:
  - name: nightly
    schedule: {cron: "0 9 * * *"}
    prompt: "Check."
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

// A parseable complete row can become invalid after an external edit or a
// validator upgrade. Admission must validate the graph, not just its trigger.
func TestEnableWorkflowRejectsInvalidTreeBeforeWrites(t *testing.T) {
	for _, tc := range []struct{ name, definition string }{
		{"root", strings.Replace(admissionWorkflow, "entry: [a]", "entry: [missing]", 1)},
		{"child", strings.Replace(admissionWorkflow, "type: approval\n    args: {title: ok}", "type: workflow\n    ref: child", 1)},
		{"unknown_key", admissionWorkflow + "invented_runtime_field: true\n"},
		{"draft", admissionWorkflow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupTriggerTest(t)
			env.saveWorkflow(t, "child", "name: child\nentry: [missing]\nnodes:\n  - {id: a, type: approval, args: {title: ok}}\n")
			env.saveWorkflow(t, "triage", tc.definition)
			if tc.name == "draft" {
				stored, err := env.repo.GetWorkflowDraftBySlug(env.ctx, env.userID, "triage")
				require.NoError(t, err)
				require.NoError(t, env.repo.UpdateWorkflowDraftDefinition(env.ctx, stored.ID, "triage", "triage", tc.definition, db.WorkflowDraftStatusDraft))
			}
			resp, err := env.svc.EnableWorkflow(env.ctx, connect.NewRequest(env.enableRequest(nil)))
			if err == nil {
				require.False(t, resp.Msg.Enabled, "invalid tree was enabled")
			}
			assert.Empty(t, env.storedTriggers(t), "rejected enable must write nothing")
			assert.Zero(t, env.backend.syncCount())
		})
	}
}

func TestLiveInvalidWorkflowIsBrokenBeforeFireAndCannotResume(t *testing.T) {
	env := setupTriggerTest(t)
	// Model daemon file sync rather than SaveWorkflow: external files may be
	// indexed, but an invalid current definition must not be executable.
	syncFile := func(definition string) {
		data, err := json.Marshal([]config.StoredWorkflow{{
			Name: "triage", RelativePath: ".reliant/workflows/triage.yaml", YAMLContent: definition,
		}})
		require.NoError(t, err)
		stored := string(data)
		now := time.Now().UTC()
		require.NoError(t, env.repo.UpsertProjectConfigRecord(env.ctx, &db.ProjectConfigRecord{
			ID: uuid.NewString(), ProjectID: env.projectID, DaemonID: env.daemonID,
			ProjectWorkflowsJSON: &stored, PushedAt: now, CreatedAt: now, UpdatedAt: now,
		}))
	}
	syncFile(admissionWorkflow)
	enabled := env.enable(t, env.enableRequest(nil))
	require.True(t, enabled.Enabled)
	id := enabled.Results[0].Activation.Id
	for _, definition := range []string{
		strings.Replace(admissionWorkflow, "entry: [a]", "entry: [missing]", 1),
		admissionWorkflow + "invented_runtime_field: true\n",
	} {
		syncFile(definition)
		row, err := env.repo.GetTrigger(env.ctx, id)
		require.NoError(t, err)
		rendered := env.svc.render(env.ctx, row)
		assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_BROKEN, rendered.GetHealth().GetStatus())
		_, err = env.svc.FireTrigger(env.ctx, connect.NewRequest(&reliantv1.FireTriggerRequest{Id: id}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
		assert.Empty(t, env.backend.fires, "invalid live graph must never reach the fire backend")
		_, err = env.svc.SetTriggerEnabled(env.ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: id, Enabled: false}))
		require.NoError(t, err)
		_, err = env.svc.SetTriggerEnabled(env.ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: id, Enabled: true}))
		require.Error(t, err)
	}
	syncFile(admissionWorkflow)
	row, err := env.repo.GetTrigger(env.ctx, id)
	require.NoError(t, err)
	assert.NotEqual(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_BROKEN, env.svc.render(env.ctx, row).GetHealth().GetStatus())
}
