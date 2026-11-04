// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// triage declares one trigger of each stored kind that does not need a provider
// plus an integration trigger, so a missing connection can fail one of several.
const enableWorkflowYAML = `name: triage
inputs:
  mode: {type: string, default: ""}
  issue_number: {type: integer, default: 0}
triggers:
  - name: nightly
    schedule: {cron: "0 9 * * *"}
    prompt: "Do the nightly sweep."
  - name: hook
    webhook: {}
    prompt: "Handle the delivery."
  - name: new-issue
    integration: {integration: test, events: [issues.opened]}
    prompt: "Triage."
    inputs:
      issue_number: "{{ trigger.payload.data.issue.number }}"
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

func (e *triggerTestEnv) enableRequest(mutate func(*reliantv1.EnableWorkflowRequest)) *reliantv1.EnableWorkflowRequest {
	req := &reliantv1.EnableWorkflowRequest{Workflow: "triage", ProjectId: e.projectID, DaemonId: e.daemonID}
	if mutate != nil {
		mutate(req)
	}
	return req
}

func (e *triggerTestEnv) enable(t *testing.T, req *reliantv1.EnableWorkflowRequest) *reliantv1.EnableWorkflowResponse {
	t.Helper()
	resp, err := e.svc.EnableWorkflow(e.ctx, connect.NewRequest(req))
	require.NoError(t, err)
	return resp.Msg
}

func outcomes(resp *reliantv1.EnableWorkflowResponse) map[string]reliantv1.WorkflowTriggerOutcome {
	out := map[string]reliantv1.WorkflowTriggerOutcome{}
	for _, r := range resp.Results {
		out[r.Trigger] = r.Outcome
	}
	return out
}

func (e *triggerTestEnv) storedTriggers(t *testing.T) []*core.Trigger {
	t.Helper()
	list, err := e.repo.ListTriggers(context.Background(), core.TriggerFilters{UserID: e.userID})
	require.NoError(t, err)
	return list
}

func TestEnableWorkflowEnablesEveryDeclaredTrigger(t *testing.T) {
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", enableWorkflowYAML)
	conn := env.createConnection(t, env.userID, "test", "acct-1", core.ConnectionStatusActive)

	resp := env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		r.ConnectionIds = map[string]string{"test": conn.ID}
		r.Params = map[string]*structpb.Value{"mode": structpb.NewStringValue("strict"), "issue_number": structpb.NewNumberValue(7)}
	}))

	require.True(t, resp.Enabled, "%v", resp.Results)
	require.Len(t, resp.Results, 3)
	assert.Equal(t, []string{"nightly", "hook", "new-issue"}, []string{resp.Results[0].Trigger, resp.Results[1].Trigger, resp.Results[2].Trigger}, "in declaration order")
	for _, r := range resp.Results {
		assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_ENABLED, r.Outcome, r.Trigger)
		assert.True(t, r.Activation.GetEnabled(), r.Trigger)
		assert.Equal(t, r.Trigger, r.Activation.GetWorkflowTrigger())
		assert.Equal(t, env.daemonID, r.Activation.GetDaemonId(), "one machine choice serves every trigger")
	}
	assert.NotEmpty(t, resp.Results[1].Webhook.GetToken(), "a webhook's token comes back once")
	assert.Empty(t, resp.Results[0].Webhook.GetToken())
	assert.Equal(t, conn.ID, resp.Results[2].Activation.GetConnectionId())

	// The trigger that maps issue_number from the event does not store the
	// shared param of that name; the others keep it.
	byName := map[string]*reliantv1.Trigger{}
	for _, r := range resp.Results {
		byName[r.Trigger] = r.Activation
	}
	assert.NotContains(t, byName["new-issue"].GetParams(), "issue_number")
	assert.Contains(t, byName["nightly"].GetParams(), "issue_number")
	assert.Equal(t, "strict", byName["new-issue"].GetParams()["mode"].GetStringValue())
	assert.Len(t, env.storedTriggers(t), 3)
}

// One trigger with no connection refuses the lot, names the cause, and writes
// nothing: no row, no schedule, no webhook token.
func TestEnableWorkflowIsAllOrNothingWhenAConnectionIsMissing(t *testing.T) {
	testkit.Slow(t, "a DB-backed trigger handler test with many round trips to pgtest's Postgres")
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", enableWorkflowYAML)

	resp := env.enable(t, env.enableRequest(nil))

	assert.False(t, resp.Enabled)
	got := outcomes(resp)
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_FAILED, got["new-issue"])
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_NOT_ENABLED, got["nightly"])
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_NOT_ENABLED, got["hook"])
	for _, r := range resp.Results {
		if r.Trigger == "new-issue" {
			assert.Contains(t, r.Message, "connect test first")
		}
		assert.Nil(t, r.Activation, r.Trigger)
		assert.Nil(t, r.Webhook, r.Trigger)
	}
	assert.Empty(t, env.storedTriggers(t), "nothing was written")
	assert.Equal(t, 0, env.backend.syncCount(), "no schedule was converged")
}

// A failure while writing (here the schedule backend) undoes what was already
// written, and every result says so.
func TestEnableWorkflowUndoesWhatItWroteWhenAWriteFails(t *testing.T) {
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", `name: triage
triggers:
  - name: hook
    webhook: {}
    prompt: "x"
  - name: nightly
    schedule: {cron: "0 9 * * *"}
    prompt: "y"
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`)
	env.backend.syncErr = errors.New("temporal is down")

	resp := env.enable(t, env.enableRequest(nil))

	assert.False(t, resp.Enabled)
	got := outcomes(resp)
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_FAILED, got["nightly"])
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_NOT_ENABLED, got["hook"], "the webhook written first is undone")
	assert.Empty(t, env.storedTriggers(t), "no row is left behind")
	for _, r := range resp.Results {
		assert.Nil(t, r.Webhook, "an undone webhook's token is not handed out")
	}
}

func TestEnableWorkflowResumesAPausedActivationAndKeepsItsSettings(t *testing.T) {
	testkit.Slow(t, "a DB-backed trigger handler test with many round trips to pgtest's Postgres")
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", enableWorkflowYAML)
	conn := env.createConnection(t, env.userID, "test", "acct-1", core.ConnectionStatusActive)
	connect1 := func(r *reliantv1.EnableWorkflowRequest) { r.ConnectionIds = map[string]string{"test": conn.ID} }
	first := env.enable(t, env.enableRequest(connect1))
	require.True(t, first.Enabled)
	for _, r := range first.Results {
		assert.Empty(t, r.Activation.GetMessage(), "%s keeps its declaration's prompt", r.Trigger)
	}

	disabled, err := env.svc.DisableWorkflow(env.ctx, connect.NewRequest(&reliantv1.DisableWorkflowRequest{Workflow: "triage"}))
	require.NoError(t, err)
	require.Len(t, disabled.Msg.Paused, 3)
	assert.Empty(t, disabled.Msg.Failed)
	for _, p := range disabled.Msg.Paused {
		assert.False(t, p.GetEnabled())
	}

	again := env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		connect1(r)
		r.Message = "ignored: every declaration here has its own prompt"
	}))
	require.True(t, again.Enabled)
	for _, r := range again.Results {
		assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_RESUMED, r.Outcome, r.Trigger)
		assert.True(t, r.Activation.GetEnabled())
	}
	assert.Len(t, env.storedTriggers(t), 3, "no duplicates")

	third := env.enable(t, env.enableRequest(connect1))
	for _, r := range third.Results {
		assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_ALREADY_ENABLED, r.Outcome, r.Trigger)
	}
}

// Enabling a workflow that is already on is how its settings change: every
// activation is brought to the request, keeps its name and id, and stays one
// row per trigger.
func TestEnableWorkflowUpdatesTheSettingsOfAnEnabledAutomation(t *testing.T) {
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", enableWorkflowYAML)
	conn := env.createConnection(t, env.userID, "test", "acct-1", core.ConnectionStatusActive)
	first := env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		r.ConnectionIds = map[string]string{"test": conn.ID}
		r.Params = map[string]*structpb.Value{"mode": structpb.NewStringValue("lenient")}
	}))
	require.True(t, first.Enabled, "%v", first.Results)
	ids := map[string]string{}
	for _, r := range first.Results {
		ids[r.Trigger] = r.Activation.GetId()
	}

	resp := env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		r.ConnectionIds = map[string]string{"test": conn.ID}
		r.Params = map[string]*structpb.Value{"mode": structpb.NewStringValue("strict")}
		r.NotifyOnComplete = true
		r.NamePrefix = "ignored for an existing activation"
	}))

	require.True(t, resp.Enabled, "%v", resp.Results)
	for _, r := range resp.Results {
		assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_UPDATED, r.Outcome, r.Trigger)
		assert.Equal(t, ids[r.Trigger], r.Activation.GetId(), "%s is the same activation", r.Trigger)
		assert.Equal(t, "strict", r.Activation.GetParams()["mode"].GetStringValue(), r.Trigger)
		assert.True(t, r.Activation.GetNotifyOnComplete(), r.Trigger)
		assert.True(t, r.Activation.GetEnabled(), r.Trigger)
		assert.Equal(t, "triage · "+r.Trigger, r.Activation.GetName(), "an existing activation keeps its name")
	}
	assert.Len(t, env.storedTriggers(t), 3, "no duplicates")

	again := env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		r.ConnectionIds = map[string]string{"test": conn.ID}
		r.Params = map[string]*structpb.Value{"mode": structpb.NewStringValue("strict")}
		r.NotifyOnComplete = true
	}))
	for _, r := range again.Results {
		assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_ALREADY_ENABLED, r.Outcome,
			"%s: the same settings again change nothing", r.Trigger)
	}
}

// Finish setup is the same call on a paused automation: it takes the new
// settings and resumes.
func TestEnableWorkflowResumesAPausedAutomationWithNewSettings(t *testing.T) {
	testkit.Slow(t, "a DB-backed trigger handler test with many round trips to pgtest's Postgres")
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", enableWorkflowYAML)
	conn := env.createConnection(t, env.userID, "test", "acct-1", core.ConnectionStatusActive)
	connect1 := func(r *reliantv1.EnableWorkflowRequest) { r.ConnectionIds = map[string]string{"test": conn.ID} }
	require.True(t, env.enable(t, env.enableRequest(connect1)).Enabled)
	_, err := env.svc.DisableWorkflow(env.ctx, connect.NewRequest(&reliantv1.DisableWorkflowRequest{Workflow: "triage"}))
	require.NoError(t, err)

	resp := env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		connect1(r)
		r.Params = map[string]*structpb.Value{"mode": structpb.NewStringValue("strict")}
	}))

	require.True(t, resp.Enabled, "%v", resp.Results)
	for _, r := range resp.Results {
		assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_RESUMED, r.Outcome, r.Trigger)
		assert.Equal(t, "strict", r.Activation.GetParams()["mode"].GetStringValue(), r.Trigger)
	}
	for _, tr := range env.storedTriggers(t) {
		assert.True(t, tr.Enabled, tr.Name)
		assert.Equal(t, "strict", tr.Params["mode"], tr.Name)
	}
}

// An existing activation's new settings are validated like a new one's, and a
// refusal changes nothing anywhere.
func TestEnableWorkflowRevalidatesAnExistingAutomationAndRefusesWithoutWriting(t *testing.T) {
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", enableWorkflowYAML)
	conn := env.createConnection(t, env.userID, "test", "acct-1", core.ConnectionStatusActive)
	require.True(t, env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		r.ConnectionIds = map[string]string{"test": conn.ID}
		r.Params = map[string]*structpb.Value{"mode": structpb.NewStringValue("lenient")}
	})).Enabled)

	resp := env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		r.ConnectionIds = map[string]string{"test": conn.ID}
		r.Params = map[string]*structpb.Value{"mode": structpb.NewNumberValue(3)}
	}))

	assert.False(t, resp.Enabled)
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_FAILED, outcomes(resp)["nightly"])
	assert.Contains(t, resultFor(resp, "nightly").Message, "params")
	for _, tr := range env.storedTriggers(t) {
		assert.Equal(t, "lenient", tr.Params["mode"], "%s keeps its settings", tr.Name)
		assert.True(t, tr.Enabled, tr.Name)
	}
}

// A write that fails after another activation was already changed puts that
// one's previous settings back.
func TestEnableWorkflowPutsBackAnUpdatedAutomationWhenALaterWriteFails(t *testing.T) {
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", `name: triage
inputs:
  mode: {type: string, default: ""}
