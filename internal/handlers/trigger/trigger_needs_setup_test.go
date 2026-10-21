// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// A workflow with one required, multi-valued input the trigger does not map:
// every activation must say which repositories it watches.
const requiredReposYAML = `name: triage
inputs:
  repos: {type: github.repository, multi: true, required: true, min_items: 1, max_items: 2}
  team: {type: string, default: platform}
  issue_number: {type: integer, default: 0}
triggers:
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

func setupReposParam(repos ...any) map[string]*structpb.Value {
	list, _ := structpb.NewList(repos)
	return map[string]*structpb.Value{"repos": structpb.NewListValue(list)}
}

type needsSetupEnv struct {
	*triggerTestEnv
	connID string
	seeded int
}

func setupNeedsSetup(t *testing.T) *needsSetupEnv {
	t.Helper()
	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", requiredReposYAML)
	conn := env.createConnection(t, env.userID, "test", "acct-1", core.ConnectionStatusActive)
	return &needsSetupEnv{triggerTestEnv: env, connID: conn.ID}
}

func (e *needsSetupEnv) activationDef(mutate func(*reliantv1.TriggerDefinition)) *reliantv1.TriggerDefinition {
	return e.activation("new-issue", func(d *reliantv1.TriggerDefinition) {
		d.ConnectionId = &e.connID
		if mutate != nil {
			mutate(d)
		}
	})
}

func (e *needsSetupEnv) enableReq(params map[string]*structpb.Value) *reliantv1.EnableWorkflowRequest {
	return e.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		r.ConnectionIds = map[string]string{"test": e.connID}
		r.Params = params
	})
}

func (e *needsSetupEnv) get(t *testing.T, id string) *reliantv1.Trigger {
	t.Helper()
	resp, err := e.svc.GetTrigger(e.ctx, connect.NewRequest(&reliantv1.GetTriggerRequest{Id: id}))
	require.NoError(t, err)
	return resp.Msg.GetTrigger()
}

// seedUnfinished stores an activation the way the owner's existing `{}` ones
// are stored: enabled, with no params, written before the input was required.
func (e *needsSetupEnv) seedUnfinished(t *testing.T, enabled bool) *core.Trigger {
	t.Helper()
	e.seeded++
	created := e.create(t, e.activationDef(func(d *reliantv1.TriggerDefinition) {
		d.Name = fmt.Sprintf("seeded %d", e.seeded)
		d.Params = setupReposParam("acme/web")
	}))
	row, err := e.repo.GetTrigger(context.Background(), created.GetId())
	require.NoError(t, err)
	row.Params = map[string]any{}
	row.Enabled = enabled
	require.NoError(t, e.repo.UpdateTrigger(context.Background(), row))
	return row
}

// ---- create ----------------------------------------------------------------

func TestCreateActivationRefusesAMissingRequiredParam(t *testing.T) {
	env := setupNeedsSetup(t)

	_, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: env.activationDef(nil)}))

	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "repos is required")
	assert.Empty(t, env.storedTriggers(t), "nothing was written")
}

func TestCreateActivationEnforcesItemBounds(t *testing.T) {
	env := setupNeedsSetup(t)
	for name, tc := range map[string]struct {
		repos []any
		want  string
	}{
		"fewer than min_items": {[]any{}, "at least 1"},
		"more than max_items":  {[]any{"a/one", "a/two", "a/three"}, "at most 2"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
				Trigger: env.activationDef(func(d *reliantv1.TriggerDefinition) { d.Params = setupReposParam(tc.repos...) }),
			}))
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			assert.Contains(t, err.Error(), tc.want)
		})
	}
	assert.Empty(t, env.storedTriggers(t))
}

func TestCreateActivationAcceptsACompleteSetup(t *testing.T) {
	env := setupNeedsSetup(t)
	created := env.create(t, env.activationDef(func(d *reliantv1.TriggerDefinition) { d.Params = setupReposParam("acme/web") }))
	assert.NotEqual(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_NEEDS_SETUP, created.GetHealth().GetStatus())
}

// ---- update ----------------------------------------------------------------

func TestUpdateActivationCannotClearARequiredParam(t *testing.T) {
	env := setupNeedsSetup(t)
	created := env.create(t, env.activationDef(func(d *reliantv1.TriggerDefinition) { d.Params = setupReposParam("acme/web") }))

	_, err := env.svc.UpdateTrigger(env.ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{
		Id: created.GetId(), Trigger: env.activationDef(func(d *reliantv1.TriggerDefinition) { d.Params = nil }),
	}))

	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "repos is required")
	assert.Equal(t, []any{"acme/web"}, env.storedTriggers(t)[0].Params["repos"], "the stored setup is untouched")
}

// ---- enable: new -----------------------------------------------------------

func TestEnableWorkflowRefusesANewActivationWithoutTheRequiredParam(t *testing.T) {
	env := setupNeedsSetup(t)

	resp := env.enable(t, env.enableReq(nil))

	assert.False(t, resp.Enabled)
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_FAILED, outcomes(resp)["new-issue"])
	assert.Contains(t, resultFor(resp, "new-issue").Message, "repos is required")
	assert.Empty(t, env.storedTriggers(t))

	ok := env.enable(t, env.enableReq(setupReposParam("acme/web")))
	assert.True(t, ok.Enabled, "%v", ok.Results)
}

// ---- enable: existing / update --------------------------------------------

func TestEnableWorkflowRevalidatesAnExistingActivationAgainstRequiredParams(t *testing.T) {
	env := setupNeedsSetup(t)
	seeded := env.seedUnfinished(t, true)

	resp := env.enable(t, env.enableReq(nil))

	assert.False(t, resp.Enabled)
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_FAILED, outcomes(resp)["new-issue"])
	assert.Contains(t, resultFor(resp, "new-issue").Message, "repos is required")
	assert.Empty(t, env.storedTriggers(t)[0].Params, "refused before anything was written")
	assert.Equal(t, seeded.ID, env.storedTriggers(t)[0].ID)

	// Supplying it finishes the setup: the same call is "Finish setup".
	done := env.enable(t, env.enableReq(setupReposParam("acme/web")))
	require.True(t, done.Enabled, "%v", done.Results)
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_UPDATED, outcomes(done)["new-issue"])
	assert.Equal(t, []any{"acme/web"}, env.storedTriggers(t)[0].Params["repos"])
}

// ---- enable: resume --------------------------------------------------------

func TestEnableWorkflowResumeValidatesParams(t *testing.T) {
	env := setupNeedsSetup(t)
	env.seedUnfinished(t, false) // paused, and never finished

	resp := env.enable(t, env.enableReq(nil))
	assert.False(t, resp.Enabled, "a resume used to skip validation and turn it on")
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_FAILED, outcomes(resp)["new-issue"])
	assert.Contains(t, resultFor(resp, "new-issue").Message, "repos is required")
	assert.False(t, env.storedTriggers(t)[0].Enabled, "it stays paused")

	done := env.enable(t, env.enableReq(setupReposParam("acme/web")))
	require.True(t, done.Enabled, "%v", done.Results)
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_RESUMED, outcomes(done)["new-issue"])
	assert.True(t, env.storedTriggers(t)[0].Enabled)
}

func TestSetTriggerEnabledDoesNotResumeAnUnfinishedActivation(t *testing.T) {
	env := setupNeedsSetup(t)
	row := env.seedUnfinished(t, false)

	_, err := env.svc.SetTriggerEnabled(env.ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: row.ID, Enabled: true}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "needs setup: repos is required")

	// Pausing needs no setup.
	enabled := env.seedUnfinished(t, true)
	_, err = env.svc.SetTriggerEnabled(env.ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: enabled.ID, Enabled: false}))
	require.NoError(t, err)
}

// ---- run now ---------------------------------------------------------------

func TestRunNowOfAnUnfinishedActivationSaysWhatToFinish(t *testing.T) {
	env := setupNeedsSetup(t)
	row := env.seedUnfinished(t, true)

	_, err := env.svc.FireTrigger(env.ctx, connect.NewRequest(&reliantv1.FireTriggerRequest{Id: row.ID}))

	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "needs setup: repos is required")
	assert.Contains(t, err.Error(), "Finish setting up")
}

func TestRunNowOfACompleteActivationStillFires(t *testing.T) {
	env := setupNeedsSetup(t)
	created := env.create(t, env.activationDef(func(d *reliantv1.TriggerDefinition) { d.Params = setupReposParam("acme/web") }))
	_, err := env.svc.FireTrigger(env.ctx, connect.NewRequest(&reliantv1.FireTriggerRequest{Id: created.GetId()}))
	require.NoError(t, err)
}

// ---- health ----------------------------------------------------------------

func TestHealthIsNeedsSetupForAnActivationMissingARequiredParam(t *testing.T) {
	env := setupNeedsSetup(t)
	row := env.seedUnfinished(t, true)

	got := env.get(t, row.ID)
	assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_NEEDS_SETUP, got.GetHealth().GetStatus())
	assert.Equal(t, "needs setup: repos is required", got.GetHealth().GetLastFailureDetail())
	require.Len(t, got.GetHealth().GetSetupIssues(), 1)
	assert.Equal(t, "repos", got.GetHealth().GetSetupIssues()[0].GetInput())

	listed, err := env.svc.ListTriggers(env.ctx, connect.NewRequest(&reliantv1.ListTriggersRequest{}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.GetTriggers(), 1)
	assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_NEEDS_SETUP, listed.Msg.GetTriggers()[0].GetHealth().GetStatus(), "the list reads it the same way")

	// Paused or running makes no difference: resuming would not make it fire.
	paused := env.seedUnfinished(t, false)
	assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_NEEDS_SETUP, env.get(t, paused.ID).GetHealth().GetStatus())
}

// A workflow edit that adds a required input needs no data migration: the
// activation is NEEDS_SETUP the next time anyone reads it.
func TestHealthTurnsNeedsSetupWhenTheWorkflowGrowsARequiredInput(t *testing.T) {
	env := setupNeedsSetup(t)
	env.saveWorkflow(t, "triage", `name: triage
