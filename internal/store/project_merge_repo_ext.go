package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/orm"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Merging one of a person's projects (the source) into another (the target):
// typically two projects of one repository, made on two machines before
// projects were matched by repository (projects.remote_key). MergeProjects is
// one transaction: every row of the source moves onto the target, and the
// source, left empty, is scheduled for deletion exactly as DeleteProject
// schedules it (ScheduleProjectDeletion), so RestoreProject brings it back,
// empty, within the window. PreviewProjectMerge reads the same counts and
// refusals without writing.
//
// # What happens to each table
//
// projectMergeColumns names every column that holds a project id, and what the
// merge does with it. TestProjectMergeNamesEveryProjectColumn fails when the
// migrated schema has a project_id column (or a foreign key to projects) the
// list does not name, so a new table is decided here before it ships. The
// policies:
//
//   - move: the source's rows become the target's (UPDATE ... SET project_id).
//   - merge-dedupe: the table has a unique key that includes the project. A
//     source row whose key the target already holds is dropped, keeping the
//     target's (the target's config, catalog or setting wins); the rest move.
//   - refuse: the rows move, and a conflict refuses the merge with a
//     SAME_MACHINE blocker instead of dropping anything.
//   - left in place: the rows keep naming the source, on purpose. They are
//     ledgers, logs or fences that record what happened where; they go when
//     the source is purged, or outlive it as their table already allows.
//
// The repos and worktrees of the source carry ids other tables point at too:
// projectMergeRepoRefs and projectMergeWorktreeRefs name those, held complete
// by the same test.
//
// # Locks
//
// ReadCommitted, under the locks a project move takes (MoveProjectOrg), in
// its order: the owner's org lock, then both project rows FOR UPDATE in id
// order, then every chat of both FOR NO KEY UPDATE in id order, then the
// source's triggers. FOR UPDATE on a project conflicts with the key share
// every write that adds to it takes (a chat, worktree, run, trigger, repo,
// checkout, config or catalog row: the foreign keys and the
// project_delete_admission triggers), and the chat locks are the ones a run's
// admission takes, so nothing joins either project, and no run starts, between
// the blocker check and the commit. A write to a table with no foreign key to
// projects (a setting, a preset) that lands concurrently stays with the source
// and goes with it; nothing running depends on one. Serializable would turn
// those races into aborted merges without making the result more correct.

// projectMergePolicy is what a merge does with one column holding a project id.
type projectMergePolicy string

const (
	projectMergeMove   projectMergePolicy = "move"
	projectMergeDedupe projectMergePolicy = "merge-dedupe"
	projectMergeRefuse projectMergePolicy = "refuse"
	projectMergeLeave  projectMergePolicy = "left in place"
)

// projectMergeColumn is one column holding a project id and what a merge does
// with it.
type projectMergeColumn struct {
	table, column string
	policy        projectMergePolicy
	why           string
	// steps carry the policy out, in order, ?0 being the source and ?1 the
	// target. run does it instead when it needs more than SQL.
	steps []string
	run   func(ctx context.Context, m *projectMergeTx) error
	// other: the source's rows count in ProjectMergeCounts.OtherRows (the
	// headline tables have counts of their own).
	other bool
}

// projectMergeMoveStep moves every row of table from the source to the target.
func projectMergeMoveStep(table string) string {
	return `UPDATE ` + table + ` SET project_id = ?1 WHERE project_id = ?0`
}

// projectMergeDedupeSteps drops each source row of table whose key (the
// project and keys) the target already holds, then moves the rest.
func projectMergeDedupeSteps(table string, keys ...string) []string {
	match := ""
	for _, k := range keys {
		match += ` AND x.` + k + ` = s.` + k
	}
	return []string{
		`DELETE FROM ` + table + ` s WHERE s.project_id = ?0
			AND EXISTS (SELECT 1 FROM ` + table + ` x WHERE x.project_id = ?1` + match + `)`,
		projectMergeMoveStep(table),
	}
}

// projectMergeRepoPairs pairs each source repo with the target's repo at the
// same relative path (repos are unique per (project, relative_path)): the
// source's is a duplicate of the target's.
const projectMergeRepoPairs = `
	SELECT s.id AS src, t.id AS dst FROM repos s
	JOIN repos t ON t.project_id = ?1 AND t.relative_path = s.relative_path
	WHERE s.project_id = ?0`