triggers:
  - name: hook
    webhook: {}
    prompt: "x"
  - name: nightly
    schedule: {cron: "0 9 * * *"}
    prompt: "y"
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`)
	withMode := func(mode string) func(*reliantv1.EnableWorkflowRequest) {
		return func(r *reliantv1.EnableWorkflowRequest) {
			r.Params = map[string]*structpb.Value{"mode": structpb.NewStringValue(mode)}
		}
	}
	require.True(t, env.enable(t, env.enableRequest(withMode("lenient"))).Enabled)
	env.backend.syncErr = errors.New("temporal is down")

	resp := env.enable(t, env.enableRequest(withMode("strict")))

	assert.False(t, resp.Enabled)
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_FAILED, outcomes(resp)["nightly"])
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_NOT_ENABLED, outcomes(resp)["hook"])
	assert.Contains(t, resultFor(resp, "hook").Message, "keeps its previous settings")
	for _, tr := range env.storedTriggers(t) {
		assert.Equal(t, "lenient", tr.Params["mode"], "%s has its previous settings back", tr.Name)
		assert.True(t, tr.Enabled, tr.Name)
	}
}

// A request that names no connection keeps the one an activation listens
// through instead of moving it to the default.
func TestEnableWorkflowKeepsAnUnnamedConnection(t *testing.T) {
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", enableWorkflowYAML)
	env.createConnection(t, env.userID, "test", "acct-1", core.ConnectionStatusActive)
	other := env.createConnection(t, env.userID, "test", "acct-2", core.ConnectionStatusActive)
	require.True(t, env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		r.ConnectionIds = map[string]string{"test": other.ID}
	})).Enabled)

	resp := env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		r.Params = map[string]*structpb.Value{"mode": structpb.NewStringValue("strict")}
	}))

	require.True(t, resp.Enabled, "%v", resp.Results)
	assert.Equal(t, other.ID, resultFor(resp, "new-issue").Activation.GetConnectionId())
}

// The shared prompt fills in for a declaration that has none and never
// overrides one that does.
func TestEnableWorkflowSharedPromptOnlyFillsDeclarationsWithoutOne(t *testing.T) {
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", `name: triage
triggers:
  - name: written
    schedule: {cron: "0 9 * * *"}
    prompt: "The author's prompt."
  - name: bare
    schedule: {cron: "0 10 * * *"}
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`)
	unresolved := env.enable(t, env.enableRequest(nil))
	assert.False(t, unresolved.Enabled, "a trigger with no prompt anywhere is refused")
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_FAILED, outcomes(unresolved)["bare"])
	assert.Contains(t, resultFor(unresolved, "bare").Message, "message is required")
	assert.Empty(t, env.storedTriggers(t))

	resp := env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) { r.Message = "Shared." }))
	require.True(t, resp.Enabled, "%v", resp.Results)
	assert.Empty(t, resultFor(resp, "written").Activation.GetMessage())
	assert.Equal(t, "Shared.", resultFor(resp, "bare").Activation.GetMessage())
}

// The workflow's params are what filters read as activation.params, so one that
// names no input or has the wrong type is refused for every trigger, and
// nothing is written.
func TestEnableWorkflowRefusesBadParamsWithoutWriting(t *testing.T) {
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", enableWorkflowYAML)
	conn := env.createConnection(t, env.userID, "test", "acct-1", core.ConnectionStatusActive)

	resp := env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		r.ConnectionIds = map[string]string{"test": conn.ID}
		r.Params = map[string]*structpb.Value{"nope": structpb.NewStringValue("x")}
	}))

	assert.False(t, resp.Enabled)
	assert.Contains(t, resultFor(resp, "nightly").Message, `"nope" is not an input of this workflow`)
	assert.Empty(t, env.storedTriggers(t))
}

func resultFor(resp *reliantv1.EnableWorkflowResponse, trigger string) *reliantv1.WorkflowTriggerResult {
	for _, r := range resp.Results {
		if r.Trigger == trigger {
			return r
		}
	}
	return nil
}

func TestEnableWorkflowOverridesOneTrigger(t *testing.T) {
	testkit.Slow(t, "a DB-backed trigger handler test with many round trips to pgtest's Postgres")
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", enableWorkflowYAML)
	conn := env.createConnection(t, env.userID, "test", "acct-1", core.ConnectionStatusActive)
	other := env.createConnection(t, env.userID, "test", "acct-2", core.ConnectionStatusActive)
	name := "digest"
	resp := env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		r.ConnectionIds = map[string]string{"test": conn.ID}
		r.Overrides = []*reliantv1.WorkflowTriggerOverride{
			{Trigger: "new-issue", ConnectionId: &other.ID},
			{Trigger: "nightly", Name: &name, Message: ptr.Of("Custom prompt")},
		}
	}))
	require.True(t, resp.Enabled, "%v", resp.Results)
	by := map[string]*reliantv1.Trigger{}
	for _, r := range resp.Results {
		by[r.Trigger] = r.Activation
	}
	assert.Equal(t, other.ID, by["new-issue"].GetConnectionId())
	assert.Equal(t, "digest", by["nightly"].GetName())
	assert.Equal(t, "Custom prompt", by["nightly"].GetMessage())

	_, err := env.svc.EnableWorkflow(env.ctx, connect.NewRequest(env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		r.Overrides = []*reliantv1.WorkflowTriggerOverride{{Trigger: "nope"}}
	})))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
}

// Naming triggers narrows the enable to them: an already-activated one is
// brought to the request in place (same id, one row), a new one is created,
// and the rest are left alone. This is `reliant trigger activate --trigger`,
// which used to CreateTrigger a second row of the same name and fail with an
// opaque "database error".
func TestEnableWorkflowNamedTriggersUpdatesInPlaceAndLeavesTheRest(t *testing.T) {
	testkit.Slow(t, "a DB-backed trigger handler test with many round trips to pgtest's Postgres")
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", enableWorkflowYAML)
	conn := env.createConnection(t, env.userID, "test", "acct-1", core.ConnectionStatusActive)

	first := env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		r.ConnectionIds = map[string]string{"test": conn.ID}
		r.Triggers = []string{"new-issue"}
	}))
	require.True(t, first.Enabled, "%v", first.Results)
	require.Len(t, first.Results, 1, "only the named trigger is reported")
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_ENABLED, first.Results[0].Outcome)
	id := first.Results[0].Activation.GetId()
	require.Len(t, env.storedTriggers(t), 1, "the others are not activated")

	again := env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		r.ConnectionIds = map[string]string{"test": conn.ID}
		r.Triggers = []string{"new-issue", "nightly"}
		r.ActingIdentity = reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER
		r.NotifyOnComplete = true
	}))
	require.True(t, again.Enabled, "%v", again.Results)
	got := map[string]*reliantv1.WorkflowTriggerResult{}
	for _, r := range again.Results {
		got[r.Trigger] = r
	}
	require.Len(t, got, 2)
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_UPDATED, got["new-issue"].Outcome)
	assert.Equal(t, id, got["new-issue"].Activation.GetId(), "re-activating keeps the activation (and its firings and capacity scope)")
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_ENABLED, got["nightly"].Outcome)
	assert.Len(t, env.storedTriggers(t), 2, "no duplicate row; hook is still not activated")

	_, err := env.svc.EnableWorkflow(env.ctx, connect.NewRequest(env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		r.Triggers = []string{"nope"}
	})))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err), "an undeclared name refuses, it does not enable nothing")

	_, err = env.svc.EnableWorkflow(env.ctx, connect.NewRequest(env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		r.Triggers = []string{"nightly"}
		r.Overrides = []*reliantv1.WorkflowTriggerOverride{{Trigger: "hook"}}
	})))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err), "an override for a trigger the request does not enable is refused")

	// A connection for an integration no enabled trigger listens through is
	// a typo to refuse, not a key to ignore (the trigger would listen through
	// the default connection instead), and nothing is written.
	_, err = env.svc.EnableWorkflow(env.ctx, connect.NewRequest(env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		r.Triggers = []string{"new-issue"}
		r.ConnectionIds = map[string]string{"tset": conn.ID}
	})))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err), "an unmatched connection key is refused")
	_, err = env.svc.EnableWorkflow(env.ctx, connect.NewRequest(env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		r.Triggers = []string{"nightly"}
		r.ConnectionIds = map[string]string{"test": conn.ID}
	})))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err), "a connection for a trigger the request does not enable is refused")
	assert.Len(t, env.storedTriggers(t), 2)

	// A second trigger of an existing name is the caller's conflict, not an
	// internal error.
	_, err = env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: &reliantv1.TriggerDefinition{
		Name:      got["nightly"].Activation.GetName(),
		ProjectId: env.projectID,
		DaemonId:  env.daemonID,
		Workflow:  "triage",
		Source:    &reliantv1.TriggerDefinition_WorkflowTrigger{WorkflowTrigger: "nightly"},
	}}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, svcerr.Code(err))
	assert.Len(t, env.storedTriggers(t), 2)
}

func withUser(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, auth.UserIDContextKey, userID)
}

func TestEnableWorkflowRefusals(t *testing.T) {
	testkit.Slow(t, "a DB-backed trigger handler test with many round trips to pgtest's Postgres")
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "plain", "name: plain\nentry: [a]\nnodes:\n  - id: a\n    type: approval\n    args: {title: ok}\n")

	_, err := env.svc.EnableWorkflow(env.ctx, connect.NewRequest(env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) { r.Workflow = "plain" })))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "a workflow with no triggers has nothing to enable")

	_, err = env.svc.EnableWorkflow(env.ctx, connect.NewRequest(env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) { r.Workflow = "missing" })))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))

	_, err = env.svc.EnableWorkflow(env.ctx, connect.NewRequest(env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) { r.ProjectId = "" })))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
}

// A machine the project cannot run on refuses the lot, like any activation.
func TestEnableWorkflowRefusesAnUnknownMachineWithoutWriting(t *testing.T) {
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", enableWorkflowYAML)
	resp := env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) { r.DaemonId = "no-such-daemon" }))
	assert.False(t, resp.Enabled)
	assert.Empty(t, env.storedTriggers(t))
}

func TestDisableWorkflowPausesOnlyThatWorkflowsActivations(t *testing.T) {
	testkit.Slow(t, "a DB-backed trigger handler test with many round trips to pgtest's Postgres")
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", enableWorkflowYAML)
	conn := env.createConnection(t, env.userID, "test", "acct-1", core.ConnectionStatusActive)
	require.True(t, env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) { r.ConnectionIds = map[string]string{"test": conn.ID} })).Enabled)
	unrelated := env.create(t, env.definition(nil)) // an ad hoc schedule on another workflow

	resp, err := env.svc.DisableWorkflow(env.ctx, connect.NewRequest(&reliantv1.DisableWorkflowRequest{Workflow: "triage"}))
	require.NoError(t, err)
	assert.Len(t, resp.Msg.Paused, 3)

	after, err := env.repo.GetTrigger(context.Background(), unrelated.GetId())
	require.NoError(t, err)
	assert.True(t, after.Enabled, "another workflow's automation is untouched")
}

// A pause that fails does not stop the others, and is reported.
func TestDisableWorkflowReportsAFailureAndPausesTheRest(t *testing.T) {
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", enableWorkflowYAML)
	conn := env.createConnection(t, env.userID, "test", "acct-1", core.ConnectionStatusActive)
	require.True(t, env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) { r.ConnectionIds = map[string]string{"test": conn.ID} })).Enabled)

	// Schedule and integration triggers converge through the schedule backend;
	// a webhook has nothing to converge.
	env.backend.syncErr = errors.New("temporal is down")

	resp, err := env.svc.DisableWorkflow(env.ctx, connect.NewRequest(&reliantv1.DisableWorkflowRequest{Workflow: "triage"}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Failed, 2)
	for _, f := range resp.Msg.Failed {
		// The schedule backend's own text is server-side: the person reads
		// what failed, and the log reads why.
		assert.Equal(t, "the schedule backend is unavailable", f.Message)
		assert.NotEmpty(t, f.TriggerId)
	}
	require.Len(t, resp.Msg.Paused, 1, "the webhook pauses though the others could not")
	assert.Equal(t, "hook", resp.Msg.Paused[0].GetWorkflowTrigger())
}

// One user cannot enable, resume or pause another's activations.
func TestWorkflowEnableAndDisableAreScopedToTheCaller(t *testing.T) {
	testkit.Slow(t, "a DB-backed trigger handler test with many round trips to pgtest's Postgres")
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", enableWorkflowYAML)
	conn := env.createConnection(t, env.userID, "test", "acct-1", core.ConnectionStatusActive)
	require.True(t, env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) { r.ConnectionIds = map[string]string{"test": conn.ID} })).Enabled)

	stranger := withUser(env.ctx, "someone-else")
	resp, err := env.svc.DisableWorkflow(stranger, connect.NewRequest(&reliantv1.DisableWorkflowRequest{Workflow: "triage"}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.Paused)
	for _, tr := range env.storedTriggers(t) {
		assert.True(t, tr.Enabled)
	}
}

const hookOnlyYAML = `name: triage
triggers:
  - name: hook
    webhook: {}
    prompt: "Handle the delivery."
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

