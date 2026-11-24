// Copyright (c) 2026 Reliant Labs
package store_test

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// projectChatTable is a table keyed by chat that project delete once left
// behind (reliant#3503): how to write rows of it for a chat, and the query
// that counts them. The count reads the rows by their own key, never through
// chats: the chat is gone after the delete whether or not the rows are, and a
// chat_id that is ON DELETE SET NULL no longer names it.
type projectChatTable struct {
	table string
	// seed writes the chat's rows, each id prefixed by chat. other is a
	// project that is not deleted.
	seed  func(t *testing.T, env *storetest.Env, chat, other string)
	count string // $1 is the chat
}

var projectChatTables = []projectChatTable{
	{
		table: "chat_thread_message_counts",
		seed: func(t *testing.T, env *storetest.Env, chat, _ string) {
			execOrFatal(t, env, `INSERT INTO chat_thread_message_counts (chat_id, thread_id, message_count) VALUES ($1, 'th', 3)`, chat)
		},
		count: `SELECT count(*) FROM chat_thread_message_counts WHERE chat_id = $1`,
	},
	{
		table: "chat_message_counts_pending",
		seed: func(t *testing.T, env *storetest.Env, chat, _ string) {
			execOrFatal(t, env, `INSERT INTO chat_message_counts_pending (chat_id) VALUES ($1)`, chat)
		},
		count: `SELECT count(*) FROM chat_message_counts_pending WHERE chat_id = $1`,
	},
	{
		table: "provider_backoff",
		seed: func(t *testing.T, env *storetest.Env, chat, _ string) {
			execOrFatal(t, env, `INSERT INTO provider_backoff (thread_id, chat_id, updated_at) VALUES ($1 || '-th', $1, $2)`, chat, projectDeleteWriterNow)
		},
		count: `SELECT count(*) FROM provider_backoff WHERE chat_id = $1`,
	},
	{
		table: "integration_writes",
		seed: func(t *testing.T, env *storetest.Env, chat, _ string) {
			execOrFatal(t, env, `INSERT INTO integration_writes (id, user_id, chat_id, integration, action, scope, target, status, created_at)
				VALUES ($1 || '-iw', 'owner', $1, 'github', 'comment', 'acme/app', 'issue 1', 'done', $2)`, chat, projectDeleteWriterNow)
		},
		count: `SELECT count(*) FROM integration_writes WHERE chat_id = $1`,
	},
	{
		// The trigger lives in a project that is not deleted, so the
		// deleted project's cascade does not reach the edit its chat made.
		table: "trigger_changes",
		seed: func(t *testing.T, env *storetest.Env, chat, other string) {
			execOrFatal(t, env, `INSERT INTO triggers (id, user_id, project_id, name, kind, workflow) VALUES ($1 || '-trg', 'owner', $2, $1, 'schedule', 'agent')`, chat, other)
			execOrFatal(t, env, `INSERT INTO trigger_changes (id, trigger_id, user_id, chat_id, operation, after_state, created_at)
				VALUES ($1 || '-chg', $1 || '-trg', 'owner', $1, 'updated', '{}', $2)`, chat, projectDeleteWriterNow)
		},
		count: `SELECT count(*) FROM trigger_changes WHERE chat_id = $1`,
	},
	{
		table: "video_generation_jobs",
		seed: func(t *testing.T, env *storetest.Env, chat, _ string) {
			execOrFatal(t, env, `INSERT INTO video_generation_jobs (tool_call_id, user_id, chat_id, driver, model_id, api_model, provider_job, state, created_at, updated_at)
				VALUES ($1 || '-tc', 'owner', $1, 'gemini', 'veo', 'veo-3', 'job-1', 'done', $2, $2)`, chat, projectDeleteWriterNow)
		},
		count: `SELECT count(*) FROM video_generation_jobs WHERE chat_id = $1`,
	},
	{
		table: "trigger_events",
		seed: func(t *testing.T, env *storetest.Env, chat, _ string) {
			execOrFatal(t, env, `INSERT INTO trigger_events (id, user_id, kind, dedupe_key, occurred_at, outcome, chat_id)
				VALUES ($1 || '-ev', 'owner', 'webhook', $1, $2, 'launched', $1)`, chat, projectDeleteWriterNow)
		},
		count: `SELECT count(*) FROM trigger_events WHERE id = $1 || '-ev'`,
	},
	{
		// Three lines of output: more than the thinning slice of two the
		// test deletes with, so the output crosses a slice boundary.
		table: "background_processes",
		seed: func(t *testing.T, env *storetest.Env, chat, _ string) {
			execOrFatal(t, env, `INSERT INTO background_processes (id, command, working_dir, chat_id, user_id, status, created_at, updated_at)
				VALUES ($1 || '-bp', 'npm run dev', '/srv', $1, 'owner', 2, $2, $2)`, chat, projectDeleteWriterNow)
			for seq := 1; seq <= 3; seq++ {
				execOrFatal(t, env, `INSERT INTO background_process_output (process_id, seq, stream, line, created_at)
					VALUES ($1 || '-bp', $2, 'stdout', 'private', $3)`, chat, seq, projectDeleteWriterNow)
			}
		},
		count: `SELECT (SELECT count(*) FROM background_processes WHERE id = $1 || '-bp')
			+ (SELECT count(*) FROM background_process_output WHERE process_id = $1 || '-bp')`,
	},
}

