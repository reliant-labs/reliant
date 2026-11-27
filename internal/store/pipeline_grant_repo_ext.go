package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The owner's capability grants (pipeline_capability_grants;
// internal/pipeline/grants). The table is forge:no-entity, so its columns are
// named here once. A grant is never deleted by the application: revoking
// sets revoked_at, so the table is its own audit trail.

const pipelineGrantColumns = `id, user_id, org_id, trigger_id, machine_pool_id, repo_id, capability,
	approval_id, granted_at, revoked_at, revoked_by_user_id`

type pipelineGrantRow struct {
	ID              string     `bun:"id"`
	UserID          string     `bun:"user_id"`
	OrgID           *string    `bun:"org_id"`
	TriggerID       string     `bun:"trigger_id"`
	MachinePoolID   string     `bun:"machine_pool_id"`
	RepoID          *string    `bun:"repo_id"`
	Capability      string     `bun:"capability"`
	ApprovalID      *string    `bun:"approval_id"`
	GrantedAt       time.Time  `bun:"granted_at"`
	RevokedAt       *time.Time `bun:"revoked_at"`
	RevokedByUserID *string    `bun:"revoked_by_user_id"`
}

func (r pipelineGrantRow) core() *core.PipelineCapabilityGrant {
	str := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	return &core.PipelineCapabilityGrant{
		ID: r.ID, UserID: r.UserID, OrgID: str(r.OrgID),
		TriggerID: r.TriggerID, MachinePoolID: r.MachinePoolID, RepoID: str(r.RepoID),
		Capability: core.PipelineCapability(r.Capability), ApprovalID: str(r.ApprovalID),
		GrantedAt: r.GrantedAt, RevokedAt: r.RevokedAt, RevokedByUserID: str(r.RevokedByUserID),
	}
}

func pipelineGrantsToCore(rows []pipelineGrantRow) []*core.PipelineCapabilityGrant {
	out := make([]*core.PipelineCapabilityGrant, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.core())
	}
	return out
}

// InsertPipelineCapabilityGrant writes a live grant, or returns the live
// grant of the same activation, pool, capability and repository scope that
// is already there: created says which. Granting again is therefore a no-op
// (ON CONFLICT on pipeline_capability_grants_live, which never aborts the
// caller's transaction).
func InsertPipelineCapabilityGrant(ctx context.Context, db orm.Context, g core.PipelineCapabilityGrantNew) (*core.PipelineCapabilityGrant, bool, error) {
	var rows []pipelineGrantRow
	err := db.Bun().NewRaw(`INSERT INTO pipeline_capability_grants
		(id, user_id, org_id, trigger_id, machine_pool_id, repo_id, capability, approval_id)
		VALUES (?, ?, NULLIF(?, ''), ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''))
		ON CONFLICT (trigger_id, machine_pool_id, capability, (COALESCE(repo_id, ''))) WHERE revoked_at IS NULL DO NOTHING
		RETURNING `+pipelineGrantColumns,
		uuid.NewString(), g.UserID, g.OrgID, g.TriggerID, g.MachinePoolID, g.RepoID, string(g.Capability), g.ApprovalID).Scan(ctx, &rows)
	if err != nil {
		return nil, false, fmt.Errorf("insert pipeline capability grant: %w", err)
	}
	if len(rows) == 1 {
		return rows[0].core(), true, nil
	}
	if err := db.Bun().NewRaw(`SELECT `+pipelineGrantColumns+` FROM pipeline_capability_grants
		WHERE trigger_id = ? AND machine_pool_id = ? AND capability = ? AND COALESCE(repo_id, '') = ? AND revoked_at IS NULL`,
		g.TriggerID, g.MachinePoolID, string(g.Capability), g.RepoID).Scan(ctx, &rows); err != nil {
		return nil, false, fmt.Errorf("read the live pipeline capability grant: %w", err)
	}
	if len(rows) == 0 {
		// Revoked between the two statements: the caller may grant again.
		return nil, false, fmt.Errorf("pipeline capability grant for %s: %w", g.TriggerID, core.ErrCapabilityGrantNotFound)
	}
	return rows[0].core(), false, nil
}

