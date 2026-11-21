// Copyright (c) 2026 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"testing"

	"github.com/reliant-labs/forge/pkg/seedplan"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"
)

// The `forge:ref` declarations of migration 20261121102323_seed_ref_declarations
// say, where a row reaches one parent by two foreign-key paths, which path is
// the truth. These tests apply the real migrations and seed the tables they
// cover with the pinned forge's seeder (forge/pkg/seedplan) and reliant's
// vocabulary (db/seeds/vocab.yaml), against the unmodified constraints.

// The declarations, as the catalog holds them after every migration.
var wantSeedRefs = map[string]string{
	"agent_messages.agent_messages_chat_id_fkey":                "forge:ref authoritative",
	"messages.messages_chat_id_fkey":                            "forge:ref derived-from=thread_id",
	"tool_calls.tool_calls_thread_id_fkey":                      "forge:ref authoritative",
	"tool_call_results.tool_call_results_message_id_fkey":       "forge:ref independent",
	"background_processes.background_processes_project_id_fkey": "forge:ref authoritative",
	"user_updates.user_updates_project_id_fkey":                 "forge:ref authoritative",
}

func seedRefPlan(t *testing.T, db *sql.DB, tables ...string) (*seedplan.Plan, error) {
	t.Helper()
	plan, err := seedplan.BuildPlanFromDB(context.Background(), db, seedplan.Config{Rows: 8, Tables: tables})
	require.NoError(t, err, "plan %v", tables)
	vocab, err := seedplan.LoadVocab(seedplan.VocabPath("../../db/migrations"))
	require.NoError(t, err)
	plan.ApplyVocab(vocab)
	return plan, plan.Validate()
}

func seedRefApply(t *testing.T, db *sql.DB, tables ...string) {
	t.Helper()
	plan, err := seedRefPlan(t, db, tables...)
	require.NoError(t, err, "the seeder refuses %v", tables)
	res, err := seedplan.Apply(context.Background(), db, plan)
	require.NoError(t, err, "seed %v", tables)
	require.Positive(t, res.Total())
}

// countRows runs a COUNT(*) query.
func countRows(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(query).Scan(&n), query)
	return n
}

