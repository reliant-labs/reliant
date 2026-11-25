// forge:deps — consumer-declared interfaces for this package's Deps.
// Copyright (c) 2025 Reliant Labs

package runs

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
)

// Questions reads a chat's open blocking questions, which a terminate voids.
// store.QuestionExtStore satisfies it.
type Questions interface {
	ListPendingQuestionsByChat(ctx context.Context, chatID string) ([]*db.Question, error)
}

// Chats reads the chat a run belongs to, with its computed state, and checks
// no owner: every caller decided access first. store.ChatsWithActivityExtStore
// satisfies it.
type Chats interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
}

// QuestionResolver resolves a question and announces the chat's activity.
// *questions.Service satisfies it.
type QuestionResolver interface {
	ResolveQuestion(ctx context.Context, id string, responseData *string) error
}

// WorkflowLoader reads a run's workflow row by id. store.WorkflowExtStore
// satisfies it.
type WorkflowLoader interface {
	LoadWorkflow(ctx context.Context, id string) (*db.Workflow, error)
}

// Deps is what the run-lifecycle service is built from.
type Deps struct {
	// Repo is the chat, workflow and question-update store.
	Repo Repository
	// Chats reads the chat a run belongs to.
	Chats Chats
	// Resolver resolves the questions a terminate voids.
	Resolver QuestionResolver
	// Workflows reads the run's workflow row.
	Workflows WorkflowLoader
	// Questions lists the pending questions a terminate resolves.
	Questions Questions
	// Temporal asks for execution state and cancels or terminates runs.
	Temporal TemporalClient
	// Pause signals, resets and resumes runs. May be nil where nothing
	// resumes or pauses (a test that only reads state).
	Pause PauseController
	// Waits releases a stopped run's usage-limit wait markers. nil releases
	// none. providerbackoff.Service satisfies it.
	Waits UsageLimitWaits
}
