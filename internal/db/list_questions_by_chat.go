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

// PendingQuestionsByThread returns each thread's newest PENDING question for a
// chat, keyed by the question's thread id — which is exactly a workflow row's
// `thread` column, so the result joins straight onto workflow rows.
//
// It exists because GetPendingQuestionByChatID cannot answer "which thread is
// waiting?". That query selects thread_id and workflow_id and then throws the
// scoping away with `WHERE chat_id = ? ... ORDER BY created_at DESC LIMIT 1`,
// so in a chat with several spawned threads it hands back the newest question
// from ANY thread. A caller that reports per thread — `reliant-dev workflow ps` —
// would then stamp one thread's gate onto siblings that are genuinely
// executing. Keying by thread here makes that smear impossible at the source
// rather than asking every caller to avoid it.
//
// Chat-wide questions ("is this chat awaiting input at all?", which is what the
// reconciler asks) are still GetPendingQuestionByChatID's job.
//
// Read-only: one SELECT per chat, no mutation.
func (r *Repo) PendingQuestionsByThread(ctx context.Context, chatID string) (map[string]*Question, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}

	pending, err := store.ListPendingQuestionsByChat(ctx, r.ORM(), chatID)
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
