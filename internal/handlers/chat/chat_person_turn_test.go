// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/reliant-labs/reliant/internal/runs"
)

// A person's send into a chat a trigger or an agent launched makes it
// attended from then on (org-provider-accounts §11.1, I2); nothing else does.
func TestRecordPersonTurn(t *testing.T) {
	person := auth.WithCredential(context.Background(), auth.CredentialSession)
	cli := auth.WithCredential(context.Background(), auth.CredentialAccessToken)
	triggered := &db.Chat{ID: "c1", UserID: "alice", LaunchKind: string(core.TriggerEventKindSchedule), TriggerID: ptr.Of("t1")}
	spawned := &db.Chat{ID: "c1", UserID: "alice", LaunchKind: string(core.TriggerEventKindAgentStartRun)}
	started := &db.Chat{ID: "c1", UserID: "alice", LaunchKind: string(core.TriggerEventKindChatStart)}

	for _, tc := range []struct {
		name   string
		ctx    context.Context
		sender string
		chat   *db.Chat
		want   bool
	}{
		{"the owner's send into a trigger's chat", person, "alice", triggered, true},
		{"the owner's send into an agent-started chat", person, "alice", spawned, true},
		{"a chat a person started needs no mark", person, "alice", started, false},
		{"a credential software can hold is not a person", cli, "alice", triggered, false},
		{"a sender other than the owner is not the person whose accounts it spends", person, "bob", triggered, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &MockRepository{}
			var recorded []string
			repo.RecordChatPersonTurnFunc = func(_ context.Context, chatID, userID string) error {
				recorded = append(recorded, chatID+"/"+userID)
				return nil
			}
			require.NoError(t, (&Service{deps: Deps{Repo: repo}}).recordPersonTurn(tc.ctx, tc.sender, tc.chat))
			if tc.want {
				assert.Equal(t, []string{"c1/" + tc.sender}, recorded)
			} else {
				assert.Empty(t, recorded)
			}
		})
	}

	t.Run("a failed write fails the transaction that saves the message", func(t *testing.T) {
		repo := &MockRepository{RecordChatPersonTurnFunc: func(context.Context, string, string) error { return errors.New("db down") }}
		assert.Error(t, (&Service{deps: Deps{Repo: repo}}).recordPersonTurn(person, "alice", triggered))
	})
}

// launchByTrigger makes chatID the chat a schedule trigger's fire launched.
func launchByTrigger(t *testing.T, ctx context.Context, repo *db.Repo, chatID string) {
	t.Helper()
	at := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	triggerID := "trg-" + chatID
	require.NoError(t, repo.CreateTrigger(ctx, &core.Trigger{
		ID: triggerID, UserID: "test-user", ProjectID: mustChatProject(t, ctx, repo, chatID),
		Name: "nightly", Kind: core.TriggerKindSchedule, Enabled: true, Workflow: "builtin://agent",
		Config: []byte(`{"interval":"1h"}`), CreatedAt: at, UpdatedAt: at,
	}))
	created, err := repo.CreateTriggerEvent(ctx, &core.TriggerEvent{
		ID: "evt-" + chatID, TriggerID: &triggerID, UserID: "test-user",
		Kind: core.TriggerEventKindSchedule, DedupeKey: "fire-" + chatID, OccurredAt: at,
		Payload: map[string]any{"trigger_name": "nightly"},
		Outcome: core.TriggerEventLaunched, ChatID: &chatID, CreatedAt: at,
	})
	require.NoError(t, err)
	require.True(t, created)
}

// An accepted send by the owner, signed in as a person, records their turn in
// the trigger's chat; the same send on a credential software can hold does
// not.
func TestSendMessage_PersonTurn(t *testing.T) {
	testkit.Slow(t, "a DB-backed chat handler test with many round trips to pgtest's Postgres")
	for _, tc := range []struct {
		name string
		cred auth.Credential
		want bool
	}{
		{"a person", auth.CredentialSession, true},
		{"the CLI login", auth.CredentialAccessToken, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, cleanup := db.SetupTestDB(t)
			t.Cleanup(cleanup)
			ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Completed())
			launchByTrigger(t, ctx, repo, fx.chatID)
			ctx = auth.WithCredential(ctx, tc.cred)
			temporal := &inputCapturingTemporalClient{
				absorbTestTemporalClient: absorbTestTemporalClient{exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_COMPLETED},
			}
			service := &Service{deps: Deps{Repo: repo, Temporal: temporal}, runs: runs.NewService(repo, temporal, nil)}

			_, err := service.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "one more thing"))
			require.NoError(t, err)
			turn, err := repo.ChatPersonTurnAt(ctx, fx.chatID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, turn != nil)
		})
	}
}
