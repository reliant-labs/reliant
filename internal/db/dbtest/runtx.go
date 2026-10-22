// Copyright (c) 2025 Reliant Labs
package dbtest

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"
)

// RunTxInline runs fn once with ctx, as db.Repo.RunTx would on a first attempt
// that commits. It is the RunTxFunc for a generated mock of a role that lists
// RunTx: `RunTxFunc: dbtest.RunTxInline`. There is no transaction, rollback or
// retry behind it, so a test that asserts any of those stays DB-backed
// (db.SetupTestDB).
func RunTxInline(ctx context.Context, _ orm.TxPolicy, fn func(context.Context) error) error {
	return fn(ctx)
}
