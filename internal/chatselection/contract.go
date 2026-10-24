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
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// ErrVersionConflict reports a write that named a version other than the
// record's current one. The caller refetches and decides again. It is an
// Aborted, and its text is what the client reads.
var ErrVersionConflict = svcerr.Aborted("the chat's selection changed since it was read; reload and try again")

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
	Params map[string]any
	Policy ProviderPolicy
	// ChatProvider is the one provider the chat is bound to, "" when it is
	// not bound. Only an explicit provider switch in the root picker binds a
	// chat, and a local root pick unbinds it; ordinary picks and sends never
	// write it. Server-owned: no client patch can set it. Stored beside the
	// policy, stored and returned like it, never interpreted here.
	ChatProvider string
	Version      int64
	UpdatedAt    time.Time
}

// WithChatProvider is cur with its chat provider set to driver ("" unbinds),
// the mutate a MutateChatSelection caller hands the store to (re)bind a chat.
// It returns cur itself when nothing changes, which stores nothing, and nil for
// a chat with no record that is being unbound. Pure, as mutate must be.
func WithChatProvider(cur *Selection, chatID, userID, workflowName, driver string) *Selection {
	if cur != nil && cur.ChatProvider == driver {
		return cur
	}
	if cur == nil && driver == "" {
		return nil
	}
	next := base(cur, chatID, userID, workflowName)
	if cur != nil {
		next.WorkflowName = cur.WorkflowName
	}
	next.ChatProvider = driver
	return next
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
