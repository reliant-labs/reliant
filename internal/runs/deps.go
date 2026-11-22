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

// Deps is what the run-lifecycle service is built from.
type Deps struct {
	// Repo is the chat, workflow and question-update store.
	Repo Repository
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
