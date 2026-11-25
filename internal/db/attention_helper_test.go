// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// applyAttentionUpdateRC is the attention write the workflow-status activity
// makes: one READ COMMITTED transaction around store.ApplyAttentionUpdate.
func applyAttentionUpdateRC(ctx context.Context, r *Repo, u core.AttentionUpdate) (core.AttentionResult, error) {
	var result core.AttentionResult
	err := r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var err error
		result, err = store.ApplyAttentionUpdate(ctx, r.ORM(), u)
		return err
	})
	return result, err
}
