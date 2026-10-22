// Copyright (c) 2025 Reliant Labs

// Package chatselection is the server-side record of what a chat's composer
// chose: the model, mode, tools and every other workflow param, plus the chat's
// provider policy.
//
// It exists because the choice used to live only in one browser profile's
// localStorage. A send from another device, a restart, a branch or an API
// message found nothing there and fell back to the preset's tier, so the model
// the user picked "sometimes" did not run. The record is layered under every
// run-building path (presets -> selection -> the send's own params -> schema
// defaults; see launch.Launcher.BuildWorkflowInputs) and read back by the
// composer, which no longer keeps its own copy.
//
// The package knows nothing about workflows, models or providers: Params is an
// opaque nested map, and the policy is stored, not acted on.
//
//forge:not-a-component: built by internal/launch and the chat handler (internal/handlers/chat) — never a node of the composition graph
package chatselection

import (
	"context"
	"errors"
	"time"
)

// ErrVersionConflict reports a write that named a version other than the
// record's current one. The caller refetches and decides again.
var ErrVersionConflict = errors.New("chat selection changed since it was read")

// OnLimit is what a chat does when its provider is rate-limited. The empty
// value inherits the Settings default.
type OnLimit string

const (
	OnLimitInherit  OnLimit = ""
	OnLimitWait     OnLimit = "wait"
	OnLimitFallback OnLimit = "fallback"
)

// ProviderPolicy is stored and returned, never interpreted here.
type ProviderPolicy struct {
	OnLimit OnLimit `json:"on_limit,omitempty"`
	// FallbackModel is "<model>@<driver>".
	FallbackModel string `json:"fallback_model,omitempty"`
}

// Selection is one chat's record.
type Selection struct {
	ChatID       string
	UserID       string
	WorkflowName string
	// Params are nested workflow params, shaped as a run's inputs are.
	Params    map[string]any
	Policy    ProviderPolicy
	Version   int64
	UpdatedAt time.Time
}

// Store is the persistence the service needs. *db.Repo satisfies it.
type Store interface {
	// GetChatSelection returns the chat's record, nil with no error when it has none.
	GetChatSelection(ctx context.Context, chatID string) (*Selection, error)
	// MutateChatSelection serializes writers of one chat, hands mutate the
	// current record (nil when none) and stores what it returns. Returning the
	// same pointer it was given, or nil for no record, stores nothing. mutate may
	// run more than once, so it must be pure.
	MutateChatSelection(ctx context.Context, chatID string, mutate func(current *Selection) (*Selection, error)) (*Selection, error)
	// CopyChatSelection gives toChatID a copy of fromChatID's record, owned by
	// userID, at version 1. A no-op when the source has none.
	CopyChatSelection(ctx context.Context, fromChatID, toChatID, userID string) error
}

// Service reads and writes chat selections.
type Service interface {
	// Get returns the chat's selection, nil when it has none.
	Get(ctx context.Context, chatID string) (*Selection, error)
	// Merge applies a key-level patch to the params: a nil value deletes the
	// key, anything else replaces it whole. A workflowName other than the
	// record's replaces the params before the patch applies, because params
	// belong to one workflow. expectedVersion > 0 must equal the current
	// version (ErrVersionConflict otherwise); 0 writes unconditionally. A patch
	// that changes nothing stores nothing and keeps the version.
	Merge(ctx context.Context, chatID, userID, workflowName string, patch map[string]any, expectedVersion int64) (*Selection, error)
	// SetPolicy replaces the provider policy, with the same version rules.
	SetPolicy(ctx context.Context, chatID, userID, workflowName string, policy ProviderPolicy, expectedVersion int64) (*Selection, error)
	// Copy gives toChatID a copy of fromChatID's selection (BranchChat).
	Copy(ctx context.Context, fromChatID, toChatID, userID string) error
}

// New builds the service over a store.
func New(store Store) Service {
	return &service{store: store}
}
