// yours: scaffolded once, never touched again — forge will not overwrite this file.

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Moving a project to another org (control-plane
// docs/design/org-project-move.md §3). project_org_moves is the move's
// ledger and saga state; MoveProjectOrg is the one transaction that changes
// reliant's rows (§3.2). Everything here is decided by the caller: who may
// move (internal/authz), the target's membership, control-plane's steps.

// InsertProjectOrgMove records a requested move in state preparing. A repeat
// of the same (actor, request key) returns the move that key made first,
// with created false, whatever its state: a retried request is the same
// move. A second open move of the project is ErrProjectOrgMoveInProgress.
func InsertProjectOrgMove(ctx context.Context, db orm.Context, m *core.ProjectOrgMove) (move *core.ProjectOrgMove, created bool, err error) {
	if m.ID == "" || m.ProjectID == "" || m.OwnerUserID == "" || m.ActorUserID == "" || m.RequestKey == "" {
		return nil, false, fmt.Errorf("project move: id, project, owner, actor and request key are required")
	}
	if m.FromOrgID == "" || m.ToOrgID == "" || m.FromOrgID == m.ToOrgID {
		return nil, false, fmt.Errorf("project move: two different orgs are required")
	}
	counts, err := json.Marshal(m.Counts)
	if err != nil {
		return nil, false, err
	}
	var ids []string
	err = db.Bun().NewRaw(`
INSERT INTO project_org_moves (id, project_id, owner_user_id, actor_user_id, from_org_id, to_org_id, request_key, state, counts)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb)
ON CONFLICT ON CONSTRAINT project_org_moves_request_key DO NOTHING
RETURNING id`,
		m.ID, m.ProjectID, m.OwnerUserID, m.ActorUserID, m.FromOrgID, m.ToOrgID, m.RequestKey,
		string(core.ProjectOrgMoveStatePreparing), string(counts)).Scan(ctx, &ids)
	if orm.ConstraintName(err) == ProjectOrgMoveConstraintOneOpen {
		return nil, false, core.ErrProjectOrgMoveInProgress
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("record the project move: %w", err)
	}
	if len(ids) == 1 {
		move, err = GetProjectOrgMove(ctx, db, ids[0])
		return move, true, err
	}
	var rows []*ProjectOrgMove
	if err := db.Bun().NewSelect().Model(&rows).
		Where("actor_user_id = ?", m.ActorUserID).
		Where("request_key = ?", m.RequestKey).
		Scan(ctx); err != nil {
		return nil, false, fmt.Errorf("read the project move of this request: %w", err)
	}
	if len(rows) == 0 {
		// The conflicting row was the request key's, and it is gone: a
		// move row is never deleted, so this is not a state we reach.
		return nil, false, fmt.Errorf("project move: the request key conflicted with no row")
	}
	move, err = projectOrgMoveToCore(rows[0])
	return move, false, err
}

// GetProjectOrgMove reads one move, or ErrProjectOrgMoveNotFound.
func GetProjectOrgMove(ctx context.Context, db orm.Context, id string) (*core.ProjectOrgMove, error) {
	return readProjectOrgMove(ctx, db, id, false)
}

// readProjectOrgMove reads one move, row-locked FOR UPDATE when lock is set.
func readProjectOrgMove(ctx context.Context, db orm.Context, id string, lock bool) (*core.ProjectOrgMove, error) {
	var rows []*ProjectOrgMove
	q := db.Bun().NewSelect().Model(&rows).Where("id = ?", id)
	if lock {
		q = q.For("UPDATE")
	}
	if err := q.Scan(ctx); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read the project move: %w", err)
	}
	if len(rows) == 0 {
		return nil, core.ErrProjectOrgMoveNotFound
	}
	return projectOrgMoveToCore(rows[0])
}

