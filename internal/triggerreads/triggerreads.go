// Copyright (c) 2025 Reliant Labs
// Package triggerreads hands a role that still names the trigger reads
// (GetTrigger, GetTriggerEventByChatID, GetTriggerEventByChat) the repository
// plus those reads, answered by the generated trigger stores. The facade no
// longer carries them; a role that owns its trigger reads takes a store role
// instead, and this wrapper goes when the last role that names them does.
package triggerreads

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// Repo is the repository and the three trigger reads.
type Repo struct {
	*db.Repo
}

// Of wraps repo.
func Of(repo *db.Repo) Repo { return Repo{Repo: repo} }

// GetTrigger reads a trigger with its project and daemon display names.
func (r Repo) GetTrigger(ctx context.Context, id string) (*core.Trigger, error) {
	return store.GetTrigger(ctx, r.ORM(), id)
}

// GetTriggerEventByChatID reads the launch event of chatID.
func (r Repo) GetTriggerEventByChatID(ctx context.Context, chatID string) (*core.TriggerEvent, error) {
	return store.GetTriggerEventByChatID(ctx, r.ORM(), chatID)
}

// GetTriggerEventByChat reads the launch event of the given kind that started chatID.
func (r Repo) GetTriggerEventByChat(ctx context.Context, kind core.TriggerEventKind, chatID string) (*core.TriggerEvent, error) {
	return store.GetTriggerEventByChat(ctx, r.ORM(), kind, chatID)
}