func execOrFatal(t *testing.T, env *storetest.Env, q string, args ...any) {
	t.Helper()
	if _, err := env.SQL.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// TestDeleteUserProjectRemovesEveryChatKeyedTable proves, one table at a
// time, that deleting a project removes the rows its chats left in each table
// keyed by chat that project delete once left behind, and leaves the rows of
// another project's chat alone.
func TestDeleteUserProjectRemovesEveryChatKeyedTable(t *testing.T) {
	for _, tc := range projectChatTables {
		t.Run(tc.table, func(t *testing.T) {
			env := storetest.New(t)
			ctx := context.Background()
			const project, chat = "proj-deleted", "chat-deleted"
			const keptProject, keptChat = "proj-kept", "chat-kept"
			seedProjectWithChat(t, env, project, chat)
			seedProjectWithChat(t, env, keptProject, keptChat)
			tc.seed(t, env, chat, keptProject)
			tc.seed(t, env, keptChat, keptProject)

			count := func(chat string) int {
				t.Helper()
				var n int
				if err := env.SQL.QueryRowContext(ctx, tc.count, chat).Scan(&n); err != nil {
					t.Fatalf("count %s: %v", tc.table, err)
				}
				return n
			}
			seeded := count(chat)
			if seeded == 0 {
				t.Fatalf("%s: the seed wrote no rows", tc.table)
			}

			if err := store.DeleteUserProjectInSlices(ctx, env.ORM, project, "owner", 2, 2); err != nil {
				t.Fatalf("DeleteUserProject: %v", err)
			}

			if n := count(chat); n != 0 {
				t.Errorf("%s: %d rows of the deleted project's chat survived, want 0 — add it to project_delete.go", tc.table, n)
			}
			if n := count(keptChat); n != seeded {
				t.Errorf("%s: the other project's chat has %d rows, want %d", tc.table, n, seeded)
			}
		})
	}
}

// Project-only history must go before SET NULL loses its ownership links,
// including rows that arrive after the bounded cleanup loops have finished.
func TestDeleteUserProjectRemovesProjectOnlyHistory(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedProjectWithChat(t, env, "deleted", "deleted-chat")
	seedProjectWithChat(t, env, "kept", "kept-chat")
	seed := func(project, suffix string) {
		t.Helper()
		key := project + suffix
		execOrFatal(t, env, `INSERT INTO triggers (id, user_id, project_id, name, kind, workflow)
			VALUES ($1, 'owner', $2, $1, 'schedule', 'agent')`, key, project)
		for seq := 1; seq <= 3; seq++ {
			execOrFatal(t, env, `INSERT INTO trigger_events (id, trigger_id, user_id, kind, dedupe_key, occurred_at, outcome, payload)
				VALUES ($1 || '-' || $2::int::text, $1, 'owner', 'schedule', $1 || '-' || $2::int::text, $3, 'pending', '{"private":true}')`, key, seq, projectDeleteWriterNow)
		}
		execOrFatal(t, env, `INSERT INTO background_processes (id, command, working_dir, project_id, user_id, status, created_at, updated_at)
			VALUES ($1, 'npm run dev', '/srv', $2, 'owner', 2, $3, $3)`, key, project, projectDeleteWriterNow)
		for seq := 1; seq <= 3; seq++ {
			execOrFatal(t, env, `INSERT INTO background_process_output (process_id, seq, stream, line, created_at)
				VALUES ($1, $2, 'stdout', 'private', $3)`, key, seq, projectDeleteWriterNow)
		}
	}
	seed("deleted", "-initial")
	seed("kept", "-initial")
	late := false
	defer store.SetProjectDeleteBeforeFinal(func(_ context.Context, _ string) {
		if !late {
			late = true
			seed("deleted", "-late")
		}
	})()
	if err := store.DeleteUserProjectInSlices(ctx, env.ORM, "deleted", "owner", 2, 2); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		table, key string
		want       int
	}{
		{"trigger_events", "id", 3},
		{"background_processes", "id", 1},
		{"background_process_output", "process_id", 3},
	} {
		var removed, kept int
		// Count by durable IDs, not the ownership FKs that deletion nulls.
		q := `SELECT count(*) FILTER (WHERE ` + tc.key + ` LIKE 'deleted-%'), count(*) FILTER (WHERE ` + tc.key + ` LIKE 'kept-%') FROM ` + tc.table
		if err := env.SQL.QueryRowContext(ctx, q).Scan(&removed, &kept); err != nil {
			t.Fatal(err)
		}
		if removed != 0 || kept != tc.want {
			t.Errorf("%s: deleted=%d kept=%d, want 0 and %d", tc.table, removed, kept, tc.want)
		}
	}
}
