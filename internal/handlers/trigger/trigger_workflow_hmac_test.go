// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// signedWorkflowYAML declares a webhook that verifies signatures beside one
// that does not, so a secret can be given to the wrong one.
const signedWorkflowYAML = `name: signed
triggers:
  - name: signed
    webhook:
      hmac: {header: X-Hub-Signature-256, prefix: "sha256="}
    prompt: "Handle the delivery."
  - name: plain
    webhook: {}
    prompt: "Handle it too."
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

func hmacOverride(trigger, secret string) *reliantv1.WorkflowTriggerOverride {
	return &reliantv1.WorkflowTriggerOverride{Trigger: trigger, WebhookHmacSecret: &secret}
}

// A declared hmac: webhook is enabled with the secret given as its override:
// sealed for the owner, never returned. Without one the enable is refused,
// naming the flag that gives it (north-star AW-40).
func TestEnableWorkflowSealsADeclaredWebhooksSigningSecret(t *testing.T) {
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "signed", signedWorkflowYAML)
	signed := func(r *reliantv1.EnableWorkflowRequest) { r.Workflow = "signed" }

	refused := env.enable(t, env.enableRequest(signed))
	require.False(t, refused.Enabled)
	assert.Contains(t, resultFor(refused, "signed").Message, "webhook_hmac_secret is required to verify signatures")
	assert.Contains(t, resultFor(refused, "signed").Message, "--hmac-secret-file signed=<file>")
	assert.Empty(t, env.storedTriggers(t))

	resp := env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		signed(r)
		r.Overrides = []*reliantv1.WorkflowTriggerOverride{hmacOverride("signed", "s3cret")}
	}))
	require.True(t, resp.Enabled, "%v", resp.Results)
	id := resultFor(resp, "signed").Activation.GetId()
	assert.NotEmpty(t, resultFor(resp, "signed").Webhook.GetToken(), "the token still comes back once: HMAC adds a way in")
	stored, err := store.GetTriggerWebhookCredentials(context.Background(), env.repo.ORM(), id)
	require.NoError(t, err)
	assert.Equal(t, "sealed:"+env.userID+":"+string(triggers.WebhookSecretAAD(id))+":s3cret", string(stored.SecretSealed))
	for _, r := range resp.Results {
		assert.NotContains(t, r.String(), "s3cret", "a result never carries the secret")
	}

	// Enabling again with nothing changed keeps the secret and is a no-op.
	again := env.enable(t, env.enableRequest(signed))
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_ALREADY_ENABLED, outcomes(again)["signed"])

	// A new secret on an existing activation is a change, and replaces it.
	rotated := env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		signed(r)
		r.Overrides = []*reliantv1.WorkflowTriggerOverride{hmacOverride("signed", "n3w")}
	}))
	require.True(t, rotated.Enabled, "%v", rotated.Results)
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_UPDATED, outcomes(rotated)["signed"])
	replaced, err := store.GetTriggerWebhookCredentials(context.Background(), env.repo.ORM(), id)
	require.NoError(t, err)
	assert.Equal(t, "sealed:"+env.userID+":"+string(triggers.WebhookSecretAAD(id))+":n3w", string(replaced.SecretSealed))
}

// A secret for a trigger that verifies no signature would be sealed and
// never read: the enable refuses it, and writes nothing.
func TestEnableWorkflowRefusesASigningSecretForAnUnsignedTrigger(t *testing.T) {
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "signed", signedWorkflowYAML)
	resp := env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		r.Workflow = "signed"
		r.Overrides = []*reliantv1.WorkflowTriggerOverride{hmacOverride("signed", "s3cret"), hmacOverride("plain", "s3cret")}
	}))
	require.False(t, resp.Enabled)
	assert.Contains(t, resultFor(resp, "plain").Message, `trigger "plain" does not verify signatures`)
	assert.Empty(t, env.storedTriggers(t))
}

// An enable that replaces a secret and then fails on another trigger puts
// the old secret back with the rest of the activation.
func TestEnableWorkflowUndoPutsBackAReplacedSigningSecret(t *testing.T) {
	t.Parallel()

	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "signed", `name: signed
triggers:
  - name: signed
    webhook:
      hmac: {}
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
	first := env.enable(t, env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		r.Workflow = "signed"
		r.Triggers = []string{"signed"}
		r.Overrides = []*reliantv1.WorkflowTriggerOverride{hmacOverride("signed", "old")}
	}))
	require.True(t, first.Enabled, "%v", first.Results)
	id := resultFor(first, "signed").Activation.GetId()
	before, err := store.GetTriggerWebhookCredentials(context.Background(), env.repo.ORM(), id)
	require.NoError(t, err)

	env.backend.syncErr = errors.New("temporal is down")
	resp, err := env.svc.EnableWorkflow(env.ctx, connect.NewRequest(env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		r.Workflow = "signed"
		r.Overrides = []*reliantv1.WorkflowTriggerOverride{hmacOverride("signed", "new")}
	})))
	require.NoError(t, err)
	require.False(t, resp.Msg.Enabled)
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_NOT_ENABLED, outcomes(resp.Msg)["signed"])
	after, err := store.GetTriggerWebhookCredentials(context.Background(), env.repo.ORM(), id)
	require.NoError(t, err)
	assert.Equal(t, before.SecretSealed, after.SecretSealed, "the undone enable keeps the old secret")
}