// ListLivePipelineCapabilityGrants lists the live grants of capability to
// the activation, for repoID ("" matches only the grants for every
// repository; a repository's id matches those and its own). Oldest first.
func ListLivePipelineCapabilityGrants(ctx context.Context, db orm.Context, triggerID string, capability core.PipelineCapability, repoID string) ([]*core.PipelineCapabilityGrant, error) {
	var rows []pipelineGrantRow
	if err := db.Bun().NewRaw(`SELECT `+pipelineGrantColumns+` FROM pipeline_capability_grants
		WHERE trigger_id = ? AND capability = ? AND revoked_at IS NULL AND (repo_id IS NULL OR repo_id = NULLIF(?, ''))
		ORDER BY granted_at, id`, triggerID, string(capability), repoID).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list live pipeline capability grants: %w", err)
	}
	return pipelineGrantsToCore(rows), nil
}

// ListPipelineCapabilityGrantsForUser lists the grants userID made, newest
// first: only the live ones unless includeRevoked, and only the activation's
// when triggerID is set.
func ListPipelineCapabilityGrantsForUser(ctx context.Context, db orm.Context, userID, triggerID string, includeRevoked bool) ([]*core.PipelineCapabilityGrant, error) {
	var rows []pipelineGrantRow
	if err := db.Bun().NewRaw(`SELECT `+pipelineGrantColumns+` FROM pipeline_capability_grants
		WHERE user_id = ? AND (? = '' OR trigger_id = ?) AND (? OR revoked_at IS NULL)
		ORDER BY granted_at DESC, id`, userID, triggerID, triggerID, includeRevoked).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list pipeline capability grants: %w", err)
	}
	return pipelineGrantsToCore(rows), nil
}

// RevokePipelineCapabilityGrant revokes userID's grant id at at, recording
// userID as who revoked it, and returns it. A grant already revoked is
// returned as it is; one that does not exist, or that another user made, is
// core.ErrCapabilityGrantNotFound.
func RevokePipelineCapabilityGrant(ctx context.Context, db orm.Context, userID, id string, at time.Time) (*core.PipelineCapabilityGrant, error) {
	var rows []pipelineGrantRow
	if err := db.Bun().NewRaw(`UPDATE pipeline_capability_grants
		SET revoked_at = ?, revoked_by_user_id = ?
		WHERE id = ? AND user_id = ? AND revoked_at IS NULL
		RETURNING `+pipelineGrantColumns, at, userID, id, userID).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("revoke pipeline capability grant: %w", err)
	}
	if len(rows) == 1 {
		return rows[0].core(), nil
	}
	if err := db.Bun().NewRaw(`SELECT `+pipelineGrantColumns+` FROM pipeline_capability_grants
		WHERE id = ? AND user_id = ?`, id, userID).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("read pipeline capability grant: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("pipeline capability grant %s: %w", id, core.ErrCapabilityGrantNotFound)
	}
	return rows[0].core(), nil
}

// PipelineGrantFactsFor reads what a grant of the activation on the pool is
// checked against: both owners, the activation's org and current pool, and
// the pool's name and selector. A missing record leaves its Found flag
// false.
func PipelineGrantFactsFor(ctx context.Context, db orm.Context, triggerID, poolID string) (core.PipelineGrantFacts, error) {
	var facts core.PipelineGrantFacts
	var rows []struct {
		TriggerFound  bool            `bun:"trigger_found"`
		TriggerName   string          `bun:"trigger_name"`
		TriggerUserID string          `bun:"trigger_user_id"`
		TriggerOrgID  string          `bun:"trigger_org_id"`
		TriggerPoolID string          `bun:"trigger_pool_id"`
		PoolFound     bool            `bun:"pool_found"`
		PoolUserID    string          `bun:"pool_user_id"`
		PoolName      string          `bun:"pool_name"`
		PoolSelector  json.RawMessage `bun:"pool_selector"`
	}
	if err := db.Bun().NewRaw(`SELECT
		t.id IS NOT NULL AS trigger_found,
		COALESCE(t.name, '')::text AS trigger_name,
		COALESCE(t.user_id, '')::text AS trigger_user_id,
		COALESCE(t.org_id, '')::text AS trigger_org_id,
		COALESCE(t.machine_pool_id, '')::text AS trigger_pool_id,
		mp.id IS NOT NULL AS pool_found,
		COALESCE(mp.user_id, '')::text AS pool_user_id,
		COALESCE(mp.name, '')::text AS pool_name,
		COALESCE(mp.selector, '{}'::jsonb) AS pool_selector
	FROM (SELECT 1) one
	LEFT JOIN triggers t ON t.id = ?
	LEFT JOIN machine_pools mp ON mp.id = ?`, triggerID, poolID).Scan(ctx, &rows); err != nil {
		return facts, fmt.Errorf("read capability grant facts: %w", err)
	}
	if len(rows) == 0 {
		return facts, nil
	}
	r := rows[0]
	facts = core.PipelineGrantFacts{
		TriggerFound: r.TriggerFound, TriggerName: r.TriggerName, TriggerUserID: r.TriggerUserID, TriggerOrgID: r.TriggerOrgID, TriggerPoolID: r.TriggerPoolID,
		PoolFound: r.PoolFound, PoolUserID: r.PoolUserID, PoolName: r.PoolName, PoolSelector: map[string]string{},
	}
	if len(r.PoolSelector) > 0 {
		if err := json.Unmarshal(r.PoolSelector, &facts.PoolSelector); err != nil {
			return facts, fmt.Errorf("read machine pool %s selector: %w", poolID, err)
		}
	}
	return facts, nil
}