// projectMergeColumns is every column holding a project id, in the order the
// merge carries it out. Order matters where one step reads what another
// moves: chat_projects reads the source's chats before they move, and
// project_daemon_detachments reads the checkouts after they move.
var projectMergeColumns = []projectMergeColumn{
	{
		table: "repos", column: "project_id", policy: projectMergeDedupe,
		why: "A source repo at a relative path the target also has is the same repo: what points at it " +
			"(projectMergeRepoRefs) is re-pointed to the target's, and it is dropped. The rest move.",
		steps: []string{
			// A forge app the target's repo already records at that subpath
			// is kept; the duplicate's goes with it (ON DELETE CASCADE).
			`UPDATE repo_forge_apps a SET repo_id = p.dst, updated_at = NOW()
			FROM (` + projectMergeRepoPairs + `) p
			WHERE a.repo_id = p.src
			  AND NOT EXISTS (SELECT 1 FROM repo_forge_apps b WHERE b.repo_id = p.dst AND b.subpath = a.subpath)`,
			// A recovery captures the repos of one project's worktree, so it
			// cannot already hold the target's twin.
			`UPDATE worktree_recovery_checkouts c SET repo_id = p.dst
			FROM (` + projectMergeRepoPairs + `) p
			WHERE c.repo_id = p.src`,
			`DELETE FROM repos s USING repos t
			WHERE s.project_id = ?0 AND t.project_id = ?1 AND t.relative_path = s.relative_path`,
			`UPDATE repos SET project_id = ?1, updated_at = NOW() WHERE project_id = ?0`,
		},
	},
	{
		table: "worktrees", column: "project_id", policy: projectMergeMove,
		why: "Branch worktrees move, archived ones included, so every chat keeps its worktree row. A live " +
			"name the target already uses is renamed with a suffix (the path and branch on disk are " +
			"unchanged). The source's main worktree is its main checkout: when the target has a live " +
			"main worktree, what points at the source's (projectMergeWorktreeRefs) points at the " +
			"target's instead, and the source's stays with the source, which a restore brings back; " +
			"when the target has none, the source's moves and becomes it.",
		run: mergeProjectWorktrees,
	},
	{
		table: "worktree_recoveries", column: "project_id", policy: projectMergeMove,
		why:   "A recovery belongs to its source worktree, which moved. One still finalizing refuses the merge.",
		steps: []string{projectMergeMoveStep("worktree_recoveries")}, other: true,
	},
	{
		table: "chat_projects", column: "project_id", policy: projectMergeDedupe,
		why: "An attachment of the source becomes one of the target. One the chat already has of the " +
			"target is kept, and an attachment of the chat's own primary project is dropped (the primary " +
			"is never a chat_projects row): a chat of either project attaching the other.",
		steps: []string{
			`DELETE FROM chat_projects cp WHERE cp.project_id = ?0 AND (
				EXISTS (SELECT 1 FROM chats c WHERE c.id = cp.chat_id AND c.project_id IN (?0, ?1))
				OR EXISTS (SELECT 1 FROM chat_projects x WHERE x.chat_id = cp.chat_id AND x.project_id = ?1))`,
			`DELETE FROM chat_projects cp WHERE cp.project_id = ?1
				AND EXISTS (SELECT 1 FROM chats c WHERE c.id = cp.chat_id AND c.project_id = ?0)`,
			projectMergeMoveStep("chat_projects"),
		},
		other: true,
	},
	{
		table: "chats", column: "project_id", policy: projectMergeMove,
		why: "Every chat, archived ones included. Neither updated_at nor last_active moves: a merge is not " +
			"activity in a chat. Both projects are in one org, so org_id and shares stay as they are.",
		steps: []string{projectMergeMoveStep("chats")},
	},
	{
		table: "plans", column: "project_id", policy: projectMergeMove,
		why:   "Plans follow their chats' threads (as MoveChatToProject moves them).",
		steps: []string{projectMergeMoveStep("plans")}, other: true,
	},
	{
		table: "chat_pr_links", column: "project_id", policy: projectMergeMove,
		why:   "The pull requests a chat produced follow the chat.",
		steps: []string{projectMergeMoveStep("chat_pr_links")}, other: true,
	},
	{
		table: "background_processes", column: "project_id", policy: projectMergeMove,
		why:   "Processes (and their output) of the project follow it. A running one refuses the merge.",
		steps: []string{projectMergeMoveStep("background_processes")}, other: true,
	},
	{
		table: "hosted_http_action_tickets", column: "project_id", policy: projectMergeMove,
		why:   "A ticket belongs to a run of a chat that moved.",
		steps: []string{projectMergeMoveStep("hosted_http_action_tickets")}, other: true,
	},
	{
		table: "triggers", column: "project_id", policy: projectMergeMove,
		why: "Each trigger moves with its state (enabled or paused) and its history, recorded as a " +
			"project_merged change. A name the target already uses (unique per user and project) is " +
			"renamed with a suffix. Their schedules are converged after the commit.",
		run: mergeProjectTriggers,
	},
	{
		table: "workflow_catalog_snapshots", column: "project_id", policy: projectMergeMove,
		why:   "Runs of the moved chats pin these snapshots; no key includes the project.",
		steps: []string{projectMergeMoveStep("workflow_catalog_snapshots")}, other: true,
	},
	{
		table: "checkout_teardowns", column: "project_id", policy: projectMergeRefuse,
		why: "A remove or reclone of a checkout follows the checkout. A pending one is a checkout on " +
			"that machine: when the target is on it too, SAME_MACHINE refuses (one pending teardown per " +
			"project and machine).",
		steps: []string{projectMergeMoveStep("checkout_teardowns")}, other: true,
	},
	{
		table: "project_daemons", column: "project_id", policy: projectMergeRefuse,
		why: "Each checkout becomes the target's on its machine, at its path. A machine both projects " +
			"are checked out on refuses the merge (SAME_MACHINE): a project has one checkout per machine. " +
			"A detachment of the target from a machine the source's checkout moves onto is cleared, as " +
			"attaching a checkout clears one: a live checkout is never beside a detachment.",
		steps: []string{
			`DELETE FROM project_daemon_detachments d WHERE d.project_id = ?1
				AND EXISTS (SELECT 1 FROM project_daemons pd WHERE pd.project_id = ?0 AND pd.daemon_id = d.daemon_id)`,
			projectMergeMoveStep("project_daemons"),
		},
	},
	{
		table: "project_daemon_detachments", column: "project_id", policy: projectMergeDedupe,
		why: "A detachment keeps the source off a machine; it keeps the target off it, unless the target " +
			"has its own detachment there or a checkout there (moved or its own), which wins.",
		steps: []string{
			`DELETE FROM project_daemon_detachments s WHERE s.project_id = ?0 AND (
				EXISTS (SELECT 1 FROM project_daemon_detachments x WHERE x.project_id = ?1 AND x.daemon_id = s.daemon_id)
				OR EXISTS (SELECT 1 FROM project_daemons pd WHERE pd.project_id = ?1 AND pd.daemon_id = s.daemon_id))`,
			projectMergeMoveStep("project_daemon_detachments"),
		},
		other: true,
	},
	{
		table: "project_configs", column: "project_id", policy: projectMergeDedupe,
		why:   "One config record per project: the target's is kept; the source's moves only when the target has none.",
		steps: projectMergeDedupeSteps("project_configs"), other: true,
	},
	{
		table: "project_machine_catalogs", column: "project_id", policy: projectMergeDedupe,
		why:   "A machine's synced catalog per (project, machine): the target's is kept.",
		steps: projectMergeDedupeSteps("project_machine_catalogs", "daemon_id"), other: true,
	},
	{
		table: "settings", column: "project_id", policy: projectMergeDedupe,
		why:   "Unique per (user, project, key): the target's value is kept.",
		steps: projectMergeDedupeSteps("settings", "user_id", "key"), other: true,
	},
	{
		table: "presets", column: "project_id", policy: projectMergeDedupe,
		why:   "Unique per (user, project, slug): the target's preset is kept.",
		steps: projectMergeDedupeSteps("presets", "user_id", "slug"), other: true,
	},
	{
		table: "command_favorites", column: "project_id", policy: projectMergeDedupe,
		why:   "Unique per (user, project, command): the target's favorite is kept.",
		steps: projectMergeDedupeSteps("command_favorites", "user_id", "command_key"), other: true,
	},

	{
		table: "user_updates", column: "project_id", policy: projectMergeLeave,
		why: "The user's update stream, already delivered: a log, not content, and on a long-lived " +
			"project hundreds of thousands of rows the merge would hold its locks over. They go with " +
			"the source's purge.",
	},
	{
		table: "org_updates", column: "project_id", policy: projectMergeLeave,
		why: "The org feed's append-only log of what happened, where.",
	},
	{
		table: "llm_call_usage", column: "project_id", policy: projectMergeLeave,
		why: "A usage ledger: where each call ran when it ran (MoveChatToProject leaves it too).",
	},
	{
		table: "tool_call_usage", column: "project_id", policy: projectMergeLeave,
		why: "A usage ledger, as llm_call_usage; it is keyed by chat for everything else.",
	},
	{
		table: "project_org_moves", column: "project_id", policy: projectMergeLeave,
		why: "The ledger of the source's org moves. An open move of either project refuses the merge.",
	},
	{
		table: "project_daemon_org_cleanups", column: "project_id", policy: projectMergeLeave,
		why: "An audit of past machine-org cleanups, kept whole for their recovery procedure; nothing reads it.",
	},
	{
		table: "attention_incidents", column: "project_id", policy: projectMergeLeave,
		why: "Attention fences: their key includes the project, and a resolved one stays as a high-water " +
			"mark against a late completion. Re-keyed, two fences would collide; left, they stay what they fenced.",
	},
	{
		table: "attention_actors", column: "project_id", policy: projectMergeLeave,
		why: "Per-run attention receipts, read with their incident (attention_incidents).",
	},
	{
		table: "trigger_events", column: "capacity_project_id", policy: projectMergeLeave,
		why: "forge:immutable: the capacity bucket a firing was admitted to. A firing of the source that " +
			"still holds or waits for capacity refuses the merge; released ones are history.",
	},
}

