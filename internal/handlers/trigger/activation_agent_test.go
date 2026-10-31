package trigger

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/testkit"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestAgentActivationValidationAuditAndMachineBinding(t *testing.T) {
	testkit.Slow(t, "DB-backed activation authorization and validation parity")
	env := setupTriggerTest(t)
	env.saveWorkflow(t, "triage", declaringWorkflowYAML)
	machine := env.daemonID
	message := "Run nightly checks"
	p := tools.ActivationParams{Name: "agent nightly", Workflow: "triage", WorkflowTrigger: "nightly", ProjectID: env.projectID, MachineID: &machine, Message: &message}
	preview, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolCreateTriggerActivation, p)
	require.NoError(t, err)
	require.Contains(t, preview.Title, machine)
	all, err := env.repo.ListTriggers(env.ctx, core.TriggerFilters{UserID: env.userID})
	require.NoError(t, err)
	require.Empty(t, all, "preview must not write")
	_, err = env.svc.ManageActivation(env.ctx, env.userID, tools.ToolCreateTriggerActivation, p, "")
	require.ErrorContains(t, err, "approval")
	ctx := automationpolicy.WithActor(env.ctx, automationpolicy.Actor{ChatID: "caller", RunID: "thread", ToolCallID: "call"})
	created, err := env.svc.ManageActivation(ctx, env.userID, tools.ToolCreateTriggerActivation, p, preview.Hash)
	require.NoError(t, err)
	require.Equal(t, machine, created.DaemonID)
	// Explicit machine re-point is validated and survives the ordinary app RPC.
	other := uuid.NewString()
	hostname := "cicd"
	require.NoError(t, env.repo.UpsertDaemon(env.ctx, &db.Daemon{ID: other, UserID: env.userID, Name: "cicd", Hostname: &hostname}))
	repoint := tools.ActivationParams{TriggerID: created.ID, MachineID: &other}
	move, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, repoint)
	require.NoError(t, err)
	require.Contains(t, move.Title, "cicd")
	require.Contains(t, move.Title, other)
	moved, err := env.svc.ManageActivation(ctx, env.userID, tools.ToolUpdateTriggerActivation, repoint, move.Hash)
	require.NoError(t, err)
	require.Equal(t, other, moved.DaemonID)
	require.Equal(t, "cicd", moved.DaemonName)
	changes, err := env.repo.ListTriggerChanges(env.ctx, env.userID, created.ID)
	require.NoError(t, err)
	require.Len(t, changes, 2)
	require.Equal(t, "caller", changes[0].ChatID)
	require.Equal(t, "thread", changes[0].RunID)
	require.Equal(t, "call", changes[0].ToolCallID)
	require.Contains(t, changes[0].AfterState, other)
	require.Contains(t, changes[0].BeforeState, machine)
	p = tools.ActivationParams{TriggerID: created.ID}
	pause, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolPauseTrigger, p)
	require.NoError(t, err)
	_, err = env.svc.ManageActivation(ctx, env.userID, tools.ToolPauseTrigger, p, pause.Hash)
	require.NoError(t, err)
	changes, err = env.repo.ListTriggerChanges(env.ctx, env.userID, created.ID)
	require.NoError(t, err)
	require.Len(t, changes, 3)
	require.Equal(t, "pause", changes[0].Operation)
	require.NotEmpty(t, changes[0].BeforeState)
	// The storage row lock also protects the gap between preview and write.
	stored, e := env.repo.GetTrigger(env.ctx, created.ID)
	require.NoError(t, e)
	staleCtx := automationpolicy.WithRevision(env.ctx, stored.UpdatedAt.Add(-time.Second))
	require.ErrorContains(t, env.repo.SetTriggerEnabled(staleCtx, created.ID, true), "changed after approval")
	// Approval is invalidated if either the saved definition or workflow
	// changes. Under always, a resume needs a card, so the card's hash binds.
	env.setApprovalMode(t, nil, automationpolicy.ApprovalAlways)
	resume, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolEnableTrigger, p)
	require.NoError(t, err)
	_, err = env.svc.SetTriggerEnabled(env.ctx, connect.NewRequest(&pb.SetTriggerEnabledRequest{Id: created.ID, Enabled: false}))
	require.NoError(t, err)
	_, err = env.svc.ManageActivation(ctx, env.userID, tools.ToolEnableTrigger, p, resume.Hash)
	require.ErrorContains(t, err, "changed after approval")
	detail, err := env.svc.GetTrigger(env.ctx, connect.NewRequest(&pb.GetTriggerRequest{Id: created.ID}))
	require.NoError(t, err)
	require.Len(t, detail.Msg.Trigger.Changes, 4)
}

func TestAgentActivationAppValidationParity(t *testing.T) {
	testkit.Slow(t, "DB-backed activation setup and match validation")
	env := setupNeedsSetup(t)
	for _, params := range []map[string]any{nil, {"repos": "not-a-list"}, {"repos": []any{}}, {"repos": []any{"acme/web"}}} {
		p := tools.ActivationParams{Name: "parity", Workflow: "triage", WorkflowTrigger: "new-issue", ProjectID: env.projectID, MachineID: &env.daemonID, ConnectionID: &env.connID, Params: params}
		_, agentErr := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolCreateTriggerActivation, p)
		def := env.activationDef(nil)
		def.Name = "parity"
		def.Params = map[string]*structpb.Value{}
		for k, v := range params {
			value, e := structpb.NewValue(v)
			require.NoError(t, e)
			def.Params[k] = value
		}
		_, appErr := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&pb.CreateTriggerRequest{Trigger: def}))
		if appErr != nil {
			require.Error(t, agentErr)
			require.Equal(t, appErr.Error(), agentErr.Error())
		} else {
			require.NoError(t, agentErr)
		}
	}
}
