// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ToolCallUsage. The generated tool_call_usage_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: toolCallUsageRepo (a *crud.Repo[ToolCallUsage]) and the
//     package-level GetToolCallUsageByPK / ListToolCallUsage / CreateToolCallUsage / UpsertToolCallUsage, keyed by
//     the whole composite key (id, attempt), plus UpdateToolCallUsage / DeleteToolCallUsageByPK
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ToolCallUsage struct (or your own row type).
//     Placeholders follow the path: bun's `?` off db.Bun() (builders,
//     NewRaw); postgres's $1, $2, … in db.Exec / db.Query / db.QueryRow,
//     which send the SQL as written (`?` there is the jsonb operator).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Each exported func(ctx context.Context, db orm.Context, …) in this file is
// also a method of ToolCallUsageExtStore (tool_call_usage_ext_store_gen.go, regenerated
// from this file every forge generate), the handle bound: name that in a
// service's Deps — forge wires and mocks it — instead of hand-writing an
// adapter over these functions.
//
// Example (uncomment and adapt):
//
//	func FindToolCallUsageBy(ctx context.Context, db orm.Context, /* args */) ([]*ToolCallUsage, error) {
//		var rows []*ToolCallUsage
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("user_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveToolCallUsages(ctx context.Context, db orm.Context) ([]*ToolCallUsage, error) {
//		return ListToolCallUsage(ctx, db, orm.WhereEq("user_id", true))
//	}
//
// A write over many rows is ONE set-based statement, never List then an
// UpdateToolCallUsageMasked per row (N round trips: it times out at scale). The
// generated UpdateToolCallUsageWhere(ctx, db, values, fields, filters...) is that
// statement over List's own filters. For one it cannot express, go through
// db.Bun() like any raw query (there is no orm.Ident — name columns in the
// SQL, values as `?` arguments):
//
//	func MoveToolCallUsages(ctx context.Context, db orm.Context, from, to any) (int64, error) {
//		res, err := db.Bun().NewUpdate().Model((*ToolCallUsage)(nil)).
//			Set("user_id = ?", to).
//			Where("user_id = ?", from).
//			Exec(ctx)
//		if err != nil {
//			return 0, err
//		}
//		return res.RowsAffected()
//	}
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
)

// tool_call_usage: the count-only record of every tool call a run makes
// (agent-analytics.md §3.5). No column holds a tool's input, output, command,
// file name or error text; TestLLMUsageTablesHoldNoContent pins the set.

// What kind of tool a call named (tool_call_usage.tool_kind, a closed set).
const (
	// ToolKindBuiltin is a tool Reliant ships in its registry.
	ToolKindBuiltin = "builtin"
	// ToolKindIntegration is an action of an integration manifest Reliant ships.
	ToolKindIntegration = "integration"
	// ToolKindMCP is a tool of an MCP server the user configured.
	ToolKindMCP = "mcp"
	// ToolKindResponse is a workflow's response tool, answered on the worker.
	ToolKindResponse = "response"
	// ToolKindUnknown is a name outside every registry: one a model invented.
	ToolKindUnknown = "unknown"
)

// Tool call outcomes.
const (
	ToolOutcomeSuccess   = "success"
	ToolOutcomeError     = "error"
	ToolOutcomeCancelled = "cancelled"
)

// Why a tool call failed (tool_call_usage.error_class, a closed set): what
// the path that answered it knew, never the text of the answer.
const (
	// ToolErrorRefused is a call the run refused to run: the workflow, the
	// turn's capabilities, a tool's bound parameters, or a run with no
	// machine said no.
	ToolErrorRefused = "refused"
	// ToolErrorInvalidInput is input that could not be parsed, or a response
	// tool's that its schema refused.
	ToolErrorInvalidInput = "invalid_input"
	// ToolErrorNoWorkspace is a machine call with no directory to run in: the
	// batch's workspace is blocked, or the project has no checkout there.
	ToolErrorNoWorkspace = "no_workspace"
	// ToolErrorDaemonOffline is a call whose machine was not connected.
	ToolErrorDaemonOffline = "daemon_offline"
	// ToolErrorDispatch is a call that could not be delivered to, or answered
	// by, what runs it.
	ToolErrorDispatch = "dispatch"
	// ToolErrorTimeout is a call that ran out of time.
	ToolErrorTimeout = "timeout"
	// ToolErrorInterrupted is a call whose attempt was lost (its worker died)
	// and was answered "interrupted" by the retry rather than run again.
	ToolErrorInterrupted = "interrupted"
	// ToolErrorTool is a tool that ran and reported an error of its own.
	ToolErrorTool = "tool_error"
	// ToolErrorInternal is a fault in Reliant's own handling of the call.
	ToolErrorInternal = "internal"
	// ToolErrorUnknown is a failure no path classified.
	ToolErrorUnknown = "unknown"
)