// projectMergeRef is a column holding a repo or worktree id, and what a merge
// does with it.
type projectMergeRef struct {
	table, column string
	why           string
}

// projectMergeRepoRefs is every column holding a repo id (repos step).
var projectMergeRepoRefs = []projectMergeRef{
	{"repo_forge_apps", "repo_id", "re-pointed to the target's twin repo, unless it records that subpath already"},
	{"worktree_recovery_checkouts", "repo_id", "re-pointed to the target's twin repo"},
}

// projectMergeWorktreeRefs is every column holding a worktree id. The main
// worktree re-point (mergeProjectWorktrees) runs projectMergeMainRepoint over
// those that re-point.
var projectMergeWorktreeRefs = []projectMergeRef{
	{"chats", "worktree_id", "re-pointed from the source's main worktree to the target's"},
	{"threads", "worktree_id", "re-pointed from the source's main worktree to the target's"},
	{"triggers", "worktree_id", "re-pointed from the source's main worktree to the target's"},
	{"background_processes", "worktree_id", "re-pointed from the source's main worktree to the target's"},
	{"user_updates", "worktree_id", "left in place: the update stream (user_updates above)"},
	{"worktree_recoveries", "source_worktree_id", "never a main worktree, so it follows its worktree's move"},
}

// projectMergeMainRepoint re-points what names the source's main worktree to
// the target's live main worktree (?1).
var projectMergeMainRepoint = []string{
	`UPDATE chats SET worktree_id = ?1 WHERE worktree_id IN (SELECT id FROM worktrees WHERE project_id = ?0 AND is_main)`,
	`UPDATE threads SET worktree_id = ?1 WHERE worktree_id IN (SELECT id FROM worktrees WHERE project_id = ?0 AND is_main)`,
	`UPDATE triggers SET worktree_id = ?1 WHERE worktree_id IN (SELECT id FROM worktrees WHERE project_id = ?0 AND is_main)`,
	`UPDATE background_processes SET worktree_id = ?1 WHERE worktree_id IN (SELECT id FROM worktrees WHERE project_id = ?0 AND is_main)`,
}

