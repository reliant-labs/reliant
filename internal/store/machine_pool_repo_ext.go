package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Machine pools (dev-docs/design/machine-pools.md). A pool's members are not
// stored: they are the owner's daemons whose labels contain its selector, so
// every read here joins daemons by `labels @> selector`.

// MachinePoolExtra holds no computed fields; it exists for the generated
// struct's embed.
type MachinePoolExtra struct{}

func machinePoolToCore(row *MachinePool) (*core.MachinePool, error) {
	selector := map[string]string{}
	if len(row.Selector) > 0 {
		if err := json.Unmarshal(row.Selector, &selector); err != nil {
			return nil, fmt.Errorf("read machine pool %s selector: %w", row.ID, err)
		}
	}
	pool := &core.MachinePool{
		ID: row.ID, UserID: row.UserID, Name: row.Name, Selector: selector,
		MinMembers: int(row.MinMembers), MaxMembers: int(row.MaxMembers), MemberSize: row.MemberSize,
		MaxRunsPerMember: int(row.MaxRunsPerMember), MemberIdleTimeout: core.DefaultMemberIdleTimeout,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.MemberIdleTimeout != nil && *row.MemberIdleTimeout != "" {
		pool.MemberIdleTimeout = *row.MemberIdleTimeout
	}
	return pool, nil
}

func scanMachinePools(rows []*MachinePool) ([]*core.MachinePool, error) {
	out := make([]*core.MachinePool, 0, len(rows))
	for _, r := range rows {
		p, err := machinePoolToCore(r)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// GetMachinePoolForUser reads one of userID's pools by id;
// core.ErrMachinePoolNotFound when it is missing or another user's.
func GetMachinePoolForUser(ctx context.Context, db orm.Context, userID, id string) (*core.MachinePool, error) {
	var row MachinePool
	err := db.Bun().NewSelect().Model(&row).Where("id = ? AND user_id = ?", id, userID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrMachinePoolNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read machine pool %s: %w", id, err)
	}
	return machinePoolToCore(&row)
}

// GetMachinePoolByName reads userID's pool of that name.
func GetMachinePoolByName(ctx context.Context, db orm.Context, userID, name string) (*core.MachinePool, error) {
	var row MachinePool
	err := db.Bun().NewSelect().Model(&row).Where("user_id = ? AND name = ?", userID, name).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrMachinePoolNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read machine pool %q: %w", name, err)
	}
	return machinePoolToCore(&row)
}

// ListMachinePoolsForUser lists userID's pools by name.
func ListMachinePoolsForUser(ctx context.Context, db orm.Context, userID string) ([]*core.MachinePool, error) {
	var rows []*MachinePool
	if err := db.Bun().NewSelect().Model(&rows).Where("user_id = ?", userID).Order("name").Scan(ctx); err != nil {
		return nil, fmt.Errorf("list machine pools: %w", err)
	}
	return scanMachinePools(rows)
}

// PutMachinePool creates userID's pool named p.Name, or replaces its
// definition (keeping its id), and returns the stored row.
func PutMachinePool(ctx context.Context, db orm.Context, p *core.MachinePool, newID string, now time.Time) (*core.MachinePool, error) {
	selector, err := json.Marshal(p.Selector)
	if err != nil {
		return nil, fmt.Errorf("encode machine pool selector: %w", err)
	}
	idleTimeout := p.MemberIdleTimeout
	if idleTimeout == "" {
		idleTimeout = core.DefaultMemberIdleTimeout
	}
	var row MachinePool
	err = db.Bun().NewRaw(`
INSERT INTO machine_pools (id, user_id, name, selector, min_members, max_members, member_size, max_runs_per_member, member_idle_timeout, created_at, updated_at)
VALUES (?0, ?1, ?2, ?3::jsonb, ?4, ?5, ?6, ?7, ?9, ?8, ?8)
ON CONFLICT (user_id, name) DO UPDATE SET
    selector = EXCLUDED.selector,
    min_members = EXCLUDED.min_members,
    max_members = EXCLUDED.max_members,
    member_size = EXCLUDED.member_size,
    max_runs_per_member = EXCLUDED.max_runs_per_member,
    member_idle_timeout = EXCLUDED.member_idle_timeout,
    updated_at = EXCLUDED.updated_at
RETURNING `+columnsOf[MachinePool]("machine_pools"),
		newID, p.UserID, p.Name, string(selector), p.MinMembers, p.MaxMembers, p.MemberSize, p.MaxRunsPerMember, now, idleTimeout).Scan(ctx, &row)
	if err != nil {
		return nil, fmt.Errorf("write machine pool %q: %w", p.Name, err)
	}
	return machinePoolToCore(&row)
}

// DeleteMachinePoolByName deletes userID's pool of that name.
// core.ErrMachinePoolNotFound when there is none; core.ErrMachinePoolInUse
// while a trigger runs on it (triggers.machine_pool_id is ON DELETE RESTRICT).
func DeleteMachinePoolByName(ctx context.Context, db orm.Context, userID, name string) error {
	res, err := db.Bun().NewDelete().Model((*MachinePool)(nil)).Where("user_id = ? AND name = ?", userID, name).Exec(ctx)
	if err != nil {
		if orm.ConstraintName(err) == TriggerConstraintMachinePoolIDFkey {
			return core.ErrMachinePoolInUse
		}
		return fmt.Errorf("delete machine pool %q: %w", name, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.ErrMachinePoolNotFound
	}
	return nil
}

// LockMachinePool row-locks a pool for the enclosing transaction, so launches
// placing on it check and take member slots one at a time.
func LockMachinePool(ctx context.Context, db orm.Context, id string) error {
	var got string
	err := db.Bun().NewRaw(`SELECT id FROM machine_pools WHERE id = ?0 FOR UPDATE`, id).Scan(ctx, &got)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ErrMachinePoolNotFound
	}
	if err != nil {
		return fmt.Errorf("lock machine pool %s: %w", id, err)
	}
	return nil
}

// liveRootSQL is true for a chat (alias ch) whose newest root workflow is
// live: pending, active, or paused (core.WorkflowStatus.Live).
const liveRootSQL = `EXISTS (
    SELECT 1 FROM (
        SELECT w.state, w.stop_reason FROM workflows w
        WHERE w.chat_id = ch.id AND w.parent_id IS NULL
        ORDER BY w.created_at DESC LIMIT 1
    ) r
    WHERE r.state IN (1, 2) OR (r.state = 3 AND r.stop_reason = 3))`

// CountMachinePoolMemberLiveRuns counts the live runs pool placed on daemonID.
func CountMachinePoolMemberLiveRuns(ctx context.Context, db orm.Context, poolID, daemonID string) (int, error) {
	var n int
	err := db.Bun().NewRaw(`SELECT count(*) FROM chats ch WHERE ch.machine_pool_id = ?0 AND ch.active_daemon_id = ?1 AND `+liveRootSQL,
		poolID, daemonID).Scan(ctx, &n)
	if err != nil {
		return 0, fmt.Errorf("count live runs of pool %s on %s: %w", poolID, daemonID, err)
	}
	return n, nil
}

// CountTriggerCapacityHeldByMember is, per member, how many slots of one
// trigger-capacity scope the runs pool placed there still hold: admitted
// (capacity_order set), launched and not released, the same predicate
// CanLaunchTriggerCapacity counts as occupied.
func CountTriggerCapacityHeldByMember(ctx context.Context, db orm.Context, poolID, userID, projectID, workflowRef string) (map[string]int, error) {
	var rows []struct {
		DaemonID string `bun:"daemon_id"`
		Held     int    `bun:"held"`
	}
	err := db.Bun().NewRaw(`
SELECT ch.active_daemon_id AS daemon_id, count(*) AS held
FROM trigger_events e
JOIN chats ch ON ch.id = e.chat_id
WHERE e.user_id = ?1 AND e.capacity_project_id = ?2 AND e.capacity_workflow_ref = ?3
  AND e.capacity_order IS NOT NULL AND e.outcome = 'launched' AND NOT e.capacity_released
  AND ch.machine_pool_id = ?0 AND ch.active_daemon_id IS NOT NULL
GROUP BY ch.active_daemon_id`, poolID, userID, projectID, workflowRef).Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("count capacity held on members of pool %s: %w", poolID, err)
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.DaemonID] = r.Held
	}
	return out, nil
}

