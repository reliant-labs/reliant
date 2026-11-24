// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// The node_observations queries: the record a node's observe: block writes
// when the node finishes (internal/workflow/observe), and the ledger retention
// pass over it. Functions take (ctx, orm.Context), join the transaction ctx
// carries, and speak core types.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// insertNodeObservationSQL writes one observation unless its run already
// holds ?15 rows, and converges on the row already there for the same node
// attempt and source (the unique index node_observations_node) instead of
// failing. It returns whether the run was full and whether a row was written;
// neither means a duplicate.
//
// The cap is counted by run, or by workflow when the activity named no run
// (?2 = ”): the two arms read the run index and the unique index's leading
// column. It is approximate under concurrency (two writers can both see one
// slot left), which a bound on growth tolerates.
const insertNodeObservationSQL = `
WITH cap AS (
    SELECT count(*) >= ?15 AS at_cap FROM (
        SELECT 1 FROM node_observations WHERE ?2 <> '' AND run_id = ?2
         UNION ALL
        SELECT 1 FROM node_observations WHERE ?2 = '' AND workflow_id = ?1
         LIMIT ?15) s
), ins AS (
    INSERT INTO node_observations (
        id, workflow_id, run_id, chat_id, step_id, node_path, loop_node_id, loop_iteration,
        attempt, source, attributes, events, errors, truncated, created_at)
    SELECT ?0, ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10::jsonb, ?11::jsonb, ?12::jsonb, ?13, ?14
     WHERE NOT (SELECT at_cap FROM cap)
    ON CONFLICT DO NOTHING
    RETURNING 1
)
SELECT (SELECT at_cap FROM cap) AS at_cap, EXISTS (SELECT 1 FROM ins) AS inserted`

// InsertNodeObservation records one node attempt's observation, at most
// maxPerRun rows a run. A second write for the same node attempt and source
// is a no-op (NodeObservationDuplicate), so an activity retry or a replayed
// recording never records twice.
func InsertNodeObservation(ctx context.Context, db orm.Context, obs *core.NodeObservation, maxPerRun int) (core.NodeObservationWrite, error) {
	if obs == nil {
		return 0, fmt.Errorf("insert node observation: nil observation")
	}
	if maxPerRun <= 0 {
		return 0, fmt.Errorf("insert node observation: maxPerRun must be positive")
	}
	source := obs.Source
	if source == "" {
		source = core.NodeObservationSourceWorkflow
	}
	attempt := obs.Attempt
	if attempt <= 0 {
		attempt = 1
	}
	created := obs.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	var row struct {
		AtCap    bool `bun:"at_cap"`
		Inserted bool `bun:"inserted"`
	}
	err := db.Bun().NewRaw(insertNodeObservationSQL,
		obs.ID, obs.WorkflowID, obs.RunID, obs.ChatID, obs.StepID, obs.NodePath,
		obs.LoopNodeID, obs.LoopIteration, attempt, source,
		jsonOr(obs.Attributes, "{}"), jsonOr(obs.Events, "[]"), jsonOr(obs.Errors, "[]"),
		obs.Truncated, created.UTC(), maxPerRun,
	).Scan(ctx, &row)
	if err != nil {
		return 0, fmt.Errorf("insert node observation: %w", err)
	}
	switch {
	case row.Inserted:
		return core.NodeObservationStored, nil
	case row.AtCap:
		return core.NodeObservationCapped, nil
	default:
		return core.NodeObservationDuplicate, nil
	}
}

// jsonOr is text made castable to jsonb (jsonbStorable), or def when empty.
func jsonOr(text, def string) string {
	if text == "" {
		return def
	}
	return *jsonbStorable(&text)
}

// ListWorkflowNodeObservations returns the observations recorded in one
// workflow (not its sub-workflows), oldest first.
func ListWorkflowNodeObservations(ctx context.Context, db orm.Context, workflowID string) ([]*core.NodeObservation, error) {
	var rows []*NodeObservation
	if err := db.Bun().NewSelect().Model(&rows).
		Where("workflow_id = ?", workflowID).
		OrderExpr("created_at, id").
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("list node observations: %w", err)
	}
	out := make([]*core.NodeObservation, len(rows))
	for i, r := range rows {
		out[i] = nodeObservationToCore(r)
	}
	return out, nil
}

// ListRunNodeObservations returns the observations recorded in one workflow
// and every workflow under it (the tree ListRunNodeExecutions reads, never a
// different root run of the same chat), newest first, at most limit.
func ListRunNodeObservations(ctx context.Context, db orm.Context, workflowID string, limit int) ([]*core.NodeObservation, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("list run node observations: limit must be positive")
	}
	var rows []*NodeObservation
	err := db.Bun().NewRaw(`WITH RECURSIVE tree AS (
    SELECT id, chat_id FROM workflows WHERE id = ?0
    UNION ALL SELECT w.id, w.chat_id FROM workflows w JOIN tree t ON w.parent_id = t.id AND w.chat_id = t.chat_id
)
SELECT `+columnsOf[NodeObservation]("o")+` FROM node_observations o JOIN tree t ON t.id = o.workflow_id AND t.chat_id = o.chat_id
ORDER BY o.created_at DESC, o.id DESC LIMIT ?1`, workflowID, limit).Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("list run node observations: %w", err)
	}
	return nodeObservationsToCore(rows), nil
}

