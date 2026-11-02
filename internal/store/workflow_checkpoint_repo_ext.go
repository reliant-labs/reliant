// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// The workflow_checkpoints queries (P5b lane L2): one row per workflow id,
// the position a run has reached, written at cheap boundaries (top-level node
// entry, and each iteration of a top-level loop) so an interrupted run can
// resume at position.
package store

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// SaveWorkflowCheckpoint records cp as its workflow's position, replacing
// the previous one. updated_at is the database's NOW(), whatever cp carries.
func SaveWorkflowCheckpoint(ctx context.Context, db orm.Context, cp *core.WorkflowCheckpoint) error {
	_, err := db.Bun().NewInsert().
		Model(&WorkflowCheckpoint{
			WorkflowID:    cp.WorkflowID,
			ChatID:        cp.ChatID,
			NodeID:        cp.NodeID,
			LoopIteration: cp.LoopIteration,
			CatalogPin:    checkpointPin(cp.CatalogPin),
		}).
		Value(WorkflowCheckpointFieldUpdatedAt, "NOW()").
		On("CONFLICT (workflow_id) DO UPDATE").
		Set("chat_id = EXCLUDED.chat_id").
		Set("node_id = EXCLUDED.node_id").
		Set("loop_iteration = EXCLUDED.loop_iteration").
		Set("catalog_pin = EXCLUDED.catalog_pin").
		Set("updated_at = NOW()").
		Returning("NULL").
		Exec(ctx)
	return err
}

// LoadWorkflowCheckpoint is a workflow's recorded position, or nil when it
// has none: a fresh workflow, or one already cleared.
func LoadWorkflowCheckpoint(ctx context.Context, db orm.Context, workflowID string) (*core.WorkflowCheckpoint, error) {
	row, err := GetWorkflowCheckpointByID(ctx, db, workflowID)
	if svcerr.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &core.WorkflowCheckpoint{
		WorkflowID:    row.WorkflowID,
		ChatID:        row.ChatID,
		NodeID:        row.NodeID,
		LoopIteration: row.LoopIteration,
		CatalogPin:    deref(row.CatalogPin),
		UpdatedAt:     row.UpdatedAt,
	}, nil
}

// ClearWorkflowCheckpoint deletes a workflow's position; none is a no-op.
func ClearWorkflowCheckpoint(ctx context.Context, db orm.Context, workflowID string) error {
	return ignoreNotFound(DeleteWorkflowCheckpoint(ctx, db, workflowID))
}

// WorkflowCheckpointExtra carries fields on WorkflowCheckpoint that are NOT columns — computed
// values, transient state, anything derived rather than stored. WorkflowCheckpoint is
// embedded on WorkflowCheckpointExtra (see workflow_checkpoint_orm.go), so a field added here is a field on
// every WorkflowCheckpoint value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type WorkflowCheckpointExtra struct {
}

// checkpointPin is a checkpoint's catalog pin as its column: NULL for none.
func checkpointPin(pin string) *string {
	if pin == "" {
		return nil
	}
	return &pin
}
