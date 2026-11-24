// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// ThreadAssignments is the thread's account assignments, which the repository
// holds.
type ThreadAssignments interface {
	ListThreadAssignments(ctx context.Context, threadID string) ([]core.Assignment, error)
	LatestServingAccount(ctx context.Context, threadID string) (string, error)
}

type personalFundingStore struct {
	store.ChatPersonalFundingExtStore
	store.ThreadFundingExtStore
	store.ChatSelectionExtStore
	store.ChatPersonPickExtStore
	ThreadAssignments
}

// NewPersonalFundingStore is the PersonalFundingStore over the generated ext
// stores of db and the repository's thread assignments.
func NewPersonalFundingStore(db orm.Context, assignments ThreadAssignments) PersonalFundingStore {
	return personalFundingStore{
		ChatPersonalFundingExtStore: store.NewChatPersonalFundingExtStore(db),
		ThreadFundingExtStore:       store.NewThreadFundingExtStore(db),
		ChatSelectionExtStore:       store.NewChatSelectionExtStore(db),
		ChatPersonPickExtStore:      store.NewChatPersonPickExtStore(db),
		ThreadAssignments:           assignments,
	}
}

// SettingReader reads a user's setting.
type SettingReader interface {
	GetSettingValue(ctx context.Context, userID, key string) (string, bool)
}

type runFundingStore struct {
	SettingReader
	store.ChatSelectionExtStore
}

// NewRunFundingStore is the RunFundingStore over a user's settings and the
// chat selection's funding order.
func NewRunFundingStore(db orm.Context, settings SettingReader) RunFundingStore {
	return runFundingStore{SettingReader: settings, ChatSelectionExtStore: store.NewChatSelectionExtStore(db)}
}
