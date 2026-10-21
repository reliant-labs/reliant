// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The production shape: the real repository, an activation with NO
// connection_id (the hosted prod table has no connections at all), and an
// owner whose GitHub id exists only as the subject of their access snapshot.
// A `/autopilot re-review`-style comment by the owner must pass the
// sender-gated filter; by anyone else, or by an owner with no identity, it must
// not, and the recorded reason must say which.
func TestOwnerGatedFilterAgainstTheRealRepository(t *testing.T) {
	f := newFireFixture(t, nil)
	ctx := context.Background()
	const ownerGitHubID = "225976313"

	name := "new-issue"
	f.trigger.Kind = core.TriggerKindIntegration
	f.trigger.Config = json.RawMessage(`{"integration":"github","events":["issues.opened"]}`)
	f.trigger.WorkflowTrigger = &name
	f.trigger.Workflow = "triage"
	f.trigger.ConnectionID = nil
	require.NoError(t, f.repo.UpdateTrigger(ctx, f.trigger))

	intake := NewIntake(f.repo, &recordingStarter{}, "q").
		WithWorkflows(fakeWorkflows{yaml: map[string]string{"triage": trustedWorkflow}})
	n := 0
	accept := func(senderID string, verified bool) *AcceptResult {
		t.Helper()
		n++
		res, err := intake.Accept(ctx, f.trigger, InboundEvent{
			Kind: core.TriggerEventKindIntegration, DedupeKey: "d" + string(rune('a'+n)),
			OccurredAt: time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC), Payload: issueIn("acme/web", 1),
			Sender: &core.TriggerSender{Kind: core.TriggerSenderKindGitHub, ID: senderID, Verified: verified},
		}, AcceptOptions{})
		require.NoError(t, err)
		require.NotEqual(t, core.TriggerEventFailed, res.Outcome, "the filter must evaluate: %s", res.Detail)
		return res
	}

	// Before the owner's GitHub identity is known: fail closed, legibly.
	res := accept(ownerGitHubID, true)
	assert.Equal(t, core.TriggerEventSkipped, res.Outcome)
	assert.Contains(t, res.Detail, "owner has no linked GitHub identity")

	// The refresher records who the owner is on GitHub (GET /user).
	require.NoError(t, f.repo.ReplaceIntegrationAccess(ctx, f.trigger.UserID, "github", ownerGitHubID, time.Now(),
		[]core.IntegrationAccessGrant{{AccountKey: "100", ResourceKey: "1"}}))

	assert.Equal(t, core.TriggerEventPending, accept(ownerGitHubID, true).Outcome, "the owner passes with no connection")
	other := accept("9999", true)
	assert.Equal(t, core.TriggerEventSkipped, other.Outcome, "another id does not")
	assert.NotContains(t, other.Detail, "no linked GitHub identity")
	assert.Equal(t, core.TriggerEventSkipped, accept(ownerGitHubID, false).Outcome, "an unverified sender never does")

	// The identity is read at every event: when the owner's token goes away the
	// grants and the identity are cleared together, and the owner stops matching.
	require.NoError(t, f.repo.ReplaceIntegrationAccess(ctx, f.trigger.UserID, "github", "", time.Now().Add(time.Minute), nil))
	res = accept(ownerGitHubID, true)
	assert.Equal(t, core.TriggerEventSkipped, res.Outcome)
	assert.Contains(t, res.Detail, "owner has no linked GitHub identity")
}
