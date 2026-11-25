package store_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// TestProjectMergeNamesEveryProjectColumn holds the merge's table list
// complete: every column of the migrated schema that holds a project id (one
// named *project_id, or a foreign key to projects) is named in
// projectMergeColumns with a policy, and every column it names exists. The
// same for the columns holding a repo id or a worktree id, whose rows the
// merge re-points when it folds a repo or keeps the source's main worktree. A
// migration that adds such a column fails here until its merge policy is
// decided.
func TestProjectMergeNamesEveryProjectColumn(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	columns := func(t *testing.T, likeSuffix, referenced string) []string {
		t.Helper()
		rows, err := env.SQL.QueryContext(ctx, `
			SELECT c.table_name || '.' || c.column_name
			FROM information_schema.columns c
			JOIN information_schema.tables tb ON tb.table_schema = c.table_schema AND tb.table_name = c.table_name
			WHERE c.table_schema = 'public' AND tb.table_type = 'BASE TABLE' AND c.column_name LIKE $1
			  AND c.table_name <> $2
			UNION
			SELECT con.conrelid::regclass::text || '.' || a.attname
			FROM pg_constraint con
			JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = ANY (con.conkey)
			WHERE con.contype = 'f' AND con.confrelid = $2::regclass
			ORDER BY 1`, "%"+likeSuffix, referenced)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	check := func(t *testing.T, what string, inSchema, declared []string) {
		t.Helper()
		want := map[string]bool{}
		for _, c := range declared {
			want[c] = true
		}
		have := map[string]bool{}
		for _, c := range inSchema {
			have[c] = true
			if !want[c] {
				t.Errorf("%s holds a %s the project merge does not name: give it a policy in projectMergeColumns (internal/store/project_merge_repo_ext.go)", c, what)
			}
		}
		for _, c := range declared {
			if !have[c] {
				t.Errorf("the project merge names %s, which the schema no longer has as a %s: remove it", c, what)
			}
		}
	}

	t.Run("project ids", func(t *testing.T) {
		policies := store.ProjectMergeColumnPolicies()
		declared := make([]string, 0, len(policies))
		for c := range policies {
			declared = append(declared, c)
		}
		sort.Strings(declared)
		check(t, "project id", columns(t, "project_id", "projects"), declared)
	})
	t.Run("repo ids", func(t *testing.T) {
		check(t, "repo id", columns(t, "repo_id", "repos"), store.ProjectMergeRepoRefColumns())
	})
	t.Run("worktree ids", func(t *testing.T) {
		check(t, "worktree id", columns(t, "worktree_id", "worktrees"), store.ProjectMergeWorktreeRefColumns())
	})
}

const (
	mergeOwner  = "merge-owner"
	mergeSource = "p-src"
	mergeTarget = "p-tgt"
)

var mergeNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// mergeFixture seeds two projects of mergeOwner, each with a main worktree
// and a checkout: the source on d-src, the target on d-tgt.
type mergeFixture struct {
	t   *testing.T
	env *storetest.Env
}

func newMergeFixture(t *testing.T, sourceOrg, targetOrg any) *mergeFixture {
	t.Helper()
	f := &mergeFixture{t: t, env: storetest.New(t)}
	f.exec(`INSERT INTO projects (id, user_id, name, path, org_id, created_at, updated_at, last_active) VALUES ($1, $2, 'shop', '/a/shop', $3, $4, $4, $4)`,
		mergeSource, mergeOwner, sourceOrg, mergeNow)
	f.exec(`INSERT INTO projects (id, user_id, name, path, org_id, created_at, updated_at, last_active) VALUES ($1, $2, 'shop-2', '/b/shop', $3, $4, $4, $4)`,
		mergeTarget, mergeOwner, targetOrg, mergeNow.Add(-time.Hour))
	for _, d := range []string{"d-src", "d-tgt", "d-other"} {
		f.exec(`INSERT INTO daemons (id, user_id, name) VALUES ($1, $2, $1)`, d, mergeOwner)
	}
	f.checkout(mergeSource, "d-src", "/a/shop")
	f.checkout(mergeTarget, "d-tgt", "/b/shop")
	f.worktree("wt-src-main", mergeSource, "main", true)
	f.worktree("wt-tgt-main", mergeTarget, "main", true)
	return f
}

func (f *mergeFixture) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.env.SQL.ExecContext(context.Background(), q, args...); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
}