inputs:
  team: {type: string, default: platform}
  issue_number: {type: integer, default: 0}
triggers:
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
`)
	created := env.create(t, env.activationDef(nil))
	assert.NotEqual(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_NEEDS_SETUP, env.get(t, created.GetId()).GetHealth().GetStatus())

	env.saveWorkflow(t, "triage", requiredReposYAML)
	assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_NEEDS_SETUP, env.get(t, created.GetId()).GetHealth().GetStatus())
}

func TestHealthBrokenOutranksNeedsSetup(t *testing.T) {
	env := setupNeedsSetup(t)
	row := env.seedUnfinished(t, true)
	// The workflow stops declaring the trigger: nothing can be set up.
	env.saveWorkflow(t, "triage", `name: triage
inputs:
  repos: {type: github.repository, multi: true, required: true}
triggers:
  - name: other
    schedule: {cron: "0 9 * * *"}
    prompt: x
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`)
	assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_BROKEN, env.get(t, row.ID).GetHealth().GetStatus())
}

// ---- reachability ----------------------------------------------------------

type fakeOptions struct {
	checks      []ValueCheck
	unreachable []string
	warnings    []string
	err         error
}

func (f *fakeOptions) CheckValues(_ context.Context, c ValueCheck) (ValueCheckResult, error) {
	f.checks = append(f.checks, c)
	if f.err != nil {
		return ValueCheckResult{}, f.err
	}
	return ValueCheckResult{Unreachable: f.unreachable, Warnings: f.warnings}, nil
}

func (e *needsSetupEnv) withOptions(t *testing.T, o OptionsChecker) {
	t.Helper()
	e.configure(t, func(d *Deps) { d.Options = o })
}

func TestEnableFailsOnADefinitelyUnreachableValue(t *testing.T) {
	env := setupNeedsSetup(t)
	opts := &fakeOptions{unreachable: []string{"acme/gone"}}
	env.withOptions(t, opts)

	resp := env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		// The typed values are github's; the trigger listens through "test".
		r.ConnectionIds = map[string]string{"test": env.connID, "github": "gh-conn"}
		r.Params = setupReposParam("acme/web", "acme/gone")
	}))

	assert.False(t, resp.Enabled)
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_FAILED, outcomes(resp)["new-issue"])
	assert.Contains(t, resultFor(resp, "new-issue").Message, "repos: acme/gone not reachable")
	assert.Empty(t, env.storedTriggers(t), "nothing is written for an unreachable value")

	require.Len(t, opts.checks, 1)
	check := opts.checks[0]
	assert.Equal(t, "github.repository", check.Type)
	assert.Equal(t, env.userID, check.UserID)
	assert.Equal(t, "gh-conn", check.ConnectionID, "the connection the request names for the type's integration")
	assert.Equal(t, core.ActingIdentityOwner, check.ActingIdentity)
	assert.Equal(t, []string{"acme/web", "acme/gone"}, check.Values)
}

func TestEnableWarnsButProceedsOnAnUpstreamError(t *testing.T) {
	env := setupNeedsSetup(t)
	env.withOptions(t, &fakeOptions{err: errors.New("github is down")})

	resp := env.enable(t, env.enableReq(setupReposParam("acme/web")))

	require.True(t, resp.Enabled, "%v", resp.Results)
	require.Len(t, resultFor(resp, "new-issue").Warnings, 1)
	assert.Contains(t, resultFor(resp, "new-issue").Warnings[0], "github is down")
	assert.Len(t, env.storedTriggers(t), 1, "the activation is saved anyway")
}

// "The bot cannot write" is the checker's finding, not a refusal: review-only
// use is legitimate, so it reaches the person as a warning.
func TestEnableWarnsWhenTheBotCannotWrite(t *testing.T) {
	env := setupNeedsSetup(t)
	env.withOptions(t, &fakeOptions{warnings: []string{"The Reliant bot cannot write to acme/web; reviews will be skipped."}})

	resp := env.enable(t, env.enableReq(setupReposParam("acme/web")))

	require.True(t, resp.Enabled, "%v", resp.Results)
	assert.Equal(t, []string{"The Reliant bot cannot write to acme/web; reviews will be skipped."}, resultFor(resp, "new-issue").Warnings)
}

func TestEnableChecksReachabilityOfAResumedAndAnUpdatedActivationToo(t *testing.T) {
	env := setupNeedsSetup(t)
	opts := &fakeOptions{}
	env.withOptions(t, opts)
	require.True(t, env.enable(t, env.enableReq(setupReposParam("acme/web"))).Enabled)
	opts.checks = nil

	opts.unreachable = []string{"acme/other"}
	resp := env.enable(t, env.enableReq(setupReposParam("acme/other")))
	assert.False(t, resp.Enabled, "an update to an unreachable value is refused")
	assert.Equal(t, []any{"acme/web"}, env.storedTriggers(t)[0].Params["repos"], "the previous setting stays")

	// Nothing changed and it is on: nothing to check, nothing to ask GitHub.
	opts.checks, opts.unreachable = nil, nil
	same := env.enable(t, env.enableReq(setupReposParam("acme/web")))
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_ALREADY_ENABLED, outcomes(same)["new-issue"])
	assert.Empty(t, opts.checks)
}

func TestEnableWithoutAnOptionsCheckerChecksShapesOnly(t *testing.T) {
	env := setupNeedsSetup(t)
	resp := env.enable(t, env.enableReq(setupReposParam("acme/web")))
	require.True(t, resp.Enabled)
	assert.Empty(t, resultFor(resp, "new-issue").Warnings)
}

// ---- owner identity --------------------------------------------------------

const ownerGatedYAML = `name: responder
triggers:
  - name: on-comment
    integration: {integration: test, events: [issues.opened]}
    filter: "trigger.sender.verified && trigger.sender.id == trigger.activation.owner.github_id"
    prompt: "Respond."
  - name: on-open
    integration: {integration: test, events: [issues.closed]}
    prompt: "Triage."
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

