// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
)

// A trigger with no connection_id (every hosted GitHub trigger: the credential
// is the owner's delegated token, not a saved connection) still knows who its
// owner is on GitHub: the id the access refresher read off the owner's own
// token. Sender-gated filters ("only me, or these people") depend on it.
func TestOwnerGitHubIDComesFromTheLinkedIdentityWhenThereIsNoConnection(t *testing.T) {
	const (
		me       = "225976313"
		stranger = "9999"
	)
	github := func(id string) *core.TriggerSender {
		return &core.TriggerSender{Kind: core.TriggerSenderKindGitHub, ID: id, Verified: true}
	}
	name := "new-issue"
	newActivation := func(repo *fakeRepo, id, userID string) *core.Trigger {
		a := repoActivation(id, nil)
		a.UserID = userID
		a.WorkflowTrigger = &name
		a.ConnectionID = nil
		repo.triggers[a.ID] = a
		return a
	}

	repo := newFakeRepo()
	repo.subjects["owner-linked"] = fakeSubject{id: me, refreshedAt: time.Now()}
	linked := newActivation(repo, "linked", "owner-linked")
	unlinked := newActivation(repo, "unlinked", "owner-unlinked")

	intake := NewIntake(repo, &recordingStarter{}, "q").WithWorkflows(fakeWorkflows{yaml: map[string]string{"triage": trustedWorkflow}})
	n := 0
	accept := func(a *core.Trigger, s *core.TriggerSender) (core.TriggerEventOutcome, string) {
		t.Helper()
		n++
		res, err := intake.Accept(context.Background(), a, InboundEvent{
			Kind: core.TriggerEventKindIntegration, DedupeKey: fmt.Sprintf("%s:%d", a.ID, n),
			OccurredAt: time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC), Payload: issueIn("acme/web", 1), Sender: s,
		}, AcceptOptions{})
		require.NoError(t, err)
		require.NotEqual(t, core.TriggerEventFailed, res.Outcome, "the filter must evaluate: %s", res.Detail)
		return res.Outcome, res.Detail
	}

	outcome, _ := accept(linked, github(me))
	assert.Equal(t, core.TriggerEventPending, outcome, "the owner, by their linked GitHub identity, with no connection")
	outcome, detail := accept(linked, github(stranger))
	assert.Equal(t, core.TriggerEventSkipped, outcome, "another id does not match")
	assert.NotContains(t, detail, "no linked GitHub identity", "the owner has one; this is an ordinary non-match")

	outcome, detail = accept(unlinked, github(me))
	assert.Equal(t, core.TriggerEventSkipped, outcome, "an owner with no resolvable identity fails closed")
	assert.Contains(t, detail, "owner has no linked GitHub identity", "and the recorded reason says why, not just that the filter failed")
	assert.Contains(t, detail, "trigger.activation.owner.github_id")
}

func TestOwnerOfPrecedenceAndFailureModes(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	repo.subjects["u"] = fakeSubject{id: "linked-id", refreshedAt: time.Now()}
	repo.connections["conn-gh"] = &core.Connection{ID: "conn-gh", UserID: "u", IntegrationID: "github", SenderID: strPtr("connection-id")}
	repo.connections["conn-slack"] = &core.Connection{ID: "conn-slack", UserID: "u", IntegrationID: "slack", SenderID: strPtr("U1")}
	repo.connections["conn-nosender"] = &core.Connection{ID: "conn-nosender", UserID: "u", IntegrationID: "github"}

	for name, tc := range map[string]struct {
		trigger *core.Trigger
		want    string
	}{
		"no connection reads the linked identity":    {&core.Trigger{UserID: "u"}, "linked-id"},
		"a blank connection id is no connection":     {&core.Trigger{UserID: "u", ConnectionID: strPtr("")}, "linked-id"},
		"a named GitHub connection decides":          {&core.Trigger{UserID: "u", ConnectionID: strPtr("conn-gh")}, "connection-id"},
		"a named non-GitHub connection never widens": {&core.Trigger{UserID: "u", ConnectionID: strPtr("conn-slack")}, ""},
		"a named connection without a sender id":     {&core.Trigger{UserID: "u", ConnectionID: strPtr("conn-nosender")}, ""},
		"a connection that cannot be read":           {&core.Trigger{UserID: "u", ConnectionID: strPtr("missing")}, ""},
		"another user's identity is not borrowed":    {&core.Trigger{UserID: "other"}, ""},
		"a trigger with no owner":                    {&core.Trigger{}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, OwnerOf(ctx, repo, tc.trigger).GitHubID)
		})
	}

	t.Run("a stale snapshot is not trusted", func(t *testing.T) {
		stale := newFakeRepo()
		stale.subjects["u"] = fakeSubject{id: "old", refreshedAt: time.Now().Add(-2 * core.IntegrationAccessFreshness)}
		assert.Empty(t, OwnerOf(ctx, stale, &core.Trigger{UserID: "u"}).GitHubID)
	})
	t.Run("a repository that cannot say yields no id", func(t *testing.T) {
		assert.Empty(t, OwnerOf(ctx, struct{}{}, &core.Trigger{UserID: "u"}).GitHubID)
		assert.Empty(t, OwnerOf(ctx, nil, &core.Trigger{UserID: "u"}).GitHubID)
	})
	t.Run("a read error never widens", func(t *testing.T) {
		assert.Empty(t, OwnerOf(ctx, failingSubjects{}, &core.Trigger{UserID: "u"}).GitHubID)
	})
}

type failingSubjects struct{}

func (failingSubjects) GetIntegrationAccessSubject(context.Context, string, string, time.Time) (string, error) {
	return "", errors.New("db down")
}

func TestOwnerIdentityGapOnlyForFiltersThatReadTheOwner(t *testing.T) {
	known := triggerspec.Scope{Activation: triggerspec.ActivationRoot("a", triggerspec.Owner{UserID: "u", GitHubID: "1"}, nil)}
	unknown := triggerspec.Scope{Activation: triggerspec.ActivationRoot("a", triggerspec.Owner{UserID: "u"}, nil)}
	gated := "trigger.sender.id == trigger.activation.owner.github_id"

	assert.Empty(t, OwnerIdentityGap(gated, known), "the id is known")
	assert.Empty(t, OwnerIdentityGap("trigger.payload.data.label == 'x'", unknown), "the filter never asks who the owner is")
	assert.Contains(t, OwnerIdentityGap(gated, unknown), "owner has no linked GitHub identity")
}
