package db

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/store"
)

// createQuestionAnnounced and resolveQuestionAnnounced do what internal/questions
// does (write the question, then announce the chat's activity); the db tests
// cannot import it (it imports this package).
func createQuestionAnnounced(ctx context.Context, r *Repo, q *Question) error {
	if err := store.InsertQuestion(ctx, r.ORM(), q); err != nil {
		return err
	}
	_ = r.emitChatActivityIfChanged(ctx, q.ChatID)
	return nil
}

func resolveQuestionAnnounced(ctx context.Context, r *Repo, id string, responseData *string) error {
	if err := store.ResolveQuestion(ctx, r.ORM(), id, responseData, time.Now().UTC()); err != nil {
		return err
	}
	if q, err := store.GetQuestion(ctx, r.ORM(), id); err == nil && q != nil {
		_ = r.emitChatActivityIfChanged(ctx, q.ChatID)
	}
	return nil
}