// projectMergeTx is one merge's transaction state.
type projectMergeTx struct {
	db              orm.Context
	source, target  string
	movedTriggerIDs []string
}

func (m *projectMergeTx) exec(ctx context.Context, query string, args ...any) error {
	if len(args) == 0 {
		args = []any{m.source, m.target}
	}
	_, err := m.db.Bun().NewRaw(query, args...).Exec(ctx)
	return err
}

// projectMergeSide is one project of a merge, as the blockers name it.
type projectMergeSide struct {
	ID   string `bun:"id"`
	Name string `bun:"name"`
}

// PreviewProjectMerge reads what merging sourceID into targetID would move
// and what refuses it now, without writing or locking: MergeProjects decides
// again as it commits. It checks no owner; the caller admitted both.
func PreviewProjectMerge(ctx context.Context, db orm.Context, sourceID, targetID string) (*core.ProjectMergePreview, error) {
	if sourceID == targetID {
		return nil, core.ErrProjectMergeSameProject
	}
	source, target, err := readProjectMergeSides(ctx, db, sourceID, targetID)
	if err != nil {
		return nil, err
	}
	counts, err := projectMergeCounts(ctx, db, sourceID)
	if err != nil {
		return nil, err
	}
	blockers, err := projectMergeBlockers(ctx, db, source, target)
	if err != nil {
		return nil, err
	}
	return &core.ProjectMergePreview{Counts: counts, Blockers: blockers}, nil
}

// MergeProjects folds m.SourceID into m.TargetID in one ReadCommitted
// transaction (see the top of this file) and schedules the source's deletion
// at m.DeleteAfter, recording m.ActorUserID as who deleted it.
//
// Both projects must still be m.Decided's owner's, in m.Decided's org, and not
// scheduled for deletion: a project that is gone, another owner's or scheduled
// is core.ErrProjectNotFound; an org that changed since the decision is
// ErrOrgChanged. With anything in the way (projectMergeBlockers, read under
// the locks) it writes nothing and returns core.ErrProjectMergeBlocked with
// the result's Blockers set.
func MergeProjects(ctx context.Context, db orm.Context, m core.ProjectMerge) (*core.ProjectMergeResult, error) {
	if m.SourceID == "" || m.TargetID == "" || m.Decided.OwnerUserID == "" || m.ActorUserID == "" {
		return nil, fmt.Errorf("project merge: source, target, owner and actor are required")
	}
	if m.SourceID == m.TargetID {
		return nil, core.ErrProjectMergeSameProject
	}
	var result *core.ProjectMergeResult
	err := db.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var err error
		result, err = mergeProjects(ctx, db, m)
		return err
	})
	if err != nil && !errors.Is(err, core.ErrProjectMergeBlocked) {
		return nil, err
	}
	return result, err
}