// TransitionProjectOrgMove moves a move from one of the states in from to
// to, setting detail, and counts when counts is non-nil. A move in any
// other state is ErrProjectOrgMoveState, and nothing is written: each saga
// step names the states it may follow, so a retried or late step cannot
// rewind a move.
func TransitionProjectOrgMove(ctx context.Context, db orm.Context, id string, from []core.ProjectOrgMoveState, to core.ProjectOrgMoveState, detail string, counts *core.ProjectOrgMoveCounts) (*core.ProjectOrgMove, error) {
	states := make([]string, len(from))
	for i, s := range from {
		states[i] = string(s)
	}
	q := db.Bun().NewUpdate().Table("project_org_moves").
		Set("state = ?", string(to)).
		Set("detail = ?", detail).
		Set("updated_at = NOW()").
		Where("id = ?", id).
		Where("state IN (?)", bun.List(states))
	if counts != nil {
		raw, err := json.Marshal(counts)
		if err != nil {
			return nil, err
		}
		q = q.Set("counts = ?::jsonb", string(raw))
	}
	res, err := q.Exec(ctx)
	if orm.ConstraintName(err) == ProjectOrgMoveConstraintOneOpen {
		// Only re-opening a closed move can collide, and no step does.
		return nil, core.ErrProjectOrgMoveInProgress
	}
	if err != nil {
		return nil, fmt.Errorf("advance the project move: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, err
	} else if n == 0 {
		if _, err := GetProjectOrgMove(ctx, db, id); err != nil {
			return nil, err
		}
		return nil, core.ErrProjectOrgMoveState
	}
	return GetProjectOrgMove(ctx, db, id)
}

func projectOrgMoveToCore(row *ProjectOrgMove) (*core.ProjectOrgMove, error) {
	m := &core.ProjectOrgMove{
		ID:          row.ID,
		ProjectID:   row.ProjectID,
		OwnerUserID: row.OwnerUserID,
		ActorUserID: row.ActorUserID,
		FromOrgID:   row.FromOrgID,
		ToOrgID:     row.ToOrgID,
		RequestKey:  row.RequestKey,
		State:       core.ProjectOrgMoveState(row.State),
		Detail:      row.Detail,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
	if len(row.Counts) > 0 {
		if err := json.Unmarshal(row.Counts, &m.Counts); err != nil {
			return nil, fmt.Errorf("project move %s: unmarshal counts: %w", row.ID, err)
		}
	}
	return m, nil
}

// The quiescence predicate (design §2). A chat blocks when its activity, as
// chats_with_activity derives it for the sidebar, is neither idle nor stopped
// on an error: running, awaiting an answer, paused, queued for its machine,
// blocked on it, or waiting out a usage limit — the states MoveChatToProject
// refuses too. A pending or running run of any of the project's chats blocks
// (a sub-agent or background run the root chat's activity does not show),
// and so does a running background process of the project or its chats.

const projectMoveBlockedChatsSQL = `
SELECT v.id, v.title, v.activity, count(*) OVER () AS total
FROM chats_with_activity v
WHERE v.project_id = ?0 AND v.activity NOT IN (?1, ?2)
ORDER BY v.last_active DESC, v.id
LIMIT ?3`

const projectMoveLiveRunsSQL = `
SELECT count(*) FROM workflows w
WHERE w.chat_id IN (SELECT c.id FROM chats c WHERE c.project_id = ?0)
  AND w.state IN (?1, ?2)`

const projectMoveLiveProcessesSQL = `
SELECT count(*) FROM background_processes b
WHERE b.status = ?1
  AND (b.project_id = ?0 OR b.chat_id IN (SELECT c.id FROM chats c WHERE c.project_id = ?0))`

// ProjectOrgMoveBlockers reads what refuses moving projectID now. Read inside
// MoveProjectOrg, under the project's chat row locks, it is the commit's
// check; read alone, it is the preview's, and may be stale by the time a move
// commits.
func ProjectOrgMoveBlockers(ctx context.Context, db orm.Context, projectID string) (*core.ProjectOrgMoveBlockers, error) {
	var chats []struct {
		ID       string `bun:"id"`
		Title    string `bun:"title"`
		Activity int32  `bun:"activity"`
		Total    int    `bun:"total"`
	}
	if err := db.Bun().NewRaw(projectMoveBlockedChatsSQL, projectID,
		chatActivityIdle, chatActivityError, core.ProjectOrgMoveBlockerLimit,
	).Scan(ctx, &chats); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read the project's running chats: %w", err)
	}
	b := &core.ProjectOrgMoveBlockers{}
	for _, c := range chats {
		b.ChatsTotal = c.Total
		b.Chats = append(b.Chats, core.ProjectOrgMoveBlockedChat{ChatID: c.ID, Title: c.Title, Activity: c.Activity})
	}
	if err := db.Bun().NewRaw(projectMoveLiveRunsSQL, projectID,
		reliantv1.WorkflowState_WORKFLOW_STATE_PENDING, reliantv1.WorkflowState_WORKFLOW_STATE_ACTIVE,
	).Scan(ctx, &b.LiveRuns); err != nil {
		return nil, fmt.Errorf("count the project's live runs: %w", err)
	}
	if err := db.Bun().NewRaw(projectMoveLiveProcessesSQL, projectID,
		reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_RUNNING,
	).Scan(ctx, &b.BackgroundProcesses); err != nil {
		return nil, fmt.Errorf("count the project's live background processes: %w", err)
	}
	return b, nil
}

// The statement set of the move's transaction (design §3.2, step 4). Each
// is bounded to the project by its id; ?1 is always the target org.
const (
	// moveProjectOrgProjectSQL restamps the project and clears its share,
	// returning what it was, for the share event.
	moveProjectOrgProjectSQL = `
UPDATE projects p SET org_id = ?1, visibility = NULL, shared_at = NULL, updated_at = NOW()
FROM (SELECT id, org_id, visibility FROM projects WHERE id = ?0) old
WHERE p.id = old.id
RETURNING old.org_id, old.visibility`

	// moveProjectOrgSharedChatsSQL restamps the project's shared chats and
	// clears their shares, recording each in share_events in the org it was
	// shared with: the audience that loses it. A chat's org change and its
	// unshare are one UPDATE, so the org feed's trigger (org-sharing S4)
	// reports the unshare in the old org.
	moveProjectOrgSharedChatsSQL = `
WITH cleared AS (
    UPDATE chats c SET org_id = ?1, visibility = NULL, shared_at = NULL
    FROM (SELECT id, org_id FROM chats WHERE project_id = ?0 AND visibility IS NOT NULL) old
    WHERE c.id = old.id
    RETURNING c.id, old.org_id
)
INSERT INTO share_events (id, org_id, resource_kind, resource_id, owner_user_id, actor_user_id, action, created_at)
SELECT gen_random_uuid()::text, cleared.org_id, 'chat', cleared.id, ?2, ?3, ?4, ?5
FROM cleared`

	// moveProjectOrgChatsSQL restamps the rest of the project's chats,
	// archived included. visibility is not in its SET list, so no per-row
	// sharing trigger fires for the private majority. Neither updated_at
	// nor last_active moves: the sidebar orders by them, and a move is not
	// activity in a chat.
	moveProjectOrgChatsSQL = `
UPDATE chats SET org_id = ?1
WHERE project_id = ?0 AND org_id IS DISTINCT FROM ?1`

	// moveProjectOrgRunsSQL restamps every run of the project's chats, so
	// run lists in the target org agree with their chats. Spend is
	// control-plane's and is never rewritten.
	moveProjectOrgRunsSQL = `
UPDATE workflows SET org_id = ?1
WHERE chat_id IN (SELECT c.id FROM chats c WHERE c.project_id = ?0)`

	// moveProjectOrgTriggerSQL restamps one trigger, pauses it (when the
	// move paused it) and clears an org-connection binding.
	moveProjectOrgTriggerSQL = `
UPDATE triggers SET org_id = ?1, enabled = ?2, paused_reason = ?3, connection_id = ?4, updated_at = NOW()
WHERE id = ?0`
)

// moveProjectOrgAccountAssignmentsSQL clears the moved chats' threads'
// sticky accounts that another org funds: an org's own account, or a
// member's seat or managed key attributed to it
// (org-provider-accounts.md §6, "Project moved"). Only the target's are
// usable in the target, so the target's scope chooses afresh at the next
// call. The person's own accounts serve them in any org and are kept.
const moveProjectOrgAccountAssignmentsSQL = `
DELETE FROM thread_provider_accounts tpa
USING threads t, chats c, provider_accounts a
WHERE t.id = tpa.thread_id AND c.id = t.chat_id AND c.project_id = ?0
  AND a.id = tpa.account_id
  AND ((a.owner_kind = 'org' AND a.org_id IS DISTINCT FROM ?1)
    OR (a.attributed_org_id IS NOT NULL AND a.attributed_org_id IS DISTINCT FROM ?1))`

// moveProjectOrgPersonalFundingSQL clears the moved chats' personal-funding
// questions and answers (org-provider-accounts.md §10): each was asked, and any
// lease granted, about the source org's accounts. In the target org a chat asks
// afresh. Their threads' funding provenance (thread_funding) names the source
// org, so it no longer counts there, and is cleared with them.
const moveProjectOrgPersonalFundingSQL = `
DELETE FROM chat_personal_funding f
USING chats c
WHERE c.id = f.chat_id AND c.project_id = ?0 AND f.org_id IS DISTINCT FROM ?1`

const moveProjectOrgThreadFundingSQL = `
DELETE FROM thread_funding tf
USING threads t, chats c
WHERE t.id = tf.thread_id AND c.id = t.chat_id AND c.project_id = ?0 AND tf.org_id IS DISTINCT FROM ?1`

// MoveProjectOrg is the move's transaction (design §3.2). It runs inside the
// caller's transaction (ReadCommitted), after the caller took the project
// owner's org lock (LockUserOrgs) first, and in this order:
//
//  1. the move row FOR UPDATE: it must be prepared. A move that already
//     committed returns AlreadyMoved and writes nothing, so a retried step
//     is a no-op.
//  2. the project FOR NO KEY UPDATE: it must still be the move's owner's,
//     and in the move's source org. A project with no org yet is in
//     c.DefaultOrgID, as the backfill would stamp it. Else ErrOrgChanged.
//  3. every chat of the project FOR NO KEY UPDATE, in id order: the lock a
//     run's admission takes (withChatWorktreeAdmission, and the workflows
//     admission trigger), so no run starts between the check and the
//     update. Then the quiescence predicate on the locked rows; anything
//     running refuses with ErrProjectNotQuiescent and Blockers.
//  4. the updates: the project, its chats, their runs and its triggers
//     restamped; the chats' threads' sticky accounts that another org
//     funds cleared; shares cleared with a share_events row each; enabled
//     triggers paused (TriggerPausedOrgMove); org-connection bindings
//     cleared; one trigger_changes row per trigger; the move row to moved,
//     with its counts. The triggers are locked FOR NO KEY UPDATE, in id
//     order, before they are read (moveProjectOrgTriggers), so a pause
//     committed meanwhile is decided on, not overwritten.
//
// Background processes are the one blocker no lock here serializes: a daemon
// records them, with no chat or project row lock. The check is a guard, not
// a fence, for them, and nothing depends on it: a process carries no org, so
// one that starts after the check keeps running in the moved project and
// decides nothing under the old org.
//
// FOR NO KEY UPDATE, not FOR UPDATE: it excludes every writer of the rows and
// every admission, but not the FOR KEY SHARE of a foreign-key check, so a
// user_updates insert naming the project or a chat never waits behind the
// move (the deadlock 20261026081121_recovery_admission_no_key_update fixed).
//
// A run being resumed takes its workflows row before its chat (the admission
// trigger runs inside the row's UPDATE), the opposite order to this
// transaction's chats-then-runs. Against a quiescent project only a resume
// racing the commit can meet it, and Postgres then aborts one of the two
// (40P01); the move is retried by its saga.
func MoveProjectOrg(ctx context.Context, db orm.Context, c core.ProjectOrgMoveCommit) (*core.ProjectOrgMoveResult, error) {
	move, err := readProjectOrgMove(ctx, db, c.MoveID, true)
	if err != nil {
		return nil, err
	}
	switch move.State {
	case core.ProjectOrgMoveStatePrepared:
	case core.ProjectOrgMoveStateMoved, core.ProjectOrgMoveStateCompleted:
		return &core.ProjectOrgMoveResult{Move: move, AlreadyMoved: true}, nil
	default:
		return nil, core.ErrProjectOrgMoveState
	}

	var projects []struct {
		UserID string  `bun:"user_id"`
		OrgID  *string `bun:"org_id"`
	}
	if err := db.Bun().NewRaw(`SELECT user_id, org_id FROM projects WHERE id = ? FOR NO KEY UPDATE`, move.ProjectID).
		Scan(ctx, &projects); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("lock the project: %w", err)
	}
	if len(projects) == 0 || projects[0].UserID != move.OwnerUserID {
		return nil, core.ErrProjectNotFound
	}
	in := projects[0].OrgID
	if in == nil && c.DefaultOrgID != "" {
		in = &c.DefaultOrgID
	}
	if in == nil || *in != move.FromOrgID {
		return nil, ErrOrgChanged
	}

	var chats int
	if err := db.Bun().NewRaw(`SELECT count(*) FROM (SELECT id FROM chats WHERE project_id = ? ORDER BY id FOR NO KEY UPDATE) locked`, move.ProjectID).
		Scan(ctx, &chats); err != nil {
		return nil, fmt.Errorf("lock the project's chats: %w", err)
	}
	blockers, err := ProjectOrgMoveBlockers(ctx, db, move.ProjectID)
	if err != nil {
		return nil, err
	}
	if !blockers.Empty() {
		return &core.ProjectOrgMoveResult{Move: move, Blockers: blockers}, core.ErrProjectNotQuiescent
	}

	at := c.At
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()
	counts := move.Counts
	counts.Chats = chats

	// The project.
	var old []struct {
		OrgID      *string `bun:"org_id"`
		Visibility *string `bun:"visibility"`
	}
	if err := db.Bun().NewRaw(moveProjectOrgProjectSQL, move.ProjectID, move.ToOrgID).Scan(ctx, &old); err != nil {
		return nil, fmt.Errorf("move the project: %w", err)
	}
	if len(old) == 1 && old[0].Visibility != nil && old[0].OrgID != nil {
		if err := CreateShareEvent(ctx, db, &ShareEvent{
			ID:           uuid.NewString(),
			OrgID:        *old[0].OrgID,
			ResourceKind: string(core.ShareKindProject),
			ResourceID:   move.ProjectID,
			OwnerUserID:  move.OwnerUserID,
			ActorUserID:  move.ActorUserID,
			Action:       string(core.ShareActionMoved),
			CreatedAt:    at,
		}); err != nil {
			return nil, fmt.Errorf("record the project's cleared share: %w", err)
		}
		counts.SharesCleared++
	}

	// Its chats: the shared ones (clearing each share), then the rest.
	res, err := db.Bun().NewRaw(moveProjectOrgSharedChatsSQL, move.ProjectID, move.ToOrgID,
		move.OwnerUserID, move.ActorUserID, string(core.ShareActionMoved), at).Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("move the project's shared chats: %w", err)
	}
	cleared, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	counts.SharesCleared += int(cleared)
	if _, err := db.Bun().NewRaw(moveProjectOrgChatsSQL, move.ProjectID, move.ToOrgID).Exec(ctx); err != nil {
		return nil, fmt.Errorf("move the project's chats: %w", err)
	}

	// Their runs.
	res, err = db.Bun().NewRaw(moveProjectOrgRunsSQL, move.ProjectID, move.ToOrgID).Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("move the project's runs: %w", err)
	}
	runs, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	counts.Runs = int(runs)

	// Their threads' sticky accounts another org funds.
	if _, err := db.Bun().NewRaw(moveProjectOrgAccountAssignmentsSQL, move.ProjectID, move.ToOrgID).Exec(ctx); err != nil {
		return nil, fmt.Errorf("clear the moved chats' org account assignments: %w", err)
	}
	// Their personal-funding questions, leases and provenance, all about the source org.
	if _, err := db.Bun().NewRaw(moveProjectOrgPersonalFundingSQL, move.ProjectID, move.ToOrgID).Exec(ctx); err != nil {
		return nil, fmt.Errorf("clear the moved chats' personal funding answers: %w", err)
	}
	if _, err := db.Bun().NewRaw(moveProjectOrgThreadFundingSQL, move.ProjectID, move.ToOrgID).Exec(ctx); err != nil {
		return nil, fmt.Errorf("clear the moved chats' thread funding: %w", err)
	}

	// Its triggers.
	if err := moveProjectOrgTriggers(ctx, db, move, &counts); err != nil {
		return nil, err
	}
	if err := restampMovedTriggerFunding(ctx, db, c, move); err != nil {
		return nil, fmt.Errorf("re-stamp the moved triggers' unattended funding: %w", err)
	}

	moved, err := TransitionProjectOrgMove(ctx, db, move.ID,
		[]core.ProjectOrgMoveState{core.ProjectOrgMoveStatePrepared}, core.ProjectOrgMoveStateMoved, "", &counts)
	if err != nil {
		return nil, err
	}
	return &core.ProjectOrgMoveResult{Move: moved}, nil
}