// ListChatNodeObservations returns the observations recorded anywhere in one
// chat, newest first, at most limit.
func ListChatNodeObservations(ctx context.Context, db orm.Context, chatID string, limit int) ([]*core.NodeObservation, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("list chat node observations: limit must be positive")
	}
	var rows []*NodeObservation
	if err := db.Bun().NewSelect().Model(&rows).
		Where("chat_id = ?", chatID).
		OrderExpr("created_at DESC, id DESC").
		Limit(int64(limit)).
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("list chat node observations: %w", err)
	}
	return nodeObservationsToCore(rows), nil
}

func nodeObservationsToCore(rows []*NodeObservation) []*core.NodeObservation {
	out := make([]*core.NodeObservation, len(rows))
	for i, r := range rows {
		out[i] = nodeObservationToCore(r)
	}
	return out
}

func nodeObservationToCore(r *NodeObservation) *core.NodeObservation {
	o := &core.NodeObservation{
		ID:            r.ID,
		ChatID:        r.ChatID,
		WorkflowID:    r.WorkflowID,
		RunID:         r.RunID,
		StepID:        r.StepID,
		NodePath:      r.NodePath,
		LoopNodeID:    r.LoopNodeID,
		LoopIteration: r.LoopIteration,
		Attributes:    string(r.Attributes),
		Events:        string(r.Events),
		Errors:        string(r.Errors),
		Truncated:     r.Truncated,
		CreatedAt:     r.CreatedAt,
	}
	if r.Attempt != nil {
		o.Attempt = *r.Attempt
	}
	if r.Source != nil {
		o.Source = *r.Source
	}
	return o
}

// The observation prune (internal/db/ledger_retention.go): the step prune's
// walk over root runs (StepRetentionRoots) and its judgment of each workflow
// in a tree (stepRetentionTreeSQL: completed or cancelled before the cutoff),
// on node_observations, with its own cursor and deferred roots so it runs on
// its own retention period. Every observation of a prunable workflow goes:
// unlike step_executions there is nothing the chat timeline renders here.

// prunableObservationsSQL selects the observations of the prunable workflows
// of stepRetentionTreeSQL.
const prunableObservationsSQL = `
    SELECT o.id FROM node_observations o
      JOIN judged j ON j.id = o.workflow_id AND j.prunable`

// pruneRunObservationsSQL deletes up to ?2 of them.
const pruneRunObservationsSQL = stepRetentionTreeSQL + `
DELETE FROM node_observations WHERE id IN (` + prunableObservationsSQL + `
     LIMIT ?2)`

// runObservationsLeftSQL: whether any prunable observation of the tree is
// left (a short batch does not prove there is none).
const runObservationsLeftSQL = stepRetentionTreeSQL + `
SELECT EXISTS (` + prunableObservationsSQL + `)`

// PruneRunObservations deletes up to limit prunable observations of root run
// rootID's tree, as of cutoff, and returns how many.
func PruneRunObservations(ctx context.Context, db orm.Context, rootID string, cutoff time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("prune run observations: limit must be positive")
	}
	res, err := db.Bun().NewRaw(pruneRunObservationsSQL, rootID, cutoff.UTC(), limit).Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("prune run observations: %w", err)
	}
	return res.RowsAffected()
}

// RunObservationsLeft reports whether root run rootID's tree still has an
// observation PruneRunObservations would delete.
func RunObservationsLeft(ctx context.Context, db orm.Context, rootID string, cutoff time.Time) (bool, error) {
	var left bool
	if err := db.Bun().NewRaw(runObservationsLeftSQL, rootID, cutoff.UTC()).Scan(ctx, &left); err != nil {
		return false, fmt.Errorf("run observations left: %w", err)
	}
	return left, nil
}

// DeferObservationRetentionRoot records that root run rootID must be
// revisited by the observation prune.
func DeferObservationRetentionRoot(ctx context.Context, db orm.Context, rootID string) error {
	if _, err := db.Exec(ctx, `INSERT INTO node_observation_retention_deferred_roots (root_id) VALUES ($1) ON CONFLICT (root_id) DO NOTHING`, rootID); err != nil {
		return fmt.Errorf("defer observation retention root: %w", err)
	}
	return nil
}

// ClearObservationRetentionRoot records that root run rootID's tree has no
// observation left to prune.
func ClearObservationRetentionRoot(ctx context.Context, db orm.Context, rootID string) error {
	if _, err := db.Exec(ctx, `DELETE FROM node_observation_retention_deferred_roots WHERE root_id = $1`, rootID); err != nil {
		return fmt.Errorf("clear observation retention root: %w", err)
	}
	return nil
}

// DeferredObservationRetentionRoots returns up to limit deferred root runs
// after afterID, in id order.
func DeferredObservationRetentionRoots(ctx context.Context, db orm.Context, afterID string, limit int) ([]string, error) {
	var ids []string
	if err := db.Bun().NewRaw(`SELECT root_id FROM node_observation_retention_deferred_roots WHERE root_id > ?0 ORDER BY root_id LIMIT ?1`, afterID, limit).Scan(ctx, &ids); err != nil {
		return nil, fmt.Errorf("deferred observation retention roots: %w", err)
	}
	return ids, nil
}

// NodeObservationExtra carries fields on NodeObservation that are NOT columns — computed
// values, transient state, anything derived rather than stored. NodeObservation is
// embedded on NodeObservationExtra (see node_observation_orm.go), so a field added here is a field on
// every NodeObservation value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type NodeObservationExtra struct {
}