func mergeProjects(ctx context.Context, db orm.Context, m core.ProjectMerge) (*core.ProjectMergeResult, error) {
	// A project that is gone is NotFound, not a changed org; the read takes
	// no lock, so it may precede the org lock.
	var present []string
	if err := db.Bun().NewRaw(`SELECT id FROM projects WHERE id IN (?0, ?1)`, m.SourceID, m.TargetID).
		Scan(ctx, &present); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read the projects: %w", err)
	}
	if len(present) != 2 {
		return nil, core.ErrProjectNotFound
	}
	// The source's org is re-read under the lock; the target's is compared
	// with it below, under the project row locks, so a target in another
	// org is refused as such, not as a changed org.
	if err := LockUserOrgs(ctx, db, m.Decided.OwnerUserID,
		OrgRead{Table: "projects", ID: m.SourceID, Org: m.Decided.OrgID}); err != nil {
		return nil, err
	}

	// FOR UPDATE, not FOR NO KEY UPDATE: it must conflict with the key share
	// each write that adds to a project holds (project_schedule.go).
	var locked []struct {
		ID        string  `bun:"id"`
		Name      string  `bun:"name"`
		UserID    string  `bun:"user_id"`
		OrgID     *string `bun:"org_id"`
		Scheduled bool    `bun:"scheduled"`
	}
	if err := db.Bun().NewRaw(`SELECT id, name, user_id, org_id, delete_after IS NOT NULL AS scheduled
		FROM projects WHERE id IN (?0, ?1) ORDER BY id FOR UPDATE`, m.SourceID, m.TargetID).
		Scan(ctx, &locked); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("lock the projects: %w", err)
	}
	if len(locked) != 2 {
		return nil, core.ErrProjectNotFound
	}
	var source, target projectMergeSide
	for _, p := range locked {
		if p.UserID != m.Decided.OwnerUserID || p.Scheduled {
			return nil, core.ErrProjectNotFound
		}
		if p.ID == m.SourceID {
			source = projectMergeSide{ID: p.ID, Name: p.Name}
		} else {
			target = projectMergeSide{ID: p.ID, Name: p.Name}
		}
	}
	if !sameOrg(locked[0].OrgID, locked[1].OrgID) {
		return nil, core.ErrProjectMergeOtherOrg
	}

	// Every chat of both, in id order: the lock a run's admission takes
	// (withChatWorktreeAdmission, and the workflows admission trigger), so
	// no run starts between the check below and the commit.
	if _, err := db.Bun().NewRaw(`SELECT count(*) FROM (
		SELECT id FROM chats WHERE project_id IN (?0, ?1) ORDER BY id FOR NO KEY UPDATE) locked`,
		m.SourceID, m.TargetID).Exec(ctx); err != nil {
		return nil, fmt.Errorf("lock the projects' chats: %w", err)
	}
	blockers, err := projectMergeBlockers(ctx, db, source, target)
	if err != nil {
		return nil, err
	}
	if len(blockers) > 0 {
		return &core.ProjectMergeResult{Blockers: blockers}, core.ErrProjectMergeBlocked
	}
	counts, err := projectMergeCounts(ctx, db, m.SourceID)
	if err != nil {
		return nil, err
	}

	tx := &projectMergeTx{db: db, source: m.SourceID, target: m.TargetID}
	for _, col := range projectMergeColumns {
		if col.run != nil {
			if err := col.run(ctx, tx); err != nil {
				return nil, fmt.Errorf("merge %s: %w", col.table, err)
			}
			continue
		}
		for _, step := range col.steps {
			if err := tx.exec(ctx, step); err != nil {
				return nil, fmt.Errorf("merge %s: %w", col.table, recoveryAdmissionError(projectDeletedError(err)))
			}
		}
	}

	// The target was active when the source last was: the sidebar orders by it.
	if _, err := db.Bun().NewRaw(`UPDATE projects SET updated_at = NOW(),
		last_active = GREATEST(last_active, (SELECT s.last_active FROM projects s WHERE s.id = ?0))
		WHERE id = ?1`, m.SourceID, m.TargetID).Exec(ctx); err != nil {
		return nil, fmt.Errorf("touch the target project: %w", err)
	}

	// The source, now empty, is scheduled as DeleteProject schedules it. It
	// has no trigger left to pause.
	deletion, err := ScheduleProjectDeletion(ctx, db, m.SourceID, m.ActorUserID, m.DeleteAfter, m.Decided)
	if err != nil {
		return nil, fmt.Errorf("schedule the merged project's deletion: %w", err)
	}
	return &core.ProjectMergeResult{
		Counts:          counts,
		MovedTriggerIDs: tx.movedTriggerIDs,
		DeleteAfter:     deletion.DeleteAfter,
	}, nil
}

// readProjectMergeSides reads both projects' names, for the blockers'
// sentences. A missing project is core.ErrProjectNotFound.
func readProjectMergeSides(ctx context.Context, db orm.Context, sourceID, targetID string) (source, target projectMergeSide, err error) {
	var rows []projectMergeSide
	if err := db.Bun().NewRaw(`SELECT id, name FROM projects WHERE id IN (?0, ?1)`, sourceID, targetID).
		Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return source, target, fmt.Errorf("read the projects: %w", err)
	}
	for _, r := range rows {
		if r.ID == sourceID {
			source = r
		} else {
			target = r
		}
	}
	if source.ID == "" || target.ID == "" {
		return source, target, core.ErrProjectNotFound
	}
	return source, target, nil
}

