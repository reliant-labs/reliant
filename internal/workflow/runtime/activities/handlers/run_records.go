// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

type attentionRecorder struct{ db orm.Context }

// NewAttentionRecorder applies attention updates over db. The locked incident
// row (including an empty tombstone) serializes all its actors; READ COMMITTED
// ensures waiters observe the previous holder's committed fence.
func NewAttentionRecorder(db orm.Context) AttentionRecorder { return attentionRecorder{db: db} }

func (a attentionRecorder) ApplyAttentionUpdate(ctx context.Context, update core.AttentionUpdate) (core.AttentionResult, error) {
	var result core.AttentionResult
	err := a.db.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var err error
		result, err = store.ApplyAttentionUpdate(ctx, a.db, update)
		return err
	})
	if err != nil {
		return core.AttentionResult{}, err
	}
	return result, nil
}
