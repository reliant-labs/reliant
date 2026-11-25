// Copyright (c) 2025 Reliant Labs
//
// Package questions creates and resolves the questions a run asks, and
// announces the change on the chat's activity.
//
//forge:exclude-contract: two store calls and an announcement over consumer-declared interfaces; the consumers declare what they use
package questions

import (
	"context"
	"errors"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
)

// A QUESTION IS PART OF A CHAT'S ACTIVITY.
//
// A chat with an open blocking question reads AWAITING_INPUT, and one whose
// last question was answered stops reading it. The activity is derived (the
// chats_with_activity view joins questions), so a question written or
// resolved without announcing it leaves clients showing the old state until
// their next snapshot. CreateQuestion and ResolveQuestion therefore announce
// the chat's activity as the repository's methods did.

// Store is the questions table: insert, read and resolve one.
// store.QuestionExtStore satisfies it.
type Store interface {
	InsertQuestion(ctx context.Context, q *db.Question) error
	GetQuestion(ctx context.Context, id string) (*db.Question, error)
	ResolveQuestion(ctx context.Context, id string, responseData *string, resolvedAt time.Time) error
}

// ChatActivity announces a chat's current activity to its watchers, after the
// enclosing transaction commits. *db.Repo satisfies it until the chat update
// emitters have their own package.
type ChatActivity interface {
	EmitChatActivityIfChanged(ctx context.Context, chatID string) error
}

// Service creates and resolves questions.
type Service struct {
	store    Store
	activity ChatActivity
	now      func() time.Time
}

// New returns a Service over s that announces through activity.
func New(s Store, activity ChatActivity) *Service {
	return &Service{store: s, activity: activity, now: func() time.Time { return time.Now().UTC() }}
}

// NewFromRepo returns a Service over the repository's own questions.
func NewFromRepo(r *db.Repo) *Service {
	return New(store.NewQuestionExtStore(r.ORM()), r)
}

// CreateQuestion stores question and announces its chat's activity. The
// announcement cannot fail the write it describes.
func (s *Service) CreateQuestion(ctx context.Context, question *db.Question) error {
	if question == nil {
		return errors.New("question cannot be nil")
	}
	if question.ID == "" {
		return errors.New("question ID cannot be empty")
	}
	if err := s.store.InsertQuestion(ctx, question); err != nil {
		return err
	}
	_ = s.activity.EmitChatActivityIfChanged(ctx, question.ChatID)
	return nil
}

// ResolveQuestion resolves the question and announces its chat's activity.
func (s *Service) ResolveQuestion(ctx context.Context, id string, responseData *string) error {
	if id == "" {
		return errors.New("question ID cannot be empty")
	}
	if err := s.store.ResolveQuestion(ctx, id, responseData, s.now()); err != nil {
		return err
	}
	// The resolve is committed: a failed read-back only means there is no chat
	// to announce, and must not fail the write it follows.
	if q, err := s.store.GetQuestion(ctx, id); err == nil && q != nil {
		_ = s.activity.EmitChatActivityIfChanged(ctx, q.ChatID)
	}
	return nil
}