// projectMergeHeadlineCountsSQL counts the source's rows of the tables
// ProjectMergeCounts names.
const projectMergeHeadlineCountsSQL = `
SELECT
    (SELECT count(*) FROM chats WHERE project_id = ?0) AS chats,
    (SELECT count(*) FROM worktrees WHERE project_id = ?0) AS worktrees,
    (SELECT count(*) FROM triggers WHERE project_id = ?0) AS triggers,
    (SELECT count(*) FROM repos WHERE project_id = ?0) AS repos`

// projectMergeOtherCountsSQL sums the source's rows of every other table the
// merge moves or folds (projectMergeColumn.other). The table names are this
// file's constants, never input.
var projectMergeOtherCountsSQL = func() string {
	var terms []string
	for _, c := range projectMergeColumns {
		if c.other {
			terms = append(terms, `(SELECT count(*) FROM `+c.table+` WHERE `+c.column+` = ?0)`)
		}
	}
	return `SELECT ` + strings.Join(terms, " + ")
}()

// projectMergeCounts reads what a merge moves from sourceID.
func projectMergeCounts(ctx context.Context, db orm.Context, sourceID string) (core.ProjectMergeCounts, error) {
	var c core.ProjectMergeCounts
	var head struct {
		Chats     int `bun:"chats"`
		Worktrees int `bun:"worktrees"`
		Triggers  int `bun:"triggers"`
		Repos     int `bun:"repos"`
	}
	if err := db.Bun().NewRaw(projectMergeHeadlineCountsSQL, sourceID).Scan(ctx, &head); err != nil {
		return c, fmt.Errorf("count what the merge moves: %w", err)
	}
	c.Chats, c.Worktrees, c.Triggers, c.Repos = head.Chats, head.Worktrees, head.Triggers, head.Repos
	if err := db.Bun().NewRaw(projectMergeOtherCountsSQL, sourceID).Scan(ctx, &c.OtherRows); err != nil {
		return c, fmt.Errorf("count the other rows the merge moves: %w", err)
	}
	var checkouts []struct {
		DaemonID     string `bun:"daemon_id"`
		InstallState string `bun:"install_state"`
	}
	if err := db.Bun().NewRaw(`SELECT daemon_id, install_state FROM project_daemons WHERE project_id = ?0 ORDER BY cloned_at, daemon_id`,
		sourceID).Scan(ctx, &checkouts); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return c, fmt.Errorf("read the project's checkouts: %w", err)
	}
	for _, pd := range checkouts {
		c.Checkouts = append(c.Checkouts, core.ProjectMergeCheckout{DaemonID: pd.DaemonID, InstallState: core.ProjectInstallState(pd.InstallState)})
	}
	return c, nil
}

// projectMergeSameMachinesSQL is every machine both projects are checked out
// on: a project_daemons row, or a pending teardown of one (a remove or
// reclone in flight, which still holds the checkout's place).
const projectMergeSameMachinesSQL = `
WITH s AS (
    SELECT daemon_id FROM project_daemons WHERE project_id = ?0
    UNION SELECT daemon_id FROM checkout_teardowns WHERE project_id = ?0 AND state = 'pending'
), t AS (
    SELECT daemon_id FROM project_daemons WHERE project_id = ?1
    UNION SELECT daemon_id FROM checkout_teardowns WHERE project_id = ?1 AND state = 'pending'
)
SELECT s.daemon_id, COALESCE(NULLIF(d.name, ''), d.hostname, '') AS name
FROM s JOIN t ON t.daemon_id = s.daemon_id
LEFT JOIN daemons d ON d.id = s.daemon_id
ORDER BY s.daemon_id`

// projectMergeQueuedFiringsSQL counts the source's trigger firings that hold
// or wait for a capacity slot (trigger_capacity): queued work, admitted to
// the source's capacity bucket, which a merge cannot re-key
// (capacity_project_id is immutable).
const projectMergeQueuedFiringsSQL = `
SELECT count(*) FROM trigger_events
WHERE capacity_project_id = ?0 AND capacity_order IS NOT NULL AND outcome = 'launched' AND NOT capacity_released`

