// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for IntegrationWrite. The generated integration_write_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: integrationWriteRepo (a *crud.Repo[IntegrationWrite]) and the
//     package-level GetIntegrationWriteByID / ListIntegrationWrite / CreateIntegrationWrite / UpdateIntegrationWrite / DeleteIntegrationWrite
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     IntegrationWrite struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindIntegrationWriteBy(ctx context.Context, db orm.Context, /* args */) ([]*IntegrationWrite, error) {
//		var rows []*IntegrationWrite
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("user_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveIntegrationWrites(ctx context.Context, db orm.Context) ([]*IntegrationWrite, error) {
//		return ListIntegrationWrite(ctx, db, orm.WhereEq("user_id", true))
//	}
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// IntegrationWriteExtra carries fields on IntegrationWrite that are NOT columns — computed
// values, transient state, anything derived rather than stored. IntegrationWrite is
// embedded on IntegrationWriteExtra (see integration_write_orm.go), so a field added here is a field on
// every IntegrationWrite value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type IntegrationWriteExtra struct {
}

// The write ledger (P5b lane L5). See the migration
// 20261014055046_integration_writes.sql for the model: an intent row is
// written before an integration action's request, completed after, and read
// when an event arrives to say whether Reliant caused it.

// InsertIntegrationWrite records w. An empty status is an intent and an empty
// actor the owner (the column DEFAULT); a nil correlation is the empty object.
func InsertIntegrationWrite(ctx context.Context, db orm.Context, w *core.IntegrationWrite) error {
	row, err := integrationWriteFromCore(w)
	if err != nil {
		return err
	}
	if row.Status == "" {
		row.Status = core.IntegrationWriteIntent
	}
	return CreateIntegrationWrite(ctx, db, row)
}

// CompleteIntegrationWrite settles an intent row at at, and reports whether
// it did. Only an intent row settles: a ledger entry is never rewritten after
// the fact, so a duplicate completion is a no-op. An empty res.Target keeps
// the intent's.
func CompleteIntegrationWrite(ctx context.Context, db orm.Context, id string, res core.IntegrationWriteResult, at time.Time) (bool, error) {
	q := db.Bun().NewUpdate().Model((*IntegrationWrite)(nil)).
		Set("? = ?", bun.Ident(IntegrationWriteFieldStatus), res.Status).
		Set("? = ?", bun.Ident(IntegrationWriteFieldObjectID), res.ObjectID).
		Set("? = ?", bun.Ident(IntegrationWriteFieldObjectURL), res.ObjectURL).
		Set("? = ?", bun.Ident(IntegrationWriteFieldConnectionID), res.ConnectionID).
		Set("? = ?", bun.Ident(IntegrationWriteFieldAmbiguous), res.Ambiguous).
		Set("? = ?", bun.Ident(IntegrationWriteFieldError), res.Error).
		Set("? = ?", bun.Ident(IntegrationWriteFieldCompletedAt), at.UTC())
	if res.Target != "" {
		q = q.Set("? = ?", bun.Ident(IntegrationWriteFieldTarget), res.Target)
	}
	result, err := q.
		Where("? = ?", bun.Ident(IntegrationWriteFieldID), id).
		Where("? = ?", bun.Ident(IntegrationWriteFieldStatus), core.IntegrationWriteIntent).
		Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

// GetIntegrationWriteByObject returns the completed write that created (or
// is) one object, newest first, or core.ErrIntegrationWriteNotFound. Served
// by idx_integration_writes_object.
func GetIntegrationWriteByObject(ctx context.Context, db orm.Context, integration, scope, objectKind, objectID string) (*core.IntegrationWrite, error) {
	var row IntegrationWrite
	err := db.Bun().NewSelect().Model(&row).
		Where("? = ?", bun.Ident(IntegrationWriteFieldIntegration), integration).
		Where("? = ?", bun.Ident(IntegrationWriteFieldScope), scope).
		Where("? = ?", bun.Ident(IntegrationWriteFieldObjectID), objectID).
		Where("? ->> 'object_kind' = ?", bun.Ident(IntegrationWriteFieldCorrelation), objectKind).
		Where("? = ?", bun.Ident(IntegrationWriteFieldStatus), core.IntegrationWriteDone).
		OrderExpr("? DESC, ? DESC", bun.Ident(IntegrationWriteFieldCreatedAt), bun.Ident(IntegrationWriteFieldID)).
		Limit(1).
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrIntegrationWriteNotFound
	}
	if err != nil {
		return nil, err
	}
	return integrationWriteToCore(&row)
}

// ListRecentIntegrationWrites lists the writes into one scope since a time,
// newest first, that could still be the cause of an arriving event: pending,
// completed, or failed with an unknown outcome. A definite rejection (a 4xx)
// changed nothing and so caused nothing. Served by
// idx_integration_writes_scope_recent.
func ListRecentIntegrationWrites(ctx context.Context, db orm.Context, integration, scope string, since time.Time, limit int) ([]*core.IntegrationWrite, error) {
	var rows []*IntegrationWrite
	err := db.Bun().NewSelect().Model(&rows).
		Where("? = ?", bun.Ident(IntegrationWriteFieldIntegration), integration).
		Where("? = ?", bun.Ident(IntegrationWriteFieldScope), scope).
		Where("? >= ?", bun.Ident(IntegrationWriteFieldCreatedAt), since.UTC()).
		Where("(? <> ? OR ?)", bun.Ident(IntegrationWriteFieldStatus), core.IntegrationWriteFailed, bun.Ident(IntegrationWriteFieldAmbiguous)).
		OrderExpr("? DESC, ? DESC", bun.Ident(IntegrationWriteFieldCreatedAt), bun.Ident(IntegrationWriteFieldID)).
		Limit(int64(limit)).
		Scan(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*core.IntegrationWrite, 0, len(rows))
	for _, row := range rows {
		w, err := integrationWriteToCore(row)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}

// PruneIntegrationWrites deletes the entries created before before and
// returns how many.
func PruneIntegrationWrites(ctx context.Context, db orm.Context, before time.Time) (int64, error) {
	res, err := db.Bun().NewDelete().Model((*IntegrationWrite)(nil)).
		Where("? < ?", bun.Ident(IntegrationWriteFieldCreatedAt), before.UTC()).
		Exec(ctx)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func integrationWriteFromCore(w *core.IntegrationWrite) (*IntegrationWrite, error) {
	correlation := w.Correlation
	if correlation == nil {
		correlation = map[string]any{}
	}
	raw, err := json.Marshal(correlation)
	if err != nil {
		return nil, fmt.Errorf("marshal integration write correlation: %w", err)
	}
	var completed *time.Time
	if w.CompletedAt != nil {
		t := w.CompletedAt.UTC()
		completed = &t
	}
	return &IntegrationWrite{
		ID: w.ID, UserID: w.UserID, ChatID: w.ChatID, Workflow: w.WorkflowName,
		TriggerID: w.TriggerID, TriggerName: w.TriggerName, NodeID: w.NodeID, ToolCallID: w.ToolCallID,
		Integration: w.Integration, Action: w.Action, ConnectionID: w.ConnectionID, Actor: w.Actor,
		Scope: w.Scope, Target: w.Target, Correlation: raw,
		ObjectID: w.ObjectID, ObjectURL: w.ObjectURL, Status: w.Status,
		Ambiguous: w.Ambiguous, Error: w.Error, CreatedAt: w.CreatedAt.UTC(), CompletedAt: completed,
	}, nil
}

func integrationWriteToCore(row *IntegrationWrite) (*core.IntegrationWrite, error) {
	correlation := map[string]any{}
	if len(row.Correlation) > 0 {
		if err := json.Unmarshal(row.Correlation, &correlation); err != nil {
			return nil, fmt.Errorf("unmarshal integration write correlation (write %s): %w", row.ID, err)
		}
	}
	return &core.IntegrationWrite{
		ID: row.ID, UserID: row.UserID, ChatID: row.ChatID, WorkflowName: row.Workflow,
		TriggerID: row.TriggerID, TriggerName: row.TriggerName, NodeID: row.NodeID, ToolCallID: row.ToolCallID,
		Integration: row.Integration, Action: row.Action, ConnectionID: row.ConnectionID, Actor: row.Actor,
		Scope: row.Scope, Target: row.Target, Correlation: correlation,
		ObjectID: row.ObjectID, ObjectURL: row.ObjectURL, Status: row.Status,
		Ambiguous: row.Ambiguous, Error: row.Error, CreatedAt: row.CreatedAt, CompletedAt: row.CompletedAt,
	}, nil
}