func gatedDeclaration(t *testing.T, name string) *reliantv1.WorkflowTrigger {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(ownerGatedYAML))
	require.NoError(t, err)
	wt := triggers.FindDeclaredTrigger(wf, name)
	require.NotNil(t, wt)
	return wt
}

// A trigger whose filter compares the sender with the owner's GitHub id would
// enable and then skip every event if the owner has no GitHub identity. The
// enable says so, for the trigger that needs it and only that one.
func TestEnableWarnsWhenAnOwnerGatedFilterHasNoOwnerIdentity(t *testing.T) {
	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "responder", ownerGatedYAML)
	conn := env.createConnection(t, env.userID, "test", "acct-1", core.ConnectionStatusActive)

	resp := env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		r.Workflow = "responder"
		r.ConnectionIds = map[string]string{"test": conn.ID}
	}))
	require.True(t, resp.Enabled, "%v", resp.Results)
	gated := resultFor(resp, "on-comment").Warnings
	require.Len(t, gated, 1, "the gated trigger warns")
	assert.Contains(t, gated[0], "your GitHub identity")
	assert.Contains(t, gated[0], "on-comment")
	assert.Empty(t, resultFor(resp, "on-open").Warnings, "a trigger that never asks who the owner is does not")
}

// With the owner's GitHub identity linked, an activation that names no
// connection (every hosted GitHub one) has nothing to warn about; unlinked, it
// does. The identity is read when asked, not stored on the activation.
func TestOwnerIdentityWarningFollowsTheLinkedIdentity(t *testing.T) {
	env, _ := setupInboundTriggerTest(t)
	row := &core.Trigger{ID: "t1", UserID: env.userID}
	gated, plain := gatedDeclaration(t, "on-comment"), gatedDeclaration(t, "on-open")

	assert.Len(t, env.svc.ownerIdentityWarnings(env.ctx, gated, row), 1, "no identity linked yet")
	assert.Empty(t, env.svc.ownerIdentityWarnings(env.ctx, plain, row))

	require.NoError(t, env.repo.ReplaceIntegrationAccess(env.ctx, env.userID, "github", "225976313", time.Now(),
		[]core.IntegrationAccessGrant{{AccountKey: "1", ResourceKey: "1"}}))
	assert.Empty(t, env.svc.ownerIdentityWarnings(env.ctx, gated, row), "linked: the same activation now matches")

	require.NoError(t, env.repo.ReplaceIntegrationAccess(env.ctx, env.userID, "github", "", time.Now().Add(time.Minute), nil))
	assert.Len(t, env.svc.ownerIdentityWarnings(env.ctx, gated, row), 1, "unlinked again (the token went away): warns again")
}
