// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/store"
)

// AtomicMessageWrite is everything one SaveMessage persists: the message row,
// its content blocks, and the chat_update that announces it. Ordinal and Seq
// are allocated by the write itself; see store.AtomicMessageWrite.
type AtomicMessageWrite = store.AtomicMessageWrite

// AtomicMessageResult reports the numbers the database assigned.
type AtomicMessageResult = store.AtomicMessageResult

// SaveMessageAtomic performs the whole message write in one transaction whose
// body is a single round trip for the durable rows (store.SaveMessageAtomic,
// which explains the statement).
//
// It runs at READ COMMITTED deliberately: this write path was the hottest
// SERIALIZABLE transaction in the system and was responsible for the
// overwhelming majority of observed 40001 conflicts. It asks Repo.RunTx for
// the full attempt budget, so the Repository's retry ladder applies —
// including 23505, which a seq collision with one of the writers that still
// allocate for themselves raises and the self-healing allocator resolves on
// the retry.
//
// The chat_update is published only after the transaction commits. Notifying
// from inside would announce a sequence number a reader could not yet SELECT,
// and on rollback would announce one that never existed at all.
func (r *Repo) SaveMessageAtomic(ctx context.Context, w AtomicMessageWrite) (*AtomicMessageResult, error) {
	var result *AtomicMessageResult
	err := r.RunTx(ctx, orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts), func(txCtx context.Context) error {
		var err error
		result, err = store.SaveMessageAtomic(txCtx, r.ORM(), w)
		if err != nil {
			return err
		}
		if r.onChatUpdate != nil {
			committed := result.ChatUpdate
			return runAfterCommit(txCtx, func() {
				r.onChatUpdate(committed.ChatID, committed.SequenceNumber, committed)
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
