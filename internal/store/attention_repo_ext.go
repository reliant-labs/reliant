package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// attentionRow includes the durable high-water fence, separately from the
// recovery owner: a no-transition start fences old finishes without renewing
// or replacing an existing recovery lease.
type attentionRow struct {
	core.AttentionIncident
	RunID      string
	StartedNS  int64
	OwnerRunID string
	Notified   bool
}

// ApplyAttentionUpdate must run in a READ COMMITTED transaction. The incident
// row lock serializes every actor of one key, including its initial creation.
// All queries stay in this owned seam; no generated projection is required.
func ApplyAttentionUpdate(ctx context.Context, db orm.Context, u core.AttentionUpdate) (core.AttentionResult, error) {
	var result core.AttentionResult
	if u.ChatID == "" || u.RunID == "" || u.StartedAt.IsZero() || strings.TrimSpace(u.Key) == "" || len(u.Key) > 512 {
		return result, errors.New("invalid attention actor identity")
	}
	if u.Phase != "start" && u.Phase != "finish" {
		return result, errors.New("invalid attention phase")
	}
	if (u.Phase == "start" && u.State != "" && u.State != "recovering" && u.State != "action_required") ||
		(u.Phase == "finish" && u.State != "" && u.State != "resolved" && u.State != "action_required") {
		return result, errors.New("invalid attention transition")
	}
	if u.State == "recovering" && (u.RecoveryTimeout < time.Second || u.RecoveryTimeout > 24*time.Hour) {
		return result, errors.New("invalid attention recovery timeout")
	}
	if u.State != "recovering" && u.RecoveryTimeout != 0 {
		return result, errors.New("attention timeout requires recovering start")
	}
	var userID, projectID, triggerID string
	err := db.QueryRow(ctx, `SELECT c.user_id, c.project_id, COALESCE(c.trigger_id, '')
FROM chats_with_activity c JOIN projects p ON p.id=c.project_id AND p.user_id=c.user_id
WHERE c.id=$1 AND c.launch_kind=ANY($2::text[])`, u.ChatID, core.UnattendedEventKinds()).Scan(&userID, &projectID, &triggerID)
	if err != nil {
		return result, fmt.Errorf("attention chat ownership: %w", err)
	}
	// A tombstone exists even for no-transition actors, so a later old finish
	// cannot open an incident behind a successful/newer observation.
	_, err = db.Exec(ctx, `INSERT INTO attention_incidents (user_id,project_id,incident_key)
VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, userID, projectID, u.Key)
	if err != nil {
		return result, err
	}
	var row attentionRow
	err = db.QueryRow(ctx, `SELECT episode_id,state,chat_id,trigger_id,failure_class,detail,opened_at,updated_at,lease_until,
actor_run_id,actor_started_at_ns,owner_run_id,notified FROM attention_incidents
WHERE user_id=$1 AND project_id=$2 AND incident_key=$3 FOR UPDATE`, userID, projectID, u.Key).
		Scan(&row.EpisodeID, &row.State, &row.ChatID, &row.TriggerID, &row.FailureClass, &row.Detail, &row.OpenedAt, &row.UpdatedAt, &row.LeaseUntil,
			&row.RunID, &row.StartedNS, &row.OwnerRunID, &row.Notified)
	if err != nil {
		return result, err
	}
	var now time.Time
	if err = db.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return result, err
	}
	if row.State == "recovering" && row.LeaseUntil != nil && !row.LeaseUntil.After(now) {
		if _, err = db.Exec(ctx, `UPDATE attention_actors SET expired=true WHERE run_id=$1`, row.OwnerRunID); err != nil {
			return result, err
		}
		row.State, row.FailureClass, row.Detail = "action_required", "recovery_expired", "Recovery timed out"
		row.UpdatedAt = *row.LeaseUntil
		row.LeaseUntil = nil
	}
	_, err = db.Exec(ctx, `INSERT INTO attention_actors
(run_id,chat_id,user_id,project_id,incident_key,started_at,started_at_ns) VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT DO NOTHING`, u.RunID, u.ChatID, userID, projectID, u.Key, u.StartedAt, u.StartedAt.UnixNano())
	if err != nil {
		return result, err
	}
	var actorChat, actorUser, actorProject, actorKey string
	var actorStarted int64
	var startSeen, finished, expired bool
	err = db.QueryRow(ctx, `SELECT chat_id,user_id,project_id,incident_key,started_at_ns,start_seen,finished,expired
FROM attention_actors WHERE run_id=$1 FOR UPDATE`, u.RunID).
		Scan(&actorChat, &actorUser, &actorProject, &actorKey, &actorStarted, &startSeen, &finished, &expired)
	if err != nil {
		return result, err
	}
	if actorChat != u.ChatID || actorUser != userID || actorProject != projectID || actorKey != u.Key || actorStarted != u.StartedAt.UnixNano() {
		return result, errors.New("attention actor identity is immutable")
	}
	result.Managed = true // Stale actors are still registered: never leak legacy alerts.
	duplicate := finished || (u.Phase == "start" && startSeen)
	if !duplicate {
		_, err = db.Exec(ctx, `UPDATE attention_actors SET start_seen=start_seen OR $2, finished=finished OR $3,
recovery_timeout_ns=CASE WHEN $2 THEN $4 ELSE recovery_timeout_ns END,
start_state=CASE WHEN $2 THEN $5 ELSE start_state END WHERE run_id=$1`,
			u.RunID, u.Phase == "start", u.Phase == "finish", int64(u.RecoveryTimeout), u.State)
		if err != nil {
			return result, err
		}
	}
	newer := actorStarted > row.StartedNS || (actorStarted == row.StartedNS && u.RunID > row.RunID)
	current := actorStarted == row.StartedNS && u.RunID == row.RunID
	if !duplicate && !expired && (newer || current) {
		if newer {
			row.RunID, row.StartedNS = u.RunID, actorStarted
		}
		state := u.State
		// A recovery that finishes without explicit resolution stops owning
		// suppression immediately (including an otherwise successful run).
		if u.Phase == "finish" && state == "" && row.State == "recovering" && row.OwnerRunID == u.RunID {
			state = "action_required"
		}
		if state == "recovering" || state == "action_required" {
			if row.State == "" || row.State == "resolved" {
				row.EpisodeID, row.OpenedAt, row.Notified = uuid.NewString(), now, false
			}
			row.State, row.UpdatedAt = state, now
			row.ChatID, row.TriggerID, row.OwnerRunID = u.ChatID, triggerID, u.RunID
			row.FailureClass, row.Detail, row.LeaseUntil = u.FailureClass, u.Detail, nil
			if state == "recovering" {
				deadline := u.StartedAt.Add(u.RecoveryTimeout)
				row.LeaseUntil = &deadline
				if !deadline.After(now) {
					row.State, row.FailureClass, row.Detail = "action_required", "recovery_expired", "Recovery timed out"
					row.LeaseUntil = nil
					if _, err = db.Exec(ctx, `UPDATE attention_actors SET expired=true WHERE run_id=$1`, u.RunID); err != nil {
						return result, err
					}
				}
			}
			if row.State == "action_required" && !row.Notified {
				result.Notify, row.Notified = true, true
			}
		} else if state == "resolved" && row.State != "" && row.State != "resolved" {
			row.State, row.UpdatedAt, row.LeaseUntil = "resolved", now, nil
		}
	}
	_, err = db.Exec(ctx, `UPDATE attention_incidents SET episode_id=$4,state=$5,chat_id=$6,trigger_id=$7,
failure_class=$8,detail=$9,opened_at=$10,updated_at=$11,lease_until=$12,actor_run_id=$13,actor_started_at_ns=$14,
owner_run_id=$15,notified=$16 WHERE user_id=$1 AND project_id=$2 AND incident_key=$3`,
		userID, projectID, u.Key, row.EpisodeID, row.State, row.ChatID, row.TriggerID, row.FailureClass, row.Detail, row.OpenedAt, row.UpdatedAt, row.LeaseUntil,
		row.RunID, row.StartedNS, row.OwnerRunID, row.Notified)
	return result, err
}

// ListAttentionIncidents projects expiry at database read time, without a
// scheduler or a write on the read path. Identity is rechecked against live
// chat/project ownership so orphaned or reassigned records cannot leak.
func ListAttentionIncidents(ctx context.Context, db orm.Context, userID string) ([]*core.AttentionIncident, error) {
	rows, err := db.Query(ctx, `SELECT a.user_id,a.project_id,a.incident_key,a.episode_id,
CASE WHEN a.state='recovering' AND a.lease_until<=statement_timestamp() THEN 'action_required' ELSE a.state END,
a.chat_id,a.trigger_id,
CASE WHEN a.state='recovering' AND a.lease_until<=statement_timestamp() THEN 'recovery_expired' ELSE a.failure_class END,
CASE WHEN a.state='recovering' AND a.lease_until<=statement_timestamp() THEN 'Recovery timed out' ELSE a.detail END,
a.opened_at,CASE WHEN a.state='recovering' AND a.lease_until<=statement_timestamp() THEN a.lease_until ELSE a.updated_at END,
CASE WHEN a.state='recovering' AND a.lease_until>statement_timestamp() THEN a.lease_until ELSE NULL END
FROM attention_incidents a JOIN projects p ON p.id=a.project_id AND p.user_id=a.user_id
JOIN chats c ON c.id=a.chat_id AND c.user_id=a.user_id AND c.project_id=a.project_id
WHERE a.user_id=$1 AND a.state IN ('recovering','action_required') ORDER BY a.opened_at,a.episode_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*core.AttentionIncident, 0)
	for rows.Next() {
		a := new(core.AttentionIncident)
		if err = rows.Scan(&a.UserID, &a.ProjectID, &a.Key, &a.EpisodeID, &a.State, &a.ChatID, &a.TriggerID, &a.FailureClass, &a.Detail, &a.OpenedAt, &a.UpdatedAt, &a.LeaseUntil); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AttentionManagedChats retains finished registrations even when stale. An
// unfinished actor needs an explicit start transition and an open durable
// backstop; a lost observer finish must still surface through legacy failure
// notifications. This is also the predicate used by unmanaged firing queries.
func AttentionManagedChats(ctx context.Context, db orm.Context, userID string, chatIDs []string) (map[string]bool, error) {
	out := make(map[string]bool)
	if len(chatIDs) == 0 {
		return out, nil
	}
	rows, err := db.Query(ctx, `SELECT DISTINCT a.chat_id FROM attention_actors a
JOIN chats c ON c.id=a.chat_id AND c.user_id=a.user_id AND c.project_id=a.project_id
JOIN projects p ON p.id=a.project_id AND p.user_id=a.user_id
WHERE a.user_id=$1 AND a.chat_id=ANY($2::text[])
AND (a.finished OR (a.start_seen AND a.start_state IN ('recovering','action_required') AND EXISTS (
SELECT 1 FROM attention_incidents i WHERE i.user_id=a.user_id AND i.project_id=a.project_id
AND i.incident_key=a.incident_key AND i.state IN ('recovering','action_required'))))`, userID, chatIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
