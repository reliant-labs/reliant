package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Run placements (internal/machinepool PlaceRun): where a workflow's
// `daemon: {pool: …}` or labels-only `daemon: {labels: …}` placed one run or
// one node of it. A placement counts among its member's live runs while its
// workflow is live (livePlacementSQL, machine_pool_repo_ext.go).

// RunMachinePlacementExtra holds no computed fields; it exists for the
// generated struct's embed.
type RunMachinePlacementExtra struct{}

func runMachinePlacementToCore(row *RunMachinePlacement) (*core.RunMachinePlacement, error) {
	selector := map[string]string{}
	if len(row.Selector) > 0 {
		if err := json.Unmarshal(row.Selector, &selector); err != nil {
			return nil, fmt.Errorf("read placement %s/%q selector: %w", row.WorkflowID, row.Scope, err)
		}
	}
	p := &core.RunMachinePlacement{
		WorkflowID: row.WorkflowID, Scope: row.Scope, UserID: row.UserID, ChatID: row.ChatID,
		Selector: selector, DaemonID: row.DaemonID, CreatedAt: row.CreatedAt,
	}
	if row.MachinePoolID != nil {
		p.MachinePoolID = *row.MachinePoolID
	}
	return p, nil
}

// GetRunMachinePlacement reads the placement of (workflowID, scope);
// core.ErrRunMachinePlacementNotFound when none is recorded.
func GetRunMachinePlacement(ctx context.Context, db orm.Context, workflowID, scope string) (*core.RunMachinePlacement, error) {
	var row RunMachinePlacement
	err := db.Bun().NewSelect().Model(&row).Where("workflow_id = ? AND scope = ?", workflowID, scope).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrRunMachinePlacementNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read placement of %s/%q: %w", workflowID, scope, err)
	}
	return runMachinePlacementToCore(&row)
}

// RecordRunMachinePlacement records p unless (p.WorkflowID, p.Scope) already
// has a placement, and returns the one recorded: p, or the placement a
// concurrent ask recorded first. So a placement, once made, never moves.
func RecordRunMachinePlacement(ctx context.Context, db orm.Context, p *core.RunMachinePlacement) (*core.RunMachinePlacement, error) {
	selector := p.Selector
	if selector == nil {
		selector = map[string]string{}
	}
	encoded, err := json.Marshal(selector)
	if err != nil {
		return nil, fmt.Errorf("encode placement selector: %w", err)
	}
	var poolID *string
	if p.MachinePoolID != "" {
		id := p.MachinePoolID
		poolID = &id
	}
	var rows []RunMachinePlacement
	err = db.Bun().NewRaw(`
INSERT INTO run_machine_placements (workflow_id, scope, user_id, chat_id, machine_pool_id, selector, daemon_id)
VALUES (?0, ?1, ?2, ?3, ?4, ?5::jsonb, ?6)
ON CONFLICT (workflow_id, scope) DO NOTHING
RETURNING `+columnsOf[RunMachinePlacement]("run_machine_placements"),
		p.WorkflowID, p.Scope, p.UserID, p.ChatID, poolID, string(encoded), p.DaemonID).Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("record placement of %s/%q: %w", p.WorkflowID, p.Scope, err)
	}
	if len(rows) == 0 {
		return GetRunMachinePlacement(ctx, db, p.WorkflowID, p.Scope)
	}
	return runMachinePlacementToCore(&rows[0])
}

// MoveRunMachinePlacement moves the placement of (p.WorkflowID, p.Scope) from
// fromDaemonID to p.DaemonID (and p's pool), and returns the placement
// recorded: the moved one, or — when it is no longer on fromDaemonID, a
// concurrent move having taken it elsewhere — the one that stands. The only
// move there is: a node's preflight found fromDaemonID unhealthy, so the
// placement's identity and its slot go to another member. Absent, it is
// recorded as p.
func MoveRunMachinePlacement(ctx context.Context, db orm.Context, p *core.RunMachinePlacement, fromDaemonID string) (*core.RunMachinePlacement, error) {
	var poolID *string
	if p.MachinePoolID != "" {
		id := p.MachinePoolID
		poolID = &id
	}
	var rows []RunMachinePlacement
	err := db.Bun().NewRaw(`
UPDATE run_machine_placements SET daemon_id = ?3, machine_pool_id = ?2
WHERE workflow_id = ?0 AND scope = ?1 AND daemon_id = ?4
RETURNING `+columnsOf[RunMachinePlacement]("run_machine_placements"),
		p.WorkflowID, p.Scope, poolID, p.DaemonID, fromDaemonID).Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("move placement of %s/%q: %w", p.WorkflowID, p.Scope, err)
	}
	if len(rows) == 0 {
		return RecordRunMachinePlacement(ctx, db, p)
	}
	return runMachinePlacementToCore(&rows[0])
}

// GetRunPlacementChat reads what placing a run needs about its chat, from the
// chats row alone; core.ErrChatNotFound when there is none.
func GetRunPlacementChat(ctx context.Context, db orm.Context, chatID string) (*core.RunPlacementChat, error) {
	var row struct {
		ID             string `bun:"id"`
		UserID         string `bun:"user_id"`
		ProjectID      string `bun:"project_id"`
		MachinePoolID  string `bun:"machine_pool_id"`
		ActiveDaemonID string `bun:"active_daemon_id"`
	}
	err := db.Bun().NewRaw(`
SELECT id, user_id, project_id, COALESCE(machine_pool_id, '') AS machine_pool_id, COALESCE(active_daemon_id, '') AS active_daemon_id
FROM chats WHERE id = ?0`, chatID).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", core.ErrChatNotFound, chatID)
	}
	if err != nil {
		return nil, fmt.Errorf("read chat %s for placement: %w", chatID, err)
	}
	c := core.RunPlacementChat(row)
	return &c, nil
}