// moveProjectOrgTriggers restamps each of the project's triggers with the
// target org. An enabled one is paused with TriggerPausedOrgMove, which the
// move re-enables once it checks out in the target (design §3.3); one a
// person or the system had already paused keeps its state and reason. A
// binding to an org connection is the source org's and is cleared, never
// carried (§1.2); a personal connection is the owner's in every org and
// stays. Each change is recorded in trigger_changes, as every trigger write
// is.
func moveProjectOrgTriggers(ctx context.Context, db orm.Context, move *core.ProjectOrgMove, counts *core.ProjectOrgMoveCounts) error {
	// Locked before they are read, in id order: every other writer of a
	// trigger's state (PauseTrigger, SetTriggerEnabled, a definition write)
	// locks its row first too, so a pause that commits while the move runs is
	// read here and kept, never overwritten from a stale snapshot.
	var rows []*Trigger
	if err := db.Bun().NewSelect().Model(&rows).
		Where("project_id = ?", move.ProjectID).
		Order("id").
		For("NO KEY UPDATE").
		Scan(ctx); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("lock the project's triggers: %w", err)
	}
	before, err := triggersToCore(rows)
	if err != nil {
		return err
	}
	// The org connections the triggers name. A target-org connection
	// cannot be bound to a trigger of the source, but if one were, it
	// would be the target's to keep.
	var bound []string
	for _, t := range before {
		if t.ConnectionID != nil {
			bound = append(bound, *t.ConnectionID)
		}
	}
	orgBound := map[string]bool{}
	if len(bound) > 0 {
		var ids []string
		if err := db.Bun().NewRaw(`SELECT id FROM connections WHERE id IN (?) AND owner_kind = ? AND org_id IS DISTINCT FROM ?`,
			bun.List(bound), core.ConnectionOwnerOrg, move.ToOrgID).Scan(ctx, &ids); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read the triggers' connections: %w", err)
		}
		for _, id := range ids {
			orgBound[id] = true
		}
	}
	to := move.ToOrgID
	for _, t := range before {
		after := *t
		after.OrgID = &to
		if t.Enabled {
			after.Enabled = false
			after.PausedReason = core.TriggerPausedOrgMove
			counts.TriggersPaused++
		}
		if t.ConnectionID != nil && orgBound[*t.ConnectionID] {
			after.ConnectionID = nil
			counts.BindingsCleared++
		}
		var reason *string
		if !after.Enabled && after.PausedReason != "" {
			reason = &after.PausedReason
		}
		if _, err := db.Bun().NewRaw(moveProjectOrgTriggerSQL, t.ID, to, after.Enabled, reason, after.ConnectionID).Exec(ctx); err != nil {
			return fmt.Errorf("move trigger %s: %w", t.ID, err)
		}
		if err := RecordTriggerChange(ctx, db, core.TriggerChangeOrgMoved, t, &after); err != nil {
			return err
		}
	}
	counts.Triggers = len(before)
	return nil
}

// restampMovedTriggerFunding is where the move re-stamps each moved trigger's
// unattended funding for the target org (org-provider-accounts.md §11.3 and
// the P8 row of §14): the triggers were paused by the move and re-enabled
// under the target's rules, so their stamp is the target's — org_only when the
// target has two or more live members, else any, and any when the count is
// unknown — as if they were created there. triggers.unattended_funding arrives
// with P11, which fills this in (and adds the target's member count to
// core.ProjectOrgMoveCommit); until then there is no stamp to write.
//
// It runs inside the move's transaction, after every trigger of the project is
// restamped and paused (moveProjectOrgTriggers), so an error rolls the whole
// move back. It is a variable only so the move's test pins that call
// (TestMoveProjectOrgCallsTheTriggerFundingSeam); P11 replaces the body, not
// the call.
var restampMovedTriggerFunding = func(context.Context, orm.Context, core.ProjectOrgMoveCommit, *core.ProjectOrgMove) error {
	return nil
}

// ProjectOrgMoveExtra carries fields on ProjectOrgMove that are NOT columns.
// Every field MUST carry the `bun:"-"` tag.
type ProjectOrgMoveExtra struct {
}