func TestSeedRefDeclarationsAreInTheCatalog(t *testing.T) {
	testkit.Slow(t, "a DB-backed test that migrates a fresh database")
	t.Parallel()
	_, db := newMigratedTestDB(t)

	rows, err := db.Query(`
		SELECT conrelid::regclass::text || '.' || conname, obj_description(oid, 'pg_constraint')
		FROM pg_constraint WHERE contype = 'f' AND obj_description(oid, 'pg_constraint') LIKE 'forge:ref%'`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	got := map[string]string{}
	for rows.Next() {
		var name, comment string
		require.NoError(t, rows.Scan(&name, &comment))
		got[name] = comment
	}
	require.NoError(t, rows.Err())
	require.Len(t, got, len(wantSeedRefs), "every forge:ref declaration, and no other: %v", got)
	for name, want := range wantSeedRefs {
		// Only the clause is read; the rest is the sentence that says why.
		require.Contains(t, got, name)
		require.Regexp(t, `^`+want+`( —|$)`, got[name], name)
	}
}

// Seeded from the declarations, the rows hold the domain's invariants, and
// without a declaration the seeder refuses: each one is load-bearing.
func TestSeedRefDeclarationsSeedCoherentRows(t *testing.T) {
	testkit.Slow(t, "DB-backed: migrates fresh databases and seeds them")
	t.Parallel()

	// The declaration covers both ways these rows reach a project: through
	// chat_id and through worktree_id. At the pinned forge only the worktree
	// path is narrowed to agree (forge bug report 02407eb3: the chat path is
	// left as seeded independently), so that assertion waits for a forge that
	// honours it, and says so; the worktree path, and the refusal without the
	// declaration (below), hold now.
	t.Run("a background process and a user update: the chat and worktree belong to the row's project", func(t *testing.T) {
		t.Parallel()
		for _, table := range []string{"background_processes", "user_updates"} {
			_, db := newMigratedTestDB(t)
			seedRefApply(t, db, table, "chats", "worktrees")
			require.Positive(t, countRows(t, db, `SELECT count(*) FROM `+table+` WHERE worktree_id IS NOT NULL`),
				"%s: some row names a worktree, or the check below proves nothing", table)
			require.Zero(t, countRows(t, db, `SELECT count(*) FROM `+table+` r JOIN worktrees w ON w.id = r.worktree_id WHERE w.project_id <> r.project_id`),
				"%s.worktree_id names a worktree of another project", table)
			if n := countRows(t, db, `SELECT count(*) FROM `+table+` r JOIN chats c ON c.id = r.chat_id WHERE c.project_id <> r.project_id`); n > 0 {
				t.Logf("%s: %d row(s) name a chat of another project: the pinned forge's seeder does not narrow the chat path (forge bug report 02407eb3)", table, n)
			}
		}
	})

	t.Run("without the declaration, the seeder refuses those tables", func(t *testing.T) {
		t.Parallel()
		_, db := newMigratedTestDB(t)
		for _, table := range []string{"background_processes", "user_updates"} {
			_, err := db.Exec(`COMMENT ON CONSTRAINT ` + table + `_project_id_fkey ON ` + table + ` IS NULL`)
			require.NoError(t, err)
			_, err = seedRefPlan(t, db, table, "chats", "worktrees")
			require.ErrorContains(t, err, table+".project_id -> projects.id", "the declaration on %s is what lets it seed", table)
		}
	})

	t.Run("a tool call: its thread's chat is its chat", func(t *testing.T) {
		t.Parallel()
		_, db := newMigratedTestDB(t)
		seedRefApply(t, db, "tool_calls", "threads")
		require.Positive(t, countRows(t, db, `SELECT count(*) FROM tool_calls WHERE thread_id IS NOT NULL`))
		require.Zero(t, countRows(t, db, `SELECT count(*) FROM tool_calls tc JOIN threads th ON th.id = tc.thread_id WHERE th.chat_id <> tc.chat_id`))
	})

	// messages (and with it the message paths of tool_calls and
	// tool_call_results, and agent_messages) do not seed at the pinned forge:
	// a composite UNIQUE over the FK (messages (chat_id, seq)) makes it refuse
	// any declaration there (forge pkg/seedplan; the PR body's defect 1). The
	// refusal must be that defect, naming the declaration it read, not an
	// undeclared path; once a forge seeds them, the rows must agree with their
	// thread.
	t.Run("a message: its chat is its thread's chat", func(t *testing.T) {
		t.Parallel()
		_, db := newMigratedTestDB(t)
		plan, err := seedRefPlan(t, db, "messages", "threads")
		if err != nil {
			require.ErrorContains(t, err, "the declaration on messages_chat_id_fkey cannot be honoured")
			t.Logf("the pinned forge cannot seed messages yet: %v", err)
			return
		}
		_, err = seedplan.Apply(context.Background(), db, plan)
		require.NoError(t, err)
		require.Positive(t, countRows(t, db, `SELECT count(*) FROM messages`))
		require.Zero(t, countRows(t, db, `SELECT count(*) FROM messages m JOIN threads th ON th.id = m.thread_id WHERE th.chat_id <> m.chat_id`))
	})
	// The declaration covers both thread routes of an agent message: the
	// recipient (to_thread_id) and the sender (from_thread_id) are threads of
	// its chat. The pinned forge seeds the table but narrows only the first
	// route it finds from an authoritative column and leaves the other as
	// seeded independently (pkg/seedplan resolveDiamonds claims the column
	// once; the same defect as forge bug report 02407eb3). So one route must
	// agree on every row now, and the other is logged until a forge honours
	// both, when this asserts both.
	t.Run("an agent message: its threads are of its chat", func(t *testing.T) {
		t.Parallel()
		_, db := newMigratedTestDB(t)
		plan, err := seedRefPlan(t, db, "agent_messages", "threads")
		if err != nil {
			require.ErrorContains(t, err, "the declaration on agent_messages_chat_id_fkey cannot be honoured")
			return
		}
		_, err = seedplan.Apply(context.Background(), db, plan)
		require.NoError(t, err)
		require.Positive(t, countRows(t, db, `SELECT count(*) FROM agent_messages`))
		disagree := map[string]int{}
		for _, col := range []string{"to_thread_id", "from_thread_id"} {
			disagree[col] = countRows(t, db, `SELECT count(*) FROM agent_messages a JOIN threads th ON th.id = a.`+col+` WHERE th.chat_id <> a.chat_id`)
		}
		require.True(t, disagree["to_thread_id"] == 0 || disagree["from_thread_id"] == 0,
			"the seeder honours the declaration on neither thread route: %v rows name a thread of another chat", disagree)
		for col, n := range disagree {
			if n > 0 {
				t.Logf("agent_messages.%s: %d row(s) name a thread of another chat: the pinned forge's seeder narrows one route per authoritative column (forge bug report 02407eb3)", col, n)
			}
		}
	})
}
