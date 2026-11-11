// Copyright (c) 2025 Reliant Labs
package workflowevent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/triggers/runevents"
)

// liveIn is a membership of the orgs it names only.
type liveIn map[string]bool

func (m liveIn) IsMember(_ context.Context, _, orgID string) (bool, bool, error) {
	return m[orgID], true, nil
}

// A workflow_event trigger whose creator left its org: the first match
// records one failure and pauses the trigger, which matches nothing after.
func TestDispatch_PausesATriggerWhoseCreatorLeftItsOrg(t *testing.T) {
	f := newFixture(t)
	f.dispatcher.WithAutoPause(triggers.NewAutoPause(f.repo, nil))
	config, err := json.Marshal(Source{Workflows: []string{"code-review"}, Outcomes: []string{"finished"}})
	require.NoError(t, err)
	acme, now := "acme", time.Now().UTC()
	tr := &core.Trigger{
		ID: uuid.NewString(), UserID: f.userID, ProjectID: f.projectID, OrgID: &acme, Name: "on-review-done",
		Kind: core.TriggerKindWorkflowEvent, Enabled: true, Workflow: "builtin://agent",
		Params:  map[string]any{"model": map[string]any{"id": "mock"}},
		Message: "react to the source run", DaemonID: f.daemonID, Config: config,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, f.repo.CreateTrigger(context.Background(), tr))
	ctx := authz.WithMembership(context.Background(), liveIn{"home": true})

	results, err := f.dispatcher.Dispatch(ctx, f.finish(f.humanRun("code-review"), core.RunEventFinished))
	require.NoError(t, err)
	res := resultFor(t, results, tr.ID)
	assert.Equal(t, core.TriggerEventFailed, res.Outcome)
	assert.Contains(t, res.Detail, "no longer a member of its organization")
	stored, err := f.repo.GetTrigger(ctx, tr.ID)
	require.NoError(t, err)
	assert.False(t, stored.Enabled)
	assert.Equal(t, core.TriggerPausedCreatorLeftOrg, stored.PausedReason)

	// Paused, it no longer listens: the next finished run is not even
	// recorded as an event for it.
	chatID := f.humanRun("code-review")
	created, err := runevents.EmitTerminal(ctx, f.repo, runevents.Terminal{
		ChatID: chatID, WorkflowID: chatID, WorkflowName: "code-review", RunID: "run-" + uuid.NewString(),
		Outcome: core.RunEventFinished, Summary: "all done",
	}, time.Now().UTC())
	require.NoError(t, err)
	assert.False(t, created, "a paused trigger matches nothing")
	assert.Zero(t, f.starter.count())
}