// PipelineDaemonFactsFor reads a machine's owner and its durable labels, as
// its owner set them (daemons.labels), never the labels it reports when it
// connects. A missing machine is Found false.
func PipelineDaemonFactsFor(ctx context.Context, db orm.Context, daemonID string) (core.PipelineDaemonFacts, error) {
	var facts core.PipelineDaemonFacts
	var rows []struct {
		UserID string          `bun:"user_id"`
		Labels json.RawMessage `bun:"labels"`
	}
	if err := db.Bun().NewRaw(`SELECT user_id, labels FROM daemons WHERE id = ?`, daemonID).Scan(ctx, &rows); err != nil {
		return facts, fmt.Errorf("read machine %s: %w", daemonID, err)
	}
	if len(rows) == 0 {
		return facts, nil
	}
	facts = core.PipelineDaemonFacts{Found: true, UserID: rows[0].UserID, Labels: map[string]string{}}
	if len(rows[0].Labels) > 0 {
		if err := json.Unmarshal(rows[0].Labels, &facts.Labels); err != nil {
			return facts, fmt.Errorf("read machine %s labels: %w", daemonID, err)
		}
	}
	return facts, nil
}

// GetCapabilityGrantCard returns the capability-grant card raised for
// entityID (one execution of reliant/pipeline.request_grant@1), or nil when
// there is none, so a retried execution answers with the card it raised.
func GetCapabilityGrantCard(ctx context.Context, db orm.Context, entityID string) (*core.Approval, error) {
	var rows []*Approval
	if err := db.Bun().NewRaw(`SELECT a.id, a.chat_id, a.approval_type, a.entity_id, a.status, a.denial_reason, a.title, a.metadata,
		a.created_at, a.resolved_at, a.action_taken, a.temporal_workflow_id, a.thread_id, a.batch_id, a.decided_by, a.min_role
	FROM approvals a WHERE a.entity_id = ?`, entityID).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("read capability grant card: %w", err)
	}
	if len(rows) == 0 || approvalToCore(rows[0]).CapabilityGrantRequest() == nil {
		return nil, nil
	}
	return approvalToCore(rows[0]), nil
}

// ListPendingCapabilityGrantCards lists the pending capability-grant cards
// (core.ApprovalKindPipelineCapabilityGrant) that ask for a grant to the
// activation, oldest first, so a run asking again finds the card already
// waiting instead of raising another. It reads idx_approvals_pending's
// pending rows only.
func ListPendingCapabilityGrantCards(ctx context.Context, db orm.Context, triggerID string) ([]*core.Approval, error) {
	var rows []*Approval
	if err := db.Bun().NewRaw(`SELECT a.id, a.chat_id, a.approval_type, a.entity_id, a.status, a.denial_reason, a.title, a.metadata,
		a.created_at, a.resolved_at, a.action_taken, a.temporal_workflow_id, a.thread_id, a.batch_id, a.decided_by, a.min_role
	FROM approvals a
	WHERE a.status = 1
	  AND a.metadata LIKE '%"kind":"`+core.ApprovalKindPipelineCapabilityGrant+`"%'
	  AND a.metadata LIKE '%' || ? || '%'
	ORDER BY a.created_at, a.id`, `"trigger_id":"`+triggerID+`"`).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list pending capability grant cards: %w", err)
	}
	var out []*core.Approval
	for _, a := range approvalsToCore(rows) {
		if req := a.CapabilityGrantRequest(); req != nil && req.TriggerID == triggerID {
			out = append(out, a)
		}
	}
	return out, nil
}
