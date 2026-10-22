// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/observe/dbtrace"

	"github.com/reliant-labs/reliant/internal/store"
)

// OpenReadOnlyRepo opens a Postgres connection and wraps it in a *Repo WITHOUT
// running migrations. It is intended for read-only tooling (run analysis /
// node inspection) that must not mutate the target database: the normal
// NewRepoFromConfig path applies migrations on connect, which is a write.
//
// The caller owns the returned Repo and must Close() it. The pgx stdlib driver
// is registered by this package's connect.go import, so callers need not import
// it themselves.
func OpenReadOnlyRepo(url string) (*Repo, error) {
	if url == "" {
		return nil, fmt.Errorf("database URL is required")
	}
	sqlDB, err := dbtrace.Open("pgx", url)
	if err != nil {
		return nil, fmt.Errorf("failed to open postgres database: %w", err)
	}
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("failed to connect to postgres database: %w", err)
	}
	repo, err := NewRepoWithDriver(sqlDB, DriverPostgres)
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return repo, nil
}

// ListQuestionsByChat returns every question (gate) raised for a chat, in
// creation order. It is a read-only query used by run-analysis tooling
// (`reliant-dev workflow analyze`) to reconstruct the gate/question history —
// what was asked at each checkpoint and what was answered.
func (r *Repo) ListQuestionsByChat(ctx context.Context, chatID string) ([]*Question, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}

	return store.ListQuestionsByChat(ctx, r.ORM(), chatID)
}

// ListPendingQuestionsByChat returns every PENDING blocking question in a
// chat, oldest first — not the async ones, which no thread waits on. Each carries its thread_id: a chat with several spawned threads can
// have one question parked per thread, and only the thread id says which
// agent a question belongs to.
//
// A newest-from-any-thread lookup (LIMIT 1) cannot stand in for this, which
// is why that accessor is gone: in prod a spawn's question disappeared behind
// a sibling's newer one, and the user's reply went to the spawn's mailbox, which a thread parked
// on a question never drains.
func (r *Repo) ListPendingQuestionsByChat(ctx context.Context, chatID string) ([]*Question, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}

	return store.ListPendingQuestionsByChat(ctx, r.ORM(), chatID)
}

// ListAsyncQuestionsByChat returns every async question in a chat — asked
// with ask_user without blocking: true, so its thread carried on — pending and resolved
// alike, oldest first. None of them is a gate, so ListPendingQuestionsByChat
// leaves them out; the chat timeline renders them from this.
func (r *Repo) ListAsyncQuestionsByChat(ctx context.Context, chatID string) ([]*Question, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}

	return store.ListAsyncQuestionsByChat(ctx, r.ORM(), chatID)
}

// ListAnswerablePendingQuestionsByChat is ListPendingQuestionsByChat minus the
// questions nobody can answer any more: those whose asking run completed or
// was cancelled (the inbox's rule; see store.ListAnswerablePendingQuestionsByChat).
// It is what the UI shows — a dead sub-agent's leftover question would
// otherwise sit in the composer of its thread, replacing the input the user
// needs to message it.
func (r *Repo) ListAnswerablePendingQuestionsByChat(ctx context.Context, chatID string) ([]*Question, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}

	return store.ListAnswerablePendingQuestionsByChat(ctx, r.ORM(), chatID)
}

// ListAnswerablePendingQuestionsByUser is ListAnswerablePendingQuestionsByChat
// across every chat the user owns: the unfiltered form of the UI's pending
// question list.
func (r *Repo) ListAnswerablePendingQuestionsByUser(ctx context.Context, userID string) ([]*Question, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}

	return store.ListAnswerablePendingQuestionsByUser(ctx, r.ORM(), userID)
}

// PendingQuestionsByThread returns each thread's newest PENDING question for a
// chat, keyed by the question's thread id — which is exactly a workflow row's
// `thread` column, so the result joins straight onto workflow rows.
//
// It exists because a newest-pending-per-chat lookup cannot answer "which
// thread is waiting?". Such a query selects thread_id and workflow_id and then throws the
// scoping away with `WHERE chat_id = ? ... ORDER BY created_at DESC LIMIT 1`,
// so in a chat with several spawned threads it hands back the newest question
// from ANY thread. A caller that reports per thread — `reliant-dev workflow ps` —
// would then stamp one thread's gate onto siblings that are genuinely
// executing. Keying by thread here makes that smear impossible at the source
// rather than asking every caller to avoid it.
//
// Chat-wide questions ("is this chat awaiting input at all?", which is what the
// reconciler asks) are ListPendingQuestionsByChat's.
//
// Read-only: one SELECT per chat, no mutation.
func (r *Repo) PendingQuestionsByThread(ctx context.Context, chatID string) (map[string]*Question, error) {
	pending, err := r.ListPendingQuestionsByChat(ctx, chatID)
	if err != nil {
		return nil, err
	}
	byThread := make(map[string]*Question, len(pending))
	for _, q := range pending {
		// Oldest first, so a later row is the newer question for its thread.
		byThread[q.ThreadID] = q
	}
	return byThread, nil
}
