// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/ptr"
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
			(&Service{deps: Deps{Repo: repo}}).recordPersonTurn(tc.ctx, tc.sender, tc.chat)
			if tc.want {
				assert.Equal(t, []string{"c1/" + tc.sender}, recorded)
			} else {
				assert.Empty(t, recorded)
			}
		})
	}

	t.Run("a failed write does not fail the send", func(t *testing.T) {
		repo := &MockRepository{RecordChatPersonTurnFunc: func(context.Context, string, string) error { return errors.New("db down") }}
		assert.NotPanics(t, func() { (&Service{deps: Deps{Repo: repo}}).recordPersonTurn(person, "alice", triggered) })
	})
}
