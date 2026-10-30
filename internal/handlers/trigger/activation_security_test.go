package trigger

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/stretchr/testify/require"
)

const securityRoot = `name: secure-parent
inputs:
  credential: {type: string, default: ""}
triggers:
  - name: nightly
    schedule: {cron: "0 9 * * *"}
    prompt: do the work
entry: [child]
nodes:
  - id: child
    type: workflow
    ref: project://secure-child
`
const securityChild = `name: secure-child
entry: [ok]
nodes:
  - id: ok
    type: approval
    args: {title: harmless}
`

func securityActivation(t *testing.T) (*triggerTestEnv, tools.ActivationParams) {
	t.Helper()
	e := setupTriggerTest(t)
	e.saveWorkflow(t, "secure-child", securityChild)
	e.saveWorkflow(t, "secure-parent", securityRoot)
	return e, tools.ActivationParams{Name: "secure activation", Workflow: "secure-parent", WorkflowTrigger: "nightly", ProjectID: e.projectID, MachineID: &e.daemonID}
}
func TestAgentActivationChildMutationInvalidatesApproval(t *testing.T) {
	testkit.Slow(t, "DB-backed approval dependency mutation")
	e, p := securityActivation(t)
	before, err := e.svc.PrepareActivation(e.ctx, e.userID, tools.ToolCreateTriggerActivation, p)
	require.NoError(t, err)
	e.saveWorkflow(t, "secure-child", "name: secure-child\nentry: [shell]\nnodes:\n - id: shell\n   type: run\n   args: {command: 'echo wider-shell-privilege'}\n")
	after, err := e.svc.PrepareActivation(e.ctx, e.userID, tools.ToolCreateTriggerActivation, p)
	require.NoError(t, err)
	require.NotEqual(t, before.Hash, after.Hash)
	_, err = e.svc.ManageActivation(e.ctx, e.userID, tools.ToolCreateTriggerActivation, p, before.Hash)
	require.ErrorContains(t, err, "changed after approval")
	rows, err := e.repo.ListTriggers(e.ctx, core.TriggerFilters{UserID: e.userID})
	require.NoError(t, err)
	require.Empty(t, rows)
}
func TestAgentActivationPresetMutationInvalidatesApproval(t *testing.T) {
	testkit.Slow(t, "DB-backed preset privilege mutation")
	e, p := securityActivation(t)
	preset := &db.Preset{ID: uuid.NewString(), UserID: e.userID, Name: "safety", Slug: "safety", Tag: "agent", Params: map[string]interface{}{"credential": "initial-secret"}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	_, err := e.repo.CreatePreset(e.ctx, preset)
	require.NoError(t, err)
	p.Presets = map[string]string{"default": "safety"}
	before, err := e.svc.PrepareActivation(e.ctx, e.userID, tools.ToolCreateTriggerActivation, p)
	require.NoError(t, err)
	preset.Params["credential"] = "changed-secret"
	preset.Params["tools"] = []any{"shell"}
	_, err = e.repo.UpdatePreset(e.ctx, preset)
	require.NoError(t, err)
	after, err := e.svc.PrepareActivation(e.ctx, e.userID, tools.ToolCreateTriggerActivation, p)
	require.NoError(t, err)
	require.NotEqual(t, before.Hash, after.Hash)
	_, err = e.svc.ManageActivation(e.ctx, e.userID, tools.ToolCreateTriggerActivation, p, before.Hash)
	require.ErrorContains(t, err, "changed after approval")
	require.NotContains(t, before.Title, "initial-secret")
	require.NotContains(t, after.Title, "changed-secret")
}
func TestAgentActivationSecretsNeverEnterCardsOrAudit(t *testing.T) {
	testkit.Slow(t, "DB-backed approval/audit privacy")
	e, p := securityActivation(t)
	secret := "sk-test-very-private-password"
	e.saveWorkflow(t, "secure-child", strings.Replace(securityChild, "harmless", secret, 1))
	p.Params = map[string]any{"credential": secret}
	p.Message = &secret
	preview, err := e.svc.PrepareActivation(e.ctx, e.userID, tools.ToolCreateTriggerActivation, p)
	require.NoError(t, err)
	require.NotContains(t, preview.Title, secret)
	// Declined approval has no mutation and the persisted title is this safe string.
	require.LessOrEqual(t, len(preview.Title), automationpolicy.MaxDisplayBytes)
	result, err := e.svc.ManageActivation(e.ctx, e.userID, tools.ToolCreateTriggerActivation, p, preview.Hash)
	require.NoError(t, err)
	empty := ""
	update := tools.ActivationParams{TriggerID: result.ID, Params: map[string]any{}, Message: &empty}
	next, err := e.svc.PrepareActivation(e.ctx, e.userID, tools.ToolUpdateTriggerActivation, update)
	require.NoError(t, err)
	require.NotContains(t, next.Title, secret)
	_, err = e.svc.ManageActivation(e.ctx, e.userID, tools.ToolUpdateTriggerActivation, update, next.Hash)
	require.NoError(t, err)
	history, err := e.repo.ListTriggerChanges(e.ctx, e.userID, result.ID)
	require.NoError(t, err)
	require.Len(t, history, 2)
	for _, change := range history {
		require.NotContains(t, change.BeforeState, secret)
		require.NotContains(t, change.AfterState, secret)
		require.LessOrEqual(t, len(change.AfterState), automationpolicy.MaxDisplayBytes)
	}
	_, err = e.svc.ManageActivation(automationpolicy.WithExecution(e.ctx, automationpolicy.Execution{Unattended: true}), e.userID, tools.ToolUpdateTriggerActivation, update, next.Hash)
	require.ErrorContains(t, err, "Unattended")
}

// Interpose at the app write after approval verification. The concurrent edit
// attempts to replace ONLY the child. Dependency row locks must keep it behind
// the commit; schedule sync must not happen until the transaction committed.
type approvalBarrierRepo struct {
	Repository
	atWrite, release chan struct{}
	once             sync.Once
}

func (r *approvalBarrierRepo) CreateTrigger(ctx context.Context, tr *core.Trigger) error {
	r.once.Do(func() { close(r.atWrite); <-r.release })
	return r.Repository.CreateTrigger(ctx, tr)
}
func TestAgentActivationAtomicDependencyMutation(t *testing.T) {
	testkit.Slow(t, "DB-backed transactional approval barrier")
	e, p := securityActivation(t)
	preview, err := e.svc.PrepareActivation(e.ctx, e.userID, tools.ToolCreateTriggerActivation, p)
	require.NoError(t, err)
	barrier := &approvalBarrierRepo{Repository: e.repo, atWrite: make(chan struct{}), release: make(chan struct{})}
	e.configure(t, func(d *Deps) { d.Repo = barrier })
	done := make(chan error, 1)
	go func() {
		_, err := e.svc.ManageActivation(e.ctx, e.userID, tools.ToolCreateTriggerActivation, p, preview.Hash)
		done <- err
	}()
	select {
	case <-barrier.atWrite:
	case <-time.After(10 * time.Second):
		t.Fatal("activation never reached write barrier")
	}
	draft, err := e.repo.GetWorkflowDraftBySlug(e.ctx, e.userID, "secure-child")
	require.NoError(t, err)
	editCtx, cancel := context.WithTimeout(e.ctx, 50*time.Millisecond)
	defer cancel()
	err = e.repo.UpdateWorkflowDraftDefinition(editCtx, draft.ID, "secure-child", "secure-child", strings.Replace(securityChild, "harmless", "edited", 1), db.WorkflowDraftStatusComplete)
	require.Error(t, err, "child edit must block behind approved write transaction")
	require.Zero(t, e.backend.syncCount(), "external sync must not see uncommitted activation")
	close(barrier.release)
	require.NoError(t, <-done)
	require.Equal(t, 1, e.backend.syncCount())
}
func TestAgentActivationBoundedClosure(t *testing.T) {
	testkit.Slow(t, "DB-backed closure size limit")
	e, p := securityActivation(t)
	e.saveWorkflow(t, "secure-child", strings.Replace(securityChild, "harmless", strings.Repeat("x", maxApprovalSnapshotBytes+1), 1))
	_, err := e.svc.PrepareActivation(e.ctx, e.userID, tools.ToolCreateTriggerActivation, p)
	require.Error(t, err)
	require.Contains(t, fmt.Sprint(err), "limit")
}

// An unbounded event input that reaches a child workflow through a
// transforming argument still may not choose the child's tools: the child's
// resolved value ("view") carries no trace of the event.
func TestAgentActivationRefusesToolsAnEventPicksThroughAChild(t *testing.T) {
	testkit.Slow(t, "DB-backed activation approval with a child workflow")
	e := setupTriggerTest(t)
	e.saveWorkflow(t, "picks-tools", `name: picks-tools
inputs:
  level: {type: string, default: low}
entry: [agent]
nodes:
  - id: agent
    type: call_llm
    args:
      model: {tags: [fast]}
      tools_config:
        preloaded_tools: "{{ inputs.level == 'high' ? ['shell'] : ['view'] }}"
        loadable_tools: []
`)
	e.saveWorkflow(t, "event-parent", `name: event-parent
inputs:
  mode: {type: string, default: safe}
triggers:
  - name: hook
    schedule: {cron: "0 9 * * *"}
    inputs: {mode: '{{ trigger.payload.scheduled_for }}'}
    prompt: go
entry: [child]
nodes:
  - id: child
    type: workflow
    ref: project://picks-tools
    args:
      level: "{{ inputs.mode == 'admin' ? 'high' : 'low' }}"
`)
	p := tools.ActivationParams{Name: "event parent", Workflow: "event-parent", WorkflowTrigger: "hook", ProjectID: e.projectID, MachineID: &e.daemonID}
	_, err := e.svc.PrepareActivation(e.ctx, e.userID, tools.ToolCreateTriggerActivation, p)
	require.ErrorContains(t, err, "fixed privilege choices")
}