// leftover is an automation of some other workflow that already holds name.
func (e *triggerTestEnv) leftover(t *testing.T, name, workflow string) {
	t.Helper()
	trigger := "hook"
	require.NoError(t, e.repo.CreateTrigger(context.Background(), &core.Trigger{
		ID: uuid.NewString(), UserID: e.userID, ProjectID: e.projectID, Name: name,
		Kind: core.TriggerKindWebhook, Enabled: false, Workflow: workflow, WorkflowTrigger: &trigger,
		ActingIdentity: core.ActingIdentityOwner,
	}))
}

func (e *triggerTestEnv) storedNames(t *testing.T) []string {
	t.Helper()
	var names []string
	for _, tr := range e.storedTriggers(t) {
		names = append(names, tr.Name)
	}
	return names
}

// A paused automation whose workflow was deleted is debris: it must not block
// enabling a workflow that would take the same default name.
func TestEnableWorkflowIsNotBlockedByABrokenLeftoverOfADeletedWorkflow(t *testing.T) {
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", hookOnlyYAML)
	env.leftover(t, "triage · hook", "no-such-workflow")

	resp := env.enable(t, env.enableRequest(nil))

	require.True(t, resp.Enabled, "%v", resp.Results)
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_ENABLED, outcomes(resp)["hook"])
	assert.ElementsMatch(t, []string{"triage · hook", "triage (yours) · hook"}, env.storedNames(t),
		"the leftover is untouched and the new automation is named apart from it")
}

