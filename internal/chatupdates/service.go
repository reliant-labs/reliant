// Copyright (c) 2025 Reliant Labs
package chatupdates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

type service struct {
	store     Store
	tx        Tx
	publisher Publisher
}

func (s *service) EmitQuestionUpdate(ctx context.Context, chatID string, update core.QuestionUpdate) error {
	if chatID == "" {
		return fmt.Errorf("chat ID cannot be empty")
	}
	data, err := core.MarshalUpdate(update)
	if err != nil {
		return fmt.Errorf("failed to marshal question update: %w", err)
	}
	return s.appendChat(ctx, chatID, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION, core.EntityIDForQuestion(update.QuestionID), data)
}

func (s *service) EmitChatRefetch(ctx context.Context, chatID string, refetchType core.RefetchType) error {
	if chatID == "" {
		return fmt.Errorf("chat ID cannot be empty")
	}
	data, err := json.Marshal(core.RefetchData{Type: refetchType})
	if err != nil {
		return fmt.Errorf("failed to marshal refetch data: %w", err)
	}
	return s.appendChat(ctx, chatID, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_REFETCH, "refetch-"+string(refetchType), string(data))
}

// appendChat appends one chat update and publishes it once the transaction
// commits.
func (s *service) appendChat(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID, data string) error {
	// The sequence allocation and the insert share one transaction: the
	// counter's row lock is what keeps sequence order equal to commit order,
	// and a rollback must take both back. READ COMMITTED for the reason the
	// facade's CreateChatUpdate documents: one counter row per chat, so a
	// waiter takes the next number instead of aborting with 40001. A call
	// inside an outer transaction inherits its isolation.
	return s.tx.RunTx(ctx, orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts), func(ctx context.Context) error {
		update, err := s.store.AppendChatUpdate(ctx, chatID, updateType, entityID, data)
		if err != nil {
			return err
		}
		if s.publisher == nil {
			return nil
		}
		return afterCommit(ctx, func() { s.publisher.PublishChat(chatID, update.SequenceNumber, update) })
	})
}

func (s *service) EmitUserRefetch(ctx context.Context, userID string, refetchType core.RefetchType, opts core.RefetchOpts) error {
	if userID == "" {
		return fmt.Errorf("user ID cannot be empty")
	}
	data, err := json.Marshal(core.RefetchData{Type: refetchType})
	if err != nil {
		return fmt.Errorf("failed to marshal refetch data: %w", err)
	}
	entityType := reliantv1.EntityType_ENTITY_TYPE_SYSTEM
	entityID := string(refetchType)
	if opts.WorktreeID != nil {
		entityType = reliantv1.EntityType_ENTITY_TYPE_WORKTREE
		entityID = *opts.WorktreeID
	} else if opts.ProjectID != nil {
		entityType = reliantv1.EntityType_ENTITY_TYPE_PROJECT
		entityID = *opts.ProjectID
	}
	update := &core.UserUpdate{
		UserID:     userID,
		ProjectID:  opts.ProjectID,
		WorktreeID: opts.WorktreeID,
		ChatID:     opts.ChatID,
		UpdateType: reliantv1.UserUpdateType_USER_UPDATE_TYPE_REFETCH,
		EntityType: entityType,
		EntityID:   entityID,
		Data:       data,
	}
	// The ID is derived again on every attempt unless the caller chose one: the
	// scoped allocation of a failed attempt rolls back, so the retry takes the
	// then-current next value.
	callerChoseID := update.ID != ""
	return s.tx.RunTx(ctx, orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts), func(ctx context.Context) error {
		if err := s.store.AppendUserUpdate(ctx, update, callerChoseID); err != nil {
			return err
		}
		if s.publisher == nil {
			return nil
		}
		committed := *update
		return afterCommit(ctx, func() { s.publisher.PublishUser(&committed) })
	})
}

// afterCommit defers publish until the transaction ctx carries commits; the
// callbacks belong to one attempt, so a failed attempt's are discarded with it.
func afterCommit(ctx context.Context, publish func()) error {
	if err := orm.AfterCommit(ctx, func(context.Context) { publish() }); err != nil {
		return errors.New("after-commit callback requires repository transaction")
	}
	return nil
}