// projectMergeBlockers reads what refuses merging source into target now:
// running work in either (the org move's quiescence predicate, an open org
// move, a worktree of the source finalizing a recovery, a queued firing of
// its triggers), and every machine both are checked out on. Read under
// MergeProjects' locks it is the commit's check; alone, the preview's.
func projectMergeBlockers(ctx context.Context, db orm.Context, source, target projectMergeSide) ([]core.ProjectMergeBlocker, error) {
	var out []core.ProjectMergeBlocker
	work := func(msg, chatID string) {
		out = append(out, core.ProjectMergeBlocker{Kind: core.ProjectMergeRunningWork, Message: msg, ChatID: chatID})
	}
	for _, p := range []projectMergeSide{source, target} {
		b, err := ProjectOrgMoveBlockers(ctx, db, p.ID)
		if err != nil {
			return nil, err
		}
		for _, c := range b.Chats {
			work(projectMergeChatSentence(c, p.Name), c.ChatID)
		}
		if more := b.ChatsTotal - len(b.Chats); more > 0 {
			work(fmt.Sprintf("%s in %q %s running or waiting; stop them, then merge", plural(more, "more chat", "more chats"), p.Name, isAre(more)), "")
		}
		if b.ChatsTotal == 0 && b.LiveRuns > 0 {
			work(fmt.Sprintf("%s of chats in %q %s running (sub-agents or background runs); wait for them to finish or stop them, then merge",
				plural(b.LiveRuns, "run", "runs"), p.Name, isAre(b.LiveRuns)), "")
		}
		if b.BackgroundProcesses > 0 {
			work(fmt.Sprintf("%s of %q %s running; stop them, then merge",
				plural(b.BackgroundProcesses, "background process", "background processes"), p.Name, isAre(b.BackgroundProcesses)), "")
		}
	}

	var moving []string
	if err := db.Bun().NewRaw(`SELECT project_id FROM project_org_moves WHERE project_id IN (?0, ?1) AND state IN (?2, ?3, ?4) ORDER BY project_id`,
		source.ID, target.ID, string(core.ProjectOrgMoveStatePreparing), string(core.ProjectOrgMoveStatePrepared), string(core.ProjectOrgMoveStateMoved),
	).Scan(ctx, &moving); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read the projects' open org moves: %w", err)
	}
	for _, id := range moving {
		name := source.Name
		if id == target.ID {
			name = target.Name
		}
		work(fmt.Sprintf("%q is being moved to another organization; wait for the move to finish, then merge", name), "")
	}

	var recovering int
	if err := db.Bun().NewRaw(`SELECT count(*) FROM worktrees WHERE project_id = ?0 AND recovery_finalizing_id IS NOT NULL`, source.ID).
		Scan(ctx, &recovering); err != nil {
		return nil, fmt.Errorf("read the project's recovering worktrees: %w", err)
	}
	if recovering > 0 {
		work(fmt.Sprintf("%s of %q %s being recovered; wait for the recovery to finish, then merge",
			plural(recovering, "worktree", "worktrees"), source.Name, isAre(recovering)), "")
	}

	var queued int
	if err := db.Bun().NewRaw(projectMergeQueuedFiringsSQL, source.ID).Scan(ctx, &queued); err != nil {
		return nil, fmt.Errorf("count the project's queued trigger runs: %w", err)
	}
	if queued > 0 {
		work(fmt.Sprintf("%s of %q's triggers %s queued or running; wait for them to finish, then merge",
			plural(queued, "run", "runs"), source.Name, isAre(queued)), "")
	}

	var machines []struct {
		DaemonID string `bun:"daemon_id"`
		Name     string `bun:"name"`
	}
	if err := db.Bun().NewRaw(projectMergeSameMachinesSQL, source.ID, target.ID).Scan(ctx, &machines); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read the projects' machines: %w", err)
	}
	for _, mc := range machines {
		name := mc.Name
		if name == "" {
			name = "one machine"
		}
		out = append(out, core.ProjectMergeBlocker{
			Kind: core.ProjectMergeSameMachine,
			Message: fmt.Sprintf("both projects are checked out on %s, in two folders, and a project has one checkout per machine; detach %q or %q from %s, then merge",
				name, source.Name, target.Name, name),
			DaemonID: mc.DaemonID,
		})
	}
	return out, nil
}