// A live automation of another workflow holding the default name no longer
// asks the user to invent a prefix: the new names are qualified by source.
func TestEnableWorkflowNamesApartFromALiveAutomationOfAnotherWorkflow(t *testing.T) {
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", hookOnlyYAML)
	env.saveWorkflow(t, "other", hookOnlyYAML)
	env.leftover(t, "triage · hook", "other")

	resp := env.enable(t, env.enableRequest(nil))

	require.True(t, resp.Enabled, "%v", resp.Results)
	assert.Equal(t, "triage (yours) · hook", resp.Results[0].Activation.GetName())
}

func TestActivationPrefixIsQualifiedBySourceThenNumbered(t *testing.T) {
	t.Parallel()

	svc := &Service{}
	declared := []*reliantv1.WorkflowTrigger{{Name: "a"}, {Name: "b"}}
	pick := func(workflow, prefix string, taken ...string) string {
		set := map[string]bool{}
		for _, n := range taken {
			set[n] = true
		}
		return svc.activationPrefix(&reliantv1.EnableWorkflowRequest{Workflow: workflow, NamePrefix: prefix},
			declared, map[string]*core.Trigger{}, map[string]*reliantv1.WorkflowTriggerOverride{}, set)
	}

	assert.Equal(t, "PR review", pick("builtin://pr-review", "PR review"), "a free prefix is used as given")
	assert.Equal(t, "PR review (built-in)", pick("builtin://pr-review", "PR review", "PR review · b"))
	assert.Equal(t, "PR review (yours)", pick("pr-review", "PR review", "PR review · a"))
	assert.Equal(t, "PR review (2)", pick("x", "PR review", "PR review · a", "PR review (yours) · a"),
		"once the source-qualified name is taken too, a number settles it")
}

