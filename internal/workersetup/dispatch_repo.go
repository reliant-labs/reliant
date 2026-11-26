// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// dispatchRepo is the repository as the workflow-event dispatcher sees it: the
// facade plus the launch-event read of a chat, which the generated trigger
// event store answers.
type dispatchRepo struct {
	*db.Repo
}

func (r dispatchRepo) GetTriggerEventByChatID(ctx context.Context, chatID string) (*core.TriggerEvent, error) {
	return store.NewTriggerEventExtStore(r.ORM()).GetTriggerEventByChatID(ctx, chatID)
}