// projectMergeChatSentence says why a chat of project refuses a merge.
func projectMergeChatSentence(c core.ProjectOrgMoveBlockedChat, project string) string {
	chat := "a chat"
	if c.Title != "" {
		chat = fmt.Sprintf("the chat %q", c.Title)
	}
	switch reliantv1.ChatActivity(c.Activity) {
	case reliantv1.ChatActivity_CHAT_ACTIVITY_AWAITING_INPUT:
		return fmt.Sprintf("%s in %q is waiting for your answer; answer or stop it, then merge", chat, project)
	case reliantv1.ChatActivity_CHAT_ACTIVITY_PAUSED:
		return fmt.Sprintf("%s in %q is paused; resume it to the end or stop it, then merge", chat, project)
	case reliantv1.ChatActivity_CHAT_ACTIVITY_QUEUED_FOR_MACHINE:
		return fmt.Sprintf("%s in %q has a message queued for its machine; wait for it to run or stop it, then merge", chat, project)
	default:
		return fmt.Sprintf("%s in %q is running; wait for it to finish or stop it, then merge", chat, project)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// mergeProjectWorktrees carries out the worktrees policy (projectMergeColumns).
func mergeProjectWorktrees(ctx context.Context, m *projectMergeTx) error {
	var targetMain []string
	if err := m.db.Bun().NewRaw(`SELECT id FROM worktrees WHERE project_id = ?0 AND is_main AND deleted_at IS NULL ORDER BY created_at, id LIMIT 1`,
		m.target).Scan(ctx, &targetMain); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read the target's main worktree: %w", err)
	}
	keepSourceMain := len(targetMain) == 1
	if keepSourceMain {
		for _, q := range projectMergeMainRepoint {
			if err := m.exec(ctx, q, m.source, targetMain[0]); err != nil {
				return fmt.Errorf("re-point the source's main worktree: %w", recoveryAdmissionError(err))
			}
		}
	}

	// Live names are unique per project. A moving worktree whose name the
	// target uses takes the first free "<name>-N".
	var live []struct {
		ID        string `bun:"id"`
		Name      string `bun:"name"`
		ProjectID string `bun:"project_id"`
	}
	if err := m.db.Bun().NewRaw(`SELECT id, name, project_id FROM worktrees
		WHERE project_id IN (?0, ?1) AND deleted_at IS NULL AND NOT (project_id = ?0 AND is_main AND ?2)
		ORDER BY id`, m.source, m.target, keepSourceMain).Scan(ctx, &live); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read the projects' live worktrees: %w", err)
	}
	taken := map[string]bool{}
	for _, w := range live {
		taken[w.Name] = true
	}
	targetNames := map[string]bool{}
	for _, w := range live {
		if w.ProjectID == m.target {
			targetNames[w.Name] = true
		}
	}
	for _, w := range live {
		if w.ProjectID != m.source || !targetNames[w.Name] {
			continue
		}
		name := freeName(w.Name, "-%d", taken)
		taken[name] = true
		if err := m.exec(ctx, `UPDATE worktrees SET name = ?0 WHERE id = ?1`, name, w.ID); err != nil {
			return fmt.Errorf("rename worktree %s: %w", w.ID, err)
		}
	}
	// A create's idempotency key is unique per project; one the target
	// already holds is a finished create's, and is cleared on the source's.
	if err := m.exec(ctx, `UPDATE worktrees s SET idempotency_key = NULL
		WHERE s.project_id = ?0 AND s.idempotency_key IS NOT NULL
		  AND EXISTS (SELECT 1 FROM worktrees t WHERE t.project_id = ?1 AND t.idempotency_key = s.idempotency_key)`); err != nil {
		return fmt.Errorf("clear duplicate idempotency keys: %w", err)
	}
	if err := m.exec(ctx, `UPDATE worktrees SET project_id = ?1 WHERE project_id = ?0 AND NOT (is_main AND ?2)`,
		m.source, m.target, keepSourceMain); err != nil {
		return recoveryAdmissionError(err)
	}
	return nil
}

// mergeProjectTriggers carries out the triggers policy (projectMergeColumns).
// Locked before they are read, in id order, as every trigger writer locks
// them, so a pause that committed meanwhile is moved, not overwritten.
func mergeProjectTriggers(ctx context.Context, m *projectMergeTx) error {
	var rows []*Trigger
	if err := m.db.Bun().NewSelect().Model(&rows).
		Where("project_id = ?", m.source).
		Order("id").
		For("NO KEY UPDATE").
		Scan(ctx); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("lock the project's triggers: %w", err)
	}
	before, err := triggersToCore(rows)
	if err != nil {
		return err
	}
	var names []struct {
		UserID string `bun:"user_id"`
		Name   string `bun:"name"`
	}
	if err := m.db.Bun().NewRaw(`SELECT user_id, name FROM triggers WHERE project_id IN (?0, ?1)`, m.source, m.target).
		Scan(ctx, &names); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read the projects' trigger names: %w", err)
	}
	taken := map[string]map[string]bool{}
	for _, n := range names {
		if taken[n.UserID] == nil {
			taken[n.UserID] = map[string]bool{}
		}
		taken[n.UserID][n.Name] = true
	}
	var targetNames []struct {
		UserID string `bun:"user_id"`
		Name   string `bun:"name"`
	}
	if err := m.db.Bun().NewRaw(`SELECT user_id, name FROM triggers WHERE project_id = ?0`, m.target).
		Scan(ctx, &targetNames); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read the target's trigger names: %w", err)
	}
	inTarget := map[[2]string]bool{}
	for _, n := range targetNames {
		inTarget[[2]string{n.UserID, n.Name}] = true
	}
	for _, t := range before {
		after := *t
		after.ProjectID = m.target
		if inTarget[[2]string{t.UserID, t.Name}] {
			after.Name = freeName(t.Name, " (%d)", taken[t.UserID])
			taken[t.UserID][after.Name] = true
		}
		if err := m.exec(ctx, `UPDATE triggers SET project_id = ?0, name = ?1, updated_at = NOW() WHERE id = ?2`,
			m.target, after.Name, t.ID); err != nil {
			return fmt.Errorf("move trigger %s: %w", t.ID, projectDeletedError(err))
		}
		if err := RecordTriggerChange(ctx, m.db, core.TriggerChangeProjectMerged, t, &after); err != nil {
			return err
		}
		m.movedTriggerIDs = append(m.movedTriggerIDs, t.ID)
	}
	return nil
}

// freeName is name with the first suffix (format applied to 2, 3, ...) that
// is not taken.
func freeName(name, format string, taken map[string]bool) string {
	for n := 2; ; n++ {
		candidate := name + fmt.Sprintf(format, n)
		if !taken[candidate] {
			return candidate
		}
	}
}