func TestActivationPrefixLeavesAResumedActivationAndAChosenNameAlone(t *testing.T) {
	t.Parallel()

	svc := &Service{}
	declared := []*reliantv1.WorkflowTrigger{{Name: "a"}, {Name: "b"}}
	chosen := "My own name"
	got := svc.activationPrefix(
		&reliantv1.EnableWorkflowRequest{Workflow: "w", NamePrefix: "P"},
		declared,
		map[string]*core.Trigger{"a": {Name: "P · a"}},
		map[string]*reliantv1.WorkflowTriggerOverride{"b": {Trigger: "b", Name: &chosen}},
		map[string]bool{"P · a": true, "My own name": true},
	)
	assert.Equal(t, "P", got, "a resumed activation keeps its name and a chosen name is not rewritten")
}

// A name the caller chose for one trigger is theirs: if it is taken, say so.
func TestEnableWorkflowStillRefusesAChosenNameThatIsTaken(t *testing.T) {
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", hookOnlyYAML)
	env.leftover(t, "mine", "no-such-workflow")
	name := "mine"

	resp := env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		r.Overrides = []*reliantv1.WorkflowTriggerOverride{{Trigger: "hook", Name: &name}}
	}))

	assert.False(t, resp.Enabled)
	assert.Contains(t, resp.Results[0].Message, `"mine"`)
	assert.Equal(t, []string{"mine"}, env.storedNames(t))
}

func TestRetireDeletedTriggersDeletesTheirSchedules(t *testing.T) {
	t.Parallel()

	env := setupTriggerTest(t)
	env.svc.RetireDeletedTriggers(env.ctx, []*core.Trigger{
		{ID: "s1", UserID: env.userID, Kind: core.TriggerKindSchedule},
		{ID: "w1", UserID: env.userID, Kind: core.TriggerKindWebhook},
	})
	assert.Equal(t, []string{"s1"}, env.backend.deleted, "only a schedule-backed trigger has a schedule to delete")
}
