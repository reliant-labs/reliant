package message_test

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/db"
	message "github.com/reliant-labs/reliant/internal/handlers/message"
)

const foreignMessageID = "msg-foreign"

// foreignMessageRepo holds one message, foreignMessageID, in a chat another
// user owns; any other id is unknown, as db.Repo reports it. The generated
// harness's stub answers (nil, nil) instead, which the handler reads as
// found. Methods past the ownership check are unreachable from the rows that
// use it: the embedded nil Repository makes calling one a panic, never a
// silent pass.
type foreignMessageRepo struct{ message.Repository }

func (foreignMessageRepo) GetMessage(_ context.Context, id string) (*db.Message, error) {
	if id != foreignMessageID {
		return nil, fmt.Errorf("%w: %s", svcerr.NotFound("message"), id)
	}
	return &db.Message{ID: id, ChatID: "chat-foreign"}, nil
}

func (foreignMessageRepo) GetChatRow(_ context.Context, id string) (*db.ChatRow, error) {
	return &db.ChatRow{ID: id, UserID: "someone-else"}, nil
}
