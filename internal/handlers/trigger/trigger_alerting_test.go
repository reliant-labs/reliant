// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"errors"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers"
)

const alertTriageYAML = `name: oncall
triggers:
  - name: alerts
    webhook: {source: alerting}
    prompt: "Triage."
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

// fakeAlerting is the control plane's side as the handler sees it: it records
// every provision and retirement, and can be told to refuse.
type fakeAlerting struct {
	mu          sync.Mutex
	provisioned []string
	retired     []string
	err         error
}

func (f *fakeAlerting) Manages(t *core.Trigger) bool { return triggers.IsAlertingWebhook(t) }

func (f *fakeAlerting) Provision(_ context.Context, t *core.Trigger) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.provisioned = append(f.provisioned, t.ID)
	return nil
}

func (f *fakeAlerting) Retire(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retired = append(f.retired, id)
	return nil
}

func setupAlertingTest(t *testing.T) (*triggerTestEnv, *fakeAlerting) {
	t.Helper()
	env, _ := setupInboundTriggerTest(t)
	fa := &fakeAlerting{}
	env.configure(t, func(d *Deps) { d.Alerting = fa })
	env.saveWorkflow(t, "oncall", alertTriageYAML)
	return env, fa
}

func (e *triggerTestEnv) enableOncall(t *testing.T) *reliantv1.EnableWorkflowResponse {
	t.Helper()
	return e.enable(t, &reliantv1.EnableWorkflowRequest{Workflow: "oncall", ProjectId: e.projectID, DaemonId: e.daemonID})
}

// One click: Enable provisions the receiver and returns no token to show.
func TestEnableAlertingWorkflowProvisionsAndShowsNoToken(t *testing.T) {
	testkit.Slow(t, "a DB-backed trigger handler test with many round trips to pgtest's Postgres")
	t.Parallel()

	env, fa := setupAlertingTest(t)
	resp := env.enableOncall(t)
	require.True(t, resp.Enabled, "%v", resp.Results)
	r := resp.Results[0]
	assert.Nil(t, r.Webhook, "a managed webhook's token never reaches the caller")
	assert.True(t, r.Activation.GetWebhookManaged())
	assert.Equal(t, "alerting", r.Activation.GetWebhook().GetSource())
	assert.Equal(t, []string{r.Activation.GetId()}, fa.provisioned)
}

// The control plane refusing (no org) refuses the enable and leaves nothing.
func TestEnableAlertingWorkflowIsRefusedWhenAlertsCannotBeConnected(t *testing.T) {
	t.Parallel()

	env, fa := setupAlertingTest(t)
	// What triggers.Alerting.Provision returns for an org-less owner.
	fa.err = svcerr.WithCause(svcerr.FailedPrecondition("on-call receives your organization's alerts, and your account belongs to no organization yet"),
		&controlplane.AutopilotError{Code: "failed_precondition", Reason: controlplane.ReasonAutopilotNoOrg})
	resp := env.enableOncall(t)
	require.False(t, resp.Enabled)
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_FAILED, resp.Results[0].Outcome)
	assert.Contains(t, resp.Results[0].Message, "no organization")
	assert.Empty(t, env.storedTriggers(t), "a trigger nothing delivers to is not stored")
}

func TestPauseResumeDeleteFollowTheReceiver(t *testing.T) {
	t.Parallel()

	env, fa := setupAlertingTest(t)
	id := env.enableOncall(t).Results[0].Activation.GetId()

	_, err := env.svc.SetTriggerEnabled(env.ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: id, Enabled: false}))
	require.NoError(t, err)
	assert.Equal(t, []string{id}, fa.retired, "pausing disconnects")

	_, err = env.svc.SetTriggerEnabled(env.ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: id, Enabled: true}))
	require.NoError(t, err)
	assert.Equal(t, []string{id, id}, fa.provisioned, "resuming re-provisions with a fresh token")

	// A resume that cannot connect is refused and the row put back.
	fa.err = svcerr.WithCause(svcerr.Unavailable("could not connect your organization's alerts right now; try again"), errors.New("control plane down"))
	_, err = env.svc.SetTriggerEnabled(env.ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: id, Enabled: false}))
	require.NoError(t, err)
	_, err = env.svc.SetTriggerEnabled(env.ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: id, Enabled: true}))
	require.Equal(t, connect.CodeUnavailable, svcerr.Code(err), "%v", err)
	stored, err := env.repo.GetTrigger(context.Background(), id)
	require.NoError(t, err)
	assert.False(t, stored.Enabled, "an enabled trigger nothing delivers to looks working and is not")
	fa.err = nil

	_, err = env.svc.DeleteTrigger(env.ctx, connect.NewRequest(&reliantv1.DeleteTriggerRequest{Id: id}))
	require.NoError(t, err)
	assert.Equal(t, id, fa.retired[len(fa.retired)-1], "deleting disconnects")
}

// Rotating a managed webhook re-provisions; the new token is not returned.
func TestRotateManagedWebhookReprovisionsWithoutShowingTheToken(t *testing.T) {
	t.Parallel()

	env, fa := setupAlertingTest(t)
	id := env.enableOncall(t).Results[0].Activation.GetId()
	before, err := env.repo.GetTriggerWebhookCredentials(context.Background(), id)
	require.NoError(t, err)

	resp, err := env.svc.RotateWebhookToken(env.ctx, connect.NewRequest(&reliantv1.RotateWebhookTokenRequest{Id: id}))
	require.NoError(t, err)
	assert.Nil(t, resp.Msg.Webhook)
	assert.Equal(t, []string{id, id}, fa.provisioned)

	// Paused: no receiver to give it to, so the token is replaced in place.
	_, err = env.svc.SetTriggerEnabled(env.ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: id, Enabled: false}))
	require.NoError(t, err)
	resp, err = env.svc.RotateWebhookToken(env.ctx, connect.NewRequest(&reliantv1.RotateWebhookTokenRequest{Id: id}))
	require.NoError(t, err)
	assert.Nil(t, resp.Msg.Webhook)
	assert.Len(t, fa.provisioned, 2, "a paused trigger is not provisioned")
	after, err := env.repo.GetTriggerWebhookCredentials(context.Background(), id)
	require.NoError(t, err)
	assert.NotEqual(t, before.TokenHash, after.TokenHash)
}

// Without a control plane (no Alerting dep) the same declaration is a plain
// webhook: the token is shown once, as for any webhook.
func TestAlertingWebhookIsAPlainWebhookWithoutAControlPlane(t *testing.T) {
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "oncall", alertTriageYAML)
	resp := env.enableOncall(t)
	require.True(t, resp.Enabled, "%v", resp.Results)
	assert.NotEmpty(t, resp.Results[0].Webhook.GetToken())
	assert.False(t, resp.Results[0].Activation.GetWebhookManaged())
}

// An ad hoc webhook trigger cannot name a source: only declared triggers are
// reconciled against the control plane.
func TestAdHocWebhookCannotNameASource(t *testing.T) {
	t.Parallel()

	env, _ := setupAlertingTest(t)
	_, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
		Trigger: webhookDefinition(env, func(d *reliantv1.TriggerDefinition) {
			d.Source = &reliantv1.TriggerDefinition_Webhook{Webhook: &reliantv1.WebhookSource{Source: "alerting"}}
		}),
	}))
	require.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err), "%v", err)
}
