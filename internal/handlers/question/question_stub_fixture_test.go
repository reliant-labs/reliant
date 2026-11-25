package question_test

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	question "github.com/reliant-labs/reliant/internal/handlers/question"
)

// emptyQuestionRepo is a repository holding no question and no chat,
// answering each lookup as the store does for a missing row: a question is
// (nil, nil), a chat is NotFound.
func emptyQuestionRepo() *question.MockRepository {
	return &question.MockRepository{
		GetQuestionByIDFunc: func(context.Context, string) (*db.Question, error) { return nil, nil },
	}
}

// emptyChatRows and emptyChatViews answer a chat as the store does for a
// missing row: core.ErrChatNotFound.
func emptyChatRows() *question.MockChatRows {
	return &question.MockChatRows{GetChatRowFunc: func(_ context.Context, id string) (*db.ChatRow, error) {
		return nil, fmt.Errorf("%w: %s", core.ErrChatNotFound, id)
	}}
}

func emptyChatViews() *question.MockChatViews {
	return &question.MockChatViews{GetChatFunc: func(_ context.Context, id string) (*db.Chat, error) {
		return nil, fmt.Errorf("%w: %s", core.ErrChatNotFound, id)
	}}
}
