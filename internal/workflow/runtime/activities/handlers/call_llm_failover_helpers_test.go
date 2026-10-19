// Copyright (c) 2025 Reliant Labs
package handlers

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/threads"
)

func newThreadsFor(h *IdempotencyTestHelper) *threads.Service { return threads.NewService(h.Repo()) }

func threadsSaveOpts(chatID, text, sealedReasoning, accountID string) threads.SaveMessageOpts {
	return threads.SaveMessageOpts{
		ChatID: chatID, Thread: chatID,
		Role:              int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
		Content:           text,
		Thinking:          &threads.ThinkingContent{Redacted: sealedReasoning},
		ProviderAccountID: accountID,
	}
}