// ToolCallUsageRecord is one tool call's record. ID is the call's key within
// its activity; (ID, Attempt) is one execution.
type ToolCallUsageRecord struct {
	ID        string
	Attempt   int32
	UserID    string
	OrgID     string
	ChatID    string
	ProjectID string
	// What the call was part of, from the run scope, as on llm_call_usage.
	// Each is stored as NULL when "": not recorded.
	WorkflowID   string
	ThreadID     string
	WorkflowName string
	NodeID       string
	Preset       string
	AIRunID      string
	AIRootRunID  string
	// ToolName is the tool's name when ToolKind is ToolKindBuiltin or
	// ToolKindIntegration, else "".
	ToolName string
	ToolKind string
	// Location is where the call ran: the span's forge.ai.tool.location
	// (telemetry.ToolLocationDaemon or ToolLocationLocal).
	Location   string
	Outcome    string
	ErrorClass string
	DurationMs int64
	CreatedAt  time.Time
}

// RecordToolCallUsage writes one tool call's record. A repeated write of the
// same (ID, Attempt) changes nothing. Reports whether anything was written.
//
// A write for a user whose account was purged is refused (false, nil), under
// the same shared advisory lock and fence as RecordLLMCallUsage: a tool call
// in flight when the account was deleted must not recreate a record the
// deletion removed. A write for a chat its project's deletion purged fails in
// the table's chat_write_admission trigger.
//
// The caller's ctx must be a transaction (db.Repo.RunTx) so the lock holds
// until the row is written.
func RecordToolCallUsage(ctx context.Context, db orm.Context, rec ToolCallUsageRecord) (bool, error) {
	if rec.ID == "" || rec.UserID == "" || rec.ChatID == "" {
		return false, errors.New("tool call usage: id, user id and chat id are required")
	}
	if rec.Attempt < 1 {
		rec.Attempt = 1
	}
	if rec.ErrorClass != "" && rec.Outcome != ToolOutcomeError {
		rec.ErrorClass = ""
	}
	rec.CreatedAt = rec.CreatedAt.UTC()

	if _, err := db.Bun().NewRaw(`SELECT pg_advisory_xact_lock_shared(hashtextextended(?, 0))`, LLMUsageUserLockKey(rec.UserID)).Exec(ctx); err != nil {
		return false, fmt.Errorf("lock tool call usage for user: %w", err)
	}
	var purged bool
	if err := db.Bun().NewRaw(`SELECT EXISTS (SELECT 1 FROM llm_usage_purged_users WHERE user_id = ?)`, rec.UserID).Scan(ctx, &purged); err != nil {
		return false, fmt.Errorf("check purged user: %w", err)
	}
	if purged {
		return false, nil
	}

	res, err := db.Bun().NewRaw(`INSERT INTO tool_call_usage (id, attempt, user_id, org_id, chat_id, project_id,
		workflow_id, thread_id, workflow_name, node_id, preset, ai_run_id, ai_root_run_id,
		tool_name, tool_kind, location, outcome, error_class, duration_ms, created_at)
		VALUES (?0, ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15, ?16, ?17, ?18, ?19)
		ON CONFLICT (id, attempt) DO NOTHING`,
		rec.ID, rec.Attempt, rec.UserID, rec.OrgID, rec.ChatID, emptyToNil(rec.ProjectID),
		emptyToNil(rec.WorkflowID), emptyToNil(rec.ThreadID), emptyToNil(rec.WorkflowName), emptyToNil(rec.NodeID),
		emptyToNil(rec.Preset), emptyToNil(rec.AIRunID), emptyToNil(rec.AIRootRunID),
		rec.ToolName, rec.ToolKind, rec.Location, rec.Outcome, rec.ErrorClass, max(rec.DurationMs, 0), rec.CreatedAt).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("insert tool call usage %s: %w", rec.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("insert tool call usage %s: %w", rec.ID, err)
	}
	return n > 0, nil
}

// PruneToolCallUsage deletes rows created before cutoff, at most batch of
// them, and returns how many. Call it until it returns less than batch.
func PruneToolCallUsage(ctx context.Context, db orm.Context, cutoff time.Time, batch int) (int64, error) {
	if batch <= 0 {
		return 0, errors.New("prune tool call usage: batch must be positive")
	}
	res, err := db.Bun().NewRaw(`DELETE FROM tool_call_usage WHERE (id, attempt) IN (
		SELECT id, attempt FROM tool_call_usage WHERE created_at < ? ORDER BY created_at LIMIT ? FOR UPDATE SKIP LOCKED)`,
		cutoff.UTC(), batch).Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("prune tool call usage: %w", err)
	}
	return res.RowsAffected()
}

// ToolCallUsageExtra carries fields on ToolCallUsage that are NOT columns — computed
// values, transient state, anything derived rather than stored. ToolCallUsage is
// embedded on ToolCallUsageExtra (see tool_call_usage_orm.go), so a field added here is a field on
// every ToolCallUsage value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ToolCallUsageExtra struct {
}