// ListMachinePoolMembers reads pool's members with what placement needs about
// them for projectID ("" for no project: Installed false, no catalog).
//
// Phase is derived exactly as a trigger's machine_state (namedTriggerSQL), so
// a pool and a trigger never disagree about a machine.
func ListMachinePoolMembers(ctx context.Context, db orm.Context, pool *core.MachinePool, projectID string) ([]core.MachinePoolMemberRow, error) {
	selector, err := json.Marshal(pool.Selector)
	if err != nil {
		return nil, fmt.Errorf("encode machine pool selector: %w", err)
	}
	var rows []struct {
		DaemonID        string     `bun:"daemon_id"`
		Name            string     `bun:"name"`
		Size            string     `bun:"size"`
		Phase           string     `bun:"phase"`
		Installed       bool       `bun:"installed"`
		Verified        bool       `bun:"verified"`
		WorkflowsJSON   *string    `bun:"workflows_json"`
		PresetsJSON     *string    `bun:"presets_json"`
		CatalogPushedAt *time.Time `bun:"pushed_at"`
		LiveRuns        int        `bun:"live_runs"`
	}
	err = db.Bun().NewRaw(`
SELECT d.id AS daemon_id,
    COALESCE(NULLIF(d.name, ''), d.hostname, '')::text AS name,
    COALESCE(d.size, '')::text AS size,
    CASE WHEN EXISTS (SELECT 1 FROM daemon_attachment a WHERE a.daemon_id = d.id
           AND a.user_id = d.user_id AND a.last_stream_activity > now() - interval '90 seconds') THEN 'connected'
         WHEN d.lifecycle_phase IN ('provisioning', 'cloning') THEN 'starting'
         WHEN d.lifecycle_phase IN ('suspending', 'suspended') THEN 'suspended'
         WHEN d.lifecycle_phase = 'failed' THEN 'failed'
         ELSE 'disconnected' END::text AS phase,
    EXISTS (SELECT 1 FROM project_daemons pd WHERE pd.project_id = ?2 AND pd.daemon_id = d.id
           AND pd.install_state = 'installed') AS installed,
    -- The checkout was seen on disk since the machine's current connection.
    -- A machine whose disk is the pod's own wakes with nothing on it while
    -- install_state still says installed, so "installed" alone is not enough
    -- to put a run on a member that just connected.
    EXISTS (SELECT 1 FROM project_daemons pd JOIN daemon_attachment a ON a.daemon_id = pd.daemon_id
           WHERE pd.project_id = ?2 AND pd.daemon_id = d.id AND pd.install_state = 'installed'
           AND pd.verified_at >= a.attached_at) AS verified,
    c.workflows_json, c.presets_json, c.pushed_at,
    (SELECT count(*) FROM chats ch WHERE ch.machine_pool_id = ?3 AND ch.active_daemon_id = d.id AND `+liveRootSQL+`) AS live_runs
FROM daemons d
LEFT JOIN project_machine_catalogs c ON c.project_id = ?2 AND c.daemon_id = d.id
WHERE d.user_id = ?0 AND d.labels @> ?1::jsonb
ORDER BY name, d.id`, pool.UserID, string(selector), projectID, pool.ID).Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("list members of machine pool %s: %w", pool.Name, err)
	}
	out := make([]core.MachinePoolMemberRow, len(rows))
	for i, r := range rows {
		out[i] = core.MachinePoolMemberRow(r)
	}
	return out, nil
}

// SetDaemonLabels replaces a machine's durable labels. False when userID has
// no such machine.
func SetDaemonLabels(ctx context.Context, db orm.Context, userID, daemonID string, labels map[string]string) (bool, error) {
	if labels == nil {
		labels = map[string]string{}
	}
	encoded, err := json.Marshal(labels)
	if err != nil {
		return false, fmt.Errorf("encode machine labels: %w", err)
	}
	res, err := db.Bun().NewRaw(`UPDATE daemons SET labels = ?0::jsonb, updated_at = now() WHERE id = ?1 AND user_id = ?2`,
		string(encoded), daemonID, userID).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("set labels of machine %s: %w", daemonID, err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// daemonLabels decodes a daemons row's labels column. A value that is not a
// string map (never written by this code) reads as no labels: the machine is
// then in no pool, which fails closed.
func daemonLabels(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]string{}
	}
	return out
}
