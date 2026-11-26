// Copyright (c) 2025 Reliant Labs
package handlers

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/rowreads"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/threads"
)

func newThreadsFor(h *IdempotencyTestHelper) *threads.Service {
	return threads.New(threads.Deps{Repo: rowreads.WithRepo(h.Repo()), Messages: store.NewMessageExtStore(h.Repo().ORM()), Senders: store.NewThreadExtStore(h.Repo().ORM())})
}

func threadsSaveOpts(chatID, text, sealedReasoning, accountID string) threads.SaveMessageOpts {
	return threads.SaveMessageOpts{
		ChatID: chatID, Thread: chatID,
		Role:              int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
		Content:           text,
		Thinking:          &threads.ThinkingContent{Redacted: sealedReasoning},
		ProviderAccountID: accountID,
	}
}
