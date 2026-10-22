// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/db"
)

func TestChatWorkflowLookup(t *testing.T) {
	restarted := "chat-1-restart-2"
	empty := ""
	cases := []struct {
		name   string
		chat   *db.Chat
		err    error
		wantID string
		wantOK bool
	}{
		{name: "chats.workflow_id is authoritative", chat: &db.Chat{ID: "chat-1", WorkflowID: &restarted}, wantID: restarted, wantOK: true},
		{name: "no workflow id", chat: &db.Chat{ID: "chat-1"}},
		{name: "empty workflow id", chat: &db.Chat{ID: "chat-1", WorkflowID: &empty}},
		{name: "no chat row", chat: nil},
		{name: "read error", err: errors.New("connection refused")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &MockChatWorkflowReader{
				GetChatFunc: func(_ context.Context, id string) (*db.Chat, error) {
					assert.Equal(t, "chat-1", id)
					return tc.chat, tc.err
				},
			}
			id, ok := ChatWorkflowLookup(repo)(context.Background(), "chat-1")
			assert.Equal(t, tc.wantID, id)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, 1, repo.CallCount("GetChat"))
		})
	}

	t.Run("no repository", func(t *testing.T) {
		id, ok := ChatWorkflowLookup(nil)(context.Background(), "chat-1")
		assert.Empty(t, id)
		assert.False(t, ok)
	})
}
