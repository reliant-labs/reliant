package chat_test

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	chat "github.com/reliant-labs/reliant/internal/handlers/chat"
)

// The per-rpc scaffold tests (handlers_scaffold_*_test.go) run against the
// generated harness: NewTestChat stubs every Deps interface, and a stub
// method nobody programmed fails closed. These are the chats they program.
const (
	// ownChatID is a chat of the harness's default caller ("test-user").
	ownChatID = "chat-own"
	// otherChatID is a chat of another user: to the caller it does not exist.
	otherChatID = "chat-other"
	// archivedChatID is an archived chat of the caller.
	archivedChatID = "chat-archived"
	// missingChatID names no chat at all.
	missingChatID = "chat-missing"
)

// newChatRepo returns a StubRepo whose chat reads answer from the three
// fixture chats, with a missing id as an error, as the real store does.
// Every other method is left for a row to program.
func newChatRepo() *chat.StubRepo {
	chats := map[string]db.Chat{
		ownChatID:      {ID: ownChatID, UserID: "test-user", ProjectID: "project-1", Title: "Own chat", State: db.ChatStateIdle},
		otherChatID:    {ID: otherChatID, UserID: "someone-else", ProjectID: "project-1", Title: "Other chat", State: db.ChatStateIdle},
		archivedChatID: {ID: archivedChatID, UserID: "test-user", ProjectID: "project-1", Title: "Archived chat", State: db.ChatStateArchived},
	}
	// A miss answers as the store does: core.ErrChatNotFound, the
	// classified NotFound, wrapped with the id.
	get := func(id string) (*db.Chat, error) {
		c, ok := chats[id]
		if !ok {
			return nil, fmt.Errorf("%w: %s", core.ErrChatNotFound, id)
		}
		return &c, nil
	}
	return &chat.StubRepo{
		GetChatFunc: func(_ context.Context, id string) (*db.Chat, error) {
			return get(id)
		},
		GetChatWithUserCheckFunc: func(_ context.Context, id, userID string) (*db.Chat, error) {
			c, err := get(id)
			if err != nil {
				return nil, err
			}
			if c.UserID != userID {
				return nil, fmt.Errorf("%w: %s", core.ErrChatNotFound, id)
			}
			return c, nil
		},
	}
}