func (f *mergeFixture) count(q string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.env.SQL.QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func (f *mergeFixture) str(q string, args ...any) string {
	f.t.Helper()
	var s *string
	if err := f.env.SQL.QueryRowContext(context.Background(), q, args...).Scan(&s); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
	if s == nil {
		return "<nil>"
	}
	return *s
}

func (f *mergeFixture) checkout(project, daemon, path string) {
	f.exec(`INSERT INTO project_daemons (project_id, daemon_id, path, install_state) VALUES ($1, $2, $3, 'installed')`, project, daemon, path)
}

func (f *mergeFixture) worktree(id, project, name string, main bool) {
	f.exec(`INSERT INTO worktrees (id, name, path, branch, base_branch, project_id, is_main, created_at, updated_at, last_active)
		VALUES ($1, $2, '/wt/' || $1, $2, 'main', $3, $4, $5, $5, $5)`, id, name, project, main, mergeNow)
}

func (f *mergeFixture) chat(id, project string, worktree any) {
	f.exec(`INSERT INTO chats (id, title, project_id, user_id, worktree_id, created_at, updated_at, last_active)
		VALUES ($1, $1, $2, $3, $4, $5, $5, $5)`, id, project, mergeOwner, worktree, mergeNow)
}

func (f *mergeFixture) merge() (*core.ProjectMergeResult, error) {
	return store.MergeProjects(context.Background(), f.env.ORM, core.ProjectMerge{
		SourceID: mergeSource, TargetID: mergeTarget, ActorUserID: mergeOwner,
		Decided:     core.ProjectDecision{OwnerUserID: mergeOwner, OrgID: f.sourceOrg()},
		DeleteAfter: mergeNow.Add(core.ProjectDeleteWindow),
	})
}

func (f *mergeFixture) sourceOrg() *string {
	org := f.str(`SELECT org_id FROM projects WHERE id = $1`, mergeSource)
	if org == "<nil>" {
		return nil
	}
	return &org
}

// TestMergeProjectsMovesEverythingOntoTheTarget merges a source with chats
// (one on a branch worktree, one on its main worktree), a checkout on another
// machine, a trigger and a branch worktree whose names the target also uses,
// a repo the target has too (with a forge app) and one it has not, a
// per-machine catalog and a config and a setting both have, and chat
// attachments, and checks every row is the target's after, none is left on
// the source but what stays on purpose, the counts, and that the source is
// scheduled for deletion.
func TestMergeProjectsMovesEverythingOntoTheTarget(t *testing.T) {
	f := newMergeFixture(t, nil, nil)
	ctx := context.Background()
	f.exec(`INSERT INTO projects (id, user_id, name, path, created_at, updated_at, last_active) VALUES ('p-other', $1, 'docs', '/a/docs', $2, $2, $2)`, mergeOwner, mergeNow)

	f.worktree("wt-src-feat", mergeSource, "feat", false)
	f.worktree("wt-tgt-feat", mergeTarget, "feat", false)
	f.chat("c-src-plain", mergeSource, nil)
	f.chat("c-src-branch", mergeSource, "wt-src-feat")
	f.chat("c-src-main", mergeSource, "wt-src-main")
	f.chat("c-tgt", mergeTarget, nil)
	f.chat("c-other", "p-other", nil)
	// The source chat attaches the target (it becomes its primary); the other
	// project's chat attaches the source (it becomes an attachment of the target).
	f.exec(`INSERT INTO chat_projects (chat_id, project_id, created_at) VALUES ('c-src-plain', $1, $2), ('c-other', $3, $2)`, mergeTarget, mergeNow, mergeSource)

	f.exec(`INSERT INTO triggers (id, user_id, project_id, name, kind, workflow) VALUES ('tr-src', $1, $2, 'nightly', 'schedule', 'agent'), ('tr-tgt', $1, $3, 'nightly', 'schedule', 'agent')`,
		mergeOwner, mergeSource, mergeTarget)

	f.exec(`INSERT INTO repos (id, project_id, name, relative_path, created_at, updated_at) VALUES
		('r-src-root', $1, 'shop', '.', $3, $3), ('r-src-lib', $1, 'lib', 'lib', $3, $3), ('r-tgt-root', $2, 'shop', '.', $3, $3)`,
		mergeSource, mergeTarget, mergeNow)
	f.exec(`INSERT INTO repo_forge_apps (repo_id, subpath, forge_name, created_at, updated_at) VALUES ('r-src-root', 'app', 'shop', $1, $1)`, mergeNow)

	f.exec(`INSERT INTO project_machine_catalogs (project_id, daemon_id, workflows_json, pushed_at, updated_at) VALUES
		($1, 'd-other', 'source', $3, $3), ($2, 'd-other', 'target', $3, $3), ($1, 'd-src', 'source-own', $3, $3)`, mergeSource, mergeTarget, mergeNow)
	f.exec(`INSERT INTO project_configs (id, project_id, daemon_id, pushed_at, created_at, updated_at) VALUES
		('pc-src', $1, 'seed', $3, $3, $3), ('pc-tgt', $2, 'seed', $3, $3, $3)`, mergeSource, mergeTarget, mergeNow)
	f.exec(`INSERT INTO settings (id, user_id, project_id, key, value, value_type, created_at, updated_at) VALUES
		('s-src', $1, $2, 'model', 'source', 'string', $4, $4), ('s-tgt', $1, $3, 'model', 'target', 'string', $4, $4)`,
		mergeOwner, mergeSource, mergeTarget, mergeNow)
	// The target was detached from the machine the source's checkout is on;
	// the source from d-other (the target has no say there) and from d-tgt
	// (where the target's checkout wins).
	f.exec(`INSERT INTO project_daemon_detachments (project_id, daemon_id, path) VALUES
		($2, 'd-src', '/old/shop'), ($1, 'd-other', '/x/shop'), ($1, 'd-tgt', '/y/shop')`, mergeSource, mergeTarget)

	preview, err := store.PreviewProjectMerge(ctx, f.env.ORM, mergeSource, mergeTarget)
	if err != nil {
		t.Fatalf("PreviewProjectMerge: %v", err)
	}
	if len(preview.Blockers) != 0 {
		t.Fatalf("preview blockers = %+v, want none", preview.Blockers)
	}

	result, err := f.merge()
	if err != nil {
		t.Fatalf("MergeProjects: %v", err)
	}

	// The counts: 3 chats, 2 worktrees (main and feat), 1 trigger, 2 repos,
	// the checkout on d-src, and 7 other rows (the attachment of c-other, the
	// config, two catalogs, the setting, two detachments). The preview said
	// the same.
	want := core.ProjectMergeCounts{
		Chats: 3, Worktrees: 2, Triggers: 1, Repos: 2, OtherRows: 7,
		Checkouts: []core.ProjectMergeCheckout{{DaemonID: "d-src", InstallState: core.ProjectInstallInstalled}},
	}
	for name, got := range map[string]core.ProjectMergeCounts{"merge": result.Counts, "preview": preview.Counts} {
		if got.Chats != want.Chats || got.Worktrees != want.Worktrees || got.Triggers != want.Triggers ||
			got.Repos != want.Repos || got.OtherRows != want.OtherRows ||
			len(got.Checkouts) != 1 || got.Checkouts[0] != want.Checkouts[0] {
			t.Errorf("%s counts = %+v, want %+v", name, got, want)
		}
	}
	if len(result.MovedTriggerIDs) != 1 || result.MovedTriggerIDs[0] != "tr-src" {
		t.Errorf("moved triggers = %v", result.MovedTriggerIDs)
	}

	// Chats, worktrees and checkouts.
	if n := f.count(`SELECT count(*) FROM chats WHERE project_id = $1 AND id IN ('c-src-plain', 'c-src-branch', 'c-src-main')`, mergeTarget); n != 3 {
		t.Errorf("%d of the source's chats are the target's, want 3", n)
	}
	if got := f.str(`SELECT worktree_id FROM chats WHERE id = 'c-src-main'`); got != "wt-tgt-main" {
		t.Errorf("the chat on the source's main worktree is on %s, want the target's main worktree", got)
	}
	if got := f.str(`SELECT worktree_id FROM chats WHERE id = 'c-src-branch'`); got != "wt-src-feat" {
		t.Errorf("the branch chat's worktree = %s, want its own wt-src-feat", got)
	}
	if got := f.str(`SELECT project_id || ' ' || name FROM worktrees WHERE id = 'wt-src-feat'`); got != mergeTarget+" feat-2" {
		t.Errorf("the source's feat worktree = %q, want it the target's, renamed feat-2", got)
	}
	if got := f.str(`SELECT project_id FROM worktrees WHERE id = 'wt-src-main'`); got != mergeSource {
		t.Errorf("the source's main worktree is in %s; it stays with the source when the target has one", got)
	}
	if n := f.count(`SELECT count(*) FROM project_daemons WHERE project_id = $1 AND daemon_id IN ('d-src', 'd-tgt')`, mergeTarget); n != 2 {
		t.Errorf("the target has %d checkouts of d-src and d-tgt, want both", n)
	}
	var detached []string
	rows, err := f.env.SQL.QueryContext(ctx, `SELECT project_id || ' ' || daemon_id FROM project_daemon_detachments ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		detached = append(detached, s)
	}
	rows.Close()
	if strings.Join(detached, ",") != mergeTarget+" d-other" {
		t.Errorf("detachments = %v, want only the source's from d-other, now the target's", detached)
	}

	// Triggers: renamed off the target's name, with its history.
	if got := f.str(`SELECT project_id || ' ' || name FROM triggers WHERE id = 'tr-src'`); got != mergeTarget+" nightly (2)" {
		t.Errorf("the source's trigger = %q, want the target's, renamed", got)
	}
	if n := f.count(`SELECT count(*) FROM trigger_changes WHERE trigger_id = 'tr-src' AND operation = $1`, core.TriggerChangeProjectMerged); n != 1 {
		t.Errorf("%d project_merged changes of the trigger, want 1", n)
	}

	// Repos: the duplicate folds into the target's root, with its forge app.
	if n := f.count(`SELECT count(*) FROM repos WHERE id = 'r-src-root'`); n != 0 {
		t.Errorf("the source's duplicate root repo survives")
	}
	if got := f.str(`SELECT project_id FROM repos WHERE id = 'r-src-lib'`); got != mergeTarget {
		t.Errorf("the source's lib repo is in %s, want the target", got)
	}
	if n := f.count(`SELECT count(*) FROM repo_forge_apps WHERE repo_id = 'r-tgt-root' AND subpath = 'app'`); n != 1 {
		t.Errorf("the duplicate repo's forge app did not move to the target's root repo")
	}

	// Per-project and per-machine records: the target's win.
	if got := f.str(`SELECT workflows_json FROM project_machine_catalogs WHERE project_id = $1 AND daemon_id = 'd-other'`, mergeTarget); got != "target" {
		t.Errorf("the target's catalog on d-other = %q, want the target's own", got)
	}
	if got := f.str(`SELECT workflows_json FROM project_machine_catalogs WHERE project_id = $1 AND daemon_id = 'd-src'`, mergeTarget); got != "source-own" {
		t.Errorf("the source's catalog on d-src = %q, want it moved", got)
	}
	if got := f.str(`SELECT id FROM project_configs WHERE project_id = $1`, mergeTarget); got != "pc-tgt" {
		t.Errorf("the target's config record = %s, want its own", got)
	}
	if got := f.str(`SELECT value FROM settings WHERE project_id = $1 AND key = 'model'`, mergeTarget); got != "target" {
		t.Errorf("the target's setting = %q, want its own", got)
	}

	// Attachments: the source chat's attachment of the target became its
	// primary; the other chat's attachment of the source is the target's.
	if n := f.count(`SELECT count(*) FROM chat_projects WHERE chat_id = 'c-src-plain'`); n != 0 {
		t.Errorf("the merged chat still attaches its own primary project")
	}
	if n := f.count(`SELECT count(*) FROM chat_projects WHERE chat_id = 'c-other' AND project_id = $1`, mergeTarget); n != 1 {
		t.Errorf("the other chat's attachment of the source did not become the target's")
	}

	// Nothing is left on the source but what stays on purpose (and its main
	// worktree, which stays because the target has one).
	for col, policy := range store.ProjectMergeColumnPolicies() {
		if policy == store.ProjectMergeLeftInPlace {
			continue
		}
		table, column, _ := strings.Cut(col, ".")
		n := f.count(`SELECT count(*) FROM `+table+` WHERE `+column+` = $1`, mergeSource)
		if table == "worktrees" {
			n -= f.count(`SELECT count(*) FROM worktrees WHERE project_id = $1 AND is_main`, mergeSource)
		}
		if n != 0 {
			t.Errorf("%s: %d rows still name the source", col, n)
		}
	}

	// The source is scheduled for deletion, by the merger.
	if got := f.str(`SELECT deleted_by FROM projects WHERE id = $1 AND delete_after IS NOT NULL`, mergeSource); got != mergeOwner {
		t.Errorf("the source's deleted_by = %s, want it scheduled by %s", got, mergeOwner)
	}
	if !result.DeleteAfter.Equal(mergeNow.Add(core.ProjectDeleteWindow)) {
		t.Errorf("DeleteAfter = %v", result.DeleteAfter)
	}
	if n := f.count(`SELECT count(*) FROM projects WHERE id = $1 AND delete_after IS NULL AND last_active = $2`, mergeTarget, mergeNow); n != 1 {
		t.Errorf("the target is not live with the source's (later) last_active")
	}

	// A repeat finds the source scheduled: not found.
	if _, err := f.merge(); !errors.Is(err, core.ErrProjectNotFound) {
		t.Errorf("repeat merge err = %v, want ErrProjectNotFound", err)
	}
}

// TestMergeProjectsMovesTheSourcesMainWorktreeIntoATargetWithNone: a target
// with no main worktree takes the source's.
func TestMergeProjectsMovesTheSourcesMainWorktreeIntoATargetWithNone(t *testing.T) {
	f := newMergeFixture(t, nil, nil)
	f.exec(`DELETE FROM worktrees WHERE id = 'wt-tgt-main'`)
	f.chat("c-src-main", mergeSource, "wt-src-main")
	if _, err := f.merge(); err != nil {
		t.Fatalf("MergeProjects: %v", err)
	}
	if got := f.str(`SELECT project_id FROM worktrees WHERE id = 'wt-src-main' AND is_main`); got != mergeTarget {
		t.Errorf("the source's main worktree is in %s, want the target's main", got)
	}
	if got := f.str(`SELECT worktree_id FROM chats WHERE id = 'c-src-main'`); got != "wt-src-main" {
		t.Errorf("the chat's worktree = %s, want its own", got)
	}
}

// TestMergeProjectsRefuses: each refusal writes nothing.
func TestMergeProjectsRefuses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		orgs      [2]any
		setup     func(f *mergeFixture)
		wantErr   error
		wantBlock core.ProjectMergeBlockerKind
		wantID    string // the blocker's daemon or chat
	}{
		{
			name: "another owner's target",
			setup: func(f *mergeFixture) {
				f.exec(`UPDATE projects SET user_id = 'someone-else' WHERE id = $1`, mergeTarget)
			},
			wantErr: core.ErrProjectNotFound,
		},
		{
			name:    "a target in another org",
			orgs:    [2]any{"acme", "globex"},
			wantErr: core.ErrProjectMergeOtherOrg,
		},
		{
			name: "a target scheduled for deletion",
			setup: func(f *mergeFixture) {
				f.exec(`UPDATE projects SET delete_after = $2 WHERE id = $1`, mergeTarget, mergeNow)
			},
			wantErr: core.ErrProjectNotFound,
		},
		{
			name:      "both checked out on one machine",
			setup:     func(f *mergeFixture) { f.checkout(mergeTarget, "d-src", "/c/shop") },
			wantErr:   core.ErrProjectMergeBlocked,
			wantBlock: core.ProjectMergeSameMachine,
			wantID:    "d-src",
		},
		{
			name: "a running chat in the target",
			setup: func(f *mergeFixture) {
				f.chat("c-tgt-run", mergeTarget, nil)
				f.exec(`INSERT INTO workflows (id, chat_id, workflow_name, thread, created_at, state) VALUES ('wf-run', 'c-tgt-run', 'wf', 'main', $1, $2)`,
					mergeNow, int32(reliantv1.WorkflowState_WORKFLOW_STATE_ACTIVE))
			},
			wantErr:   core.ErrProjectMergeBlocked,
			wantBlock: core.ProjectMergeRunningWork,
			wantID:    "c-tgt-run",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newMergeFixture(t, tc.orgs[0], tc.orgs[1])
			f.chat("c-src", mergeSource, nil)
			if tc.setup != nil {
				tc.setup(f)
			}
			if tc.wantBlock != 0 {
				preview, err := store.PreviewProjectMerge(context.Background(), f.env.ORM, mergeSource, mergeTarget)
				if err != nil {
					t.Fatalf("PreviewProjectMerge: %v", err)
				}
				assertMergeBlocker(t, "preview", preview.Blockers, tc.wantBlock, tc.wantID)
			}
			result, err := f.merge()
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantBlock != 0 {
				if result == nil {
					t.Fatal("a blocked merge returned no blockers")
				}
				assertMergeBlocker(t, "merge", result.Blockers, tc.wantBlock, tc.wantID)
			}
			if got := f.str(`SELECT project_id FROM chats WHERE id = 'c-src'`); got != mergeSource {
				t.Errorf("a refused merge moved the source's chat to %s", got)
			}
			if n := f.count(`SELECT count(*) FROM projects WHERE id = $1 AND delete_after IS NULL`, mergeSource); n != 1 {
				t.Errorf("a refused merge scheduled the source's deletion")
			}
		})
	}
}

func assertMergeBlocker(t *testing.T, what string, blockers []core.ProjectMergeBlocker, kind core.ProjectMergeBlockerKind, id string) {
	t.Helper()
	for _, b := range blockers {
		if b.Kind == kind && (b.DaemonID == id || b.ChatID == id) && b.Message != "" {
			return
		}
	}
	t.Errorf("%s blockers = %+v, want a kind %d blocker naming %s", what, blockers, kind, id)
}
