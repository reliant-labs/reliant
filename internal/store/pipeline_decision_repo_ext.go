package store

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Owner decisions (pipeline_decisions; pipeline redesign §4.3), read side
// only: a decision is written by DecidePipeline (a signed-in person) or an
// agent's request, neither of which is here yet. The table is
// forge:no-entity, so its columns are named here once.
//
// A decision is in force when it is approved and has not expired: requested,
// denied, expired, applied (consumed) and revoked decisions are not.

const pipelineDecisionColumns = `id, org_id, project_id, subject_kind, subject_id, head_sha, kind, state,
	value_json, reason, requested_by_run_id, requested_by_principal, decided_by_user_id, decided_via,
	policy_change_id, created_at, decided_at, expires_at, revoked_at, revoked_by_user_id`

// ActivePipelineDecisionsFor lists the decisions in force at now on the
// subject, newest first. A decision bound to a head (every pr_head
// decision) is in force only while head is that head, so a push voids it;
// one bound to no head holds for every head. It reads
// pipeline_decisions_subject_idx.
func ActivePipelineDecisionsFor(ctx context.Context, db orm.Context, kind core.PipelineDecisionSubjectKind, subjectID, head string, now time.Time) ([]*core.PipelineDecision, error) {
	var out []*core.PipelineDecision
	if err := db.Bun().NewRaw(`SELECT `+pipelineDecisionColumns+` FROM pipeline_decisions
		WHERE subject_kind = ?0 AND subject_id = ?1 AND state = ?2
		  AND (expires_at IS NULL OR expires_at > ?3)
		  AND (head_sha IS NULL OR head_sha = ?4)
		ORDER BY created_at DESC, id`,
		kind, subjectID, core.PipelineDecisionApproved, now.UTC(), head).Scan(ctx, &out); err != nil {
		return nil, fmt.Errorf("active pipeline decisions: %w", err)
	}
	return out, nil
}

// ActivePipelineHold returns the newest hold in force at now on the PR
// record (a hold decision about the PR, any head), or nil when it is not
// held.
func ActivePipelineHold(ctx context.Context, db orm.Context, recordID string, now time.Time) (*core.PipelineDecision, error) {
	var out []*core.PipelineDecision
	if err := db.Bun().NewRaw(`SELECT `+pipelineDecisionColumns+` FROM pipeline_decisions
		WHERE subject_kind = ? AND subject_id = ? AND kind = ? AND state = ?
		  AND (expires_at IS NULL OR expires_at > ?)
		ORDER BY created_at DESC, id LIMIT 1`,
		core.PipelineDecisionSubjectPR, recordID, core.PipelineDecisionHold, core.PipelineDecisionApproved, now.UTC()).Scan(ctx, &out); err != nil {
		return nil, fmt.Errorf("active pipeline hold: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out[0], nil
}
