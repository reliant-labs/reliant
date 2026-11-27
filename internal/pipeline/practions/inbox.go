// Copyright (c) 2025 Reliant Labs
package practions

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// QuestionWriter creates a question and puts it on its chat's feed.
// questions.Service satisfies it.
type QuestionWriter interface {
	CreateQuestion(ctx context.Context, question *db.Question) error
}

// QuestionReader reads a question by id, nil when there is none. Its store
// (store.QuestionExtStore) satisfies it.
type QuestionReader interface {
	GetQuestion(ctx context.Context, id string) (*db.Question, error)
}

// InboxRefetcher tells a user's Inbox to re-read. chatupdates.Service's
// EmitUserRefetch satisfies it.
type InboxRefetcher interface {
	EmitUserRefetch(ctx context.Context, userID string, kind db.RefetchType, opts db.RefetchOpts) error
}

// QuestionInbox pages a person the way an agent's ask_user without blocking
// does: an async question on the run's chat, which the Inbox lists until it
// is answered, whatever the run did since. It waits on no one: the run goes
// on. Its one option is an acknowledgement; what the person does about the
// PR (push a fix, hold, close) is what moves it.
type QuestionInbox struct {
	Writer  QuestionWriter
	Reader  QuestionReader
	Refetch InboxRefetcher
}

// Page creates item as an async question.
func (q QuestionInbox) Page(ctx context.Context, item InboxItem) error {
	if existing, err := q.Reader.GetQuestion(ctx, item.ID); err != nil {
		return err
	} else if existing != nil {
		return ErrInboxItemExists
	}
	meta, err := json.Marshal(map[string]any{
		"type": "ask_user",
		"questions": []map[string]any{{
			"question": fmt.Sprintf("%s\n\n%s", item.Title, item.Body),
			"options": []map[string]any{{
				"label":       "I'm on it",
				"description": "You look at the PR. Pushing a fix starts it over; nothing else waits on this answer.",
			}},
		}},
	})
	if err != nil {
		return err
	}
	metadata := string(meta)
	iteration := 0
	question := &db.Question{
		ID: item.ID, ChatID: item.ChatID, WorkflowID: item.RunID, TemporalWorkflowID: item.RunID,
		ThreadID: item.ThreadID, StepID: item.StepID, LoopIteration: &iteration,
		Status: db.QuestionStatusPending, Metadata: &metadata, Async: true, CreatedAt: time.Now().UTC(),
	}
	if err := q.Writer.CreateQuestion(ctx, question); err != nil {
		return err
	}
	if q.Refetch != nil && item.UserID != "" {
		if err := q.Refetch.EmitUserRefetch(ctx, item.UserID, db.RefetchInbox, db.RefetchOpts{ChatID: &item.ChatID}); err != nil {
			logging.WarnContext(ctx, "[practions] could not tell the inbox about a page", "question", item.ID, "error", err)
		}
	}
	return nil
}
