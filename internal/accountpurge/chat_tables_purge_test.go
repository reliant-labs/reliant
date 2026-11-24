// Copyright (c) 2026 Reliant Labs
package accountpurge_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/accountpurge/purgetest"
	"github.com/reliant-labs/reliant/internal/db"
)

// chatTablesNow is the fixtures' timestamp: data, not a clock.
var chatTablesNow = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

// chatKeyedTable is a table keyed by chat that account purge once left behind
// (reliant#3503): how to write one row of it for a user's chat, and how to
// find that row again. It is found by its own column, never through chats,
// because the chat is gone after the purge whether or not the row is.
type chatKeyedTable struct {
	table string
	// seed writes one row for user, whose project is project and chat is
	// chat. other is another user's project, for a row whose own foreign key
	// would otherwise cascade it away with the user's projects.
	seed func(t *testing.T, d *sql.DB, user, project, chat, other string)
	// byChat finds the row by chat_id; otherwise by user_id.
	byChat bool
}

var chatKeyedTables = []chatKeyedTable{
	{
		table: "chat_thread_message_counts", byChat: true,
		seed: func(t *testing.T, d *sql.DB, _, _, chat, _ string) {
			mustExec(t, d, `INSERT INTO chat_thread_message_counts (chat_id, thread_id, message_count) VALUES ($1, 'th', 3)`, chat)
		},
	},
	{
		table: "chat_message_counts_pending", byChat: true,
		seed: func(t *testing.T, d *sql.DB, _, _, chat, _ string) {
			mustExec(t, d, `INSERT INTO chat_message_counts_pending (chat_id) VALUES ($1)`, chat)
		},
	},
	{
		table: "provider_backoff", byChat: true,
		seed: func(t *testing.T, d *sql.DB, _, _, chat, _ string) {
			mustExec(t, d, `INSERT INTO provider_backoff (thread_id, chat_id, updated_at) VALUES ($1 || '-th', $1, $2)`, chat, chatTablesNow)
		},
	},
	{
		table: "user_activations",
		seed: func(t *testing.T, d *sql.DB, user, _, chat, _ string) {
			mustExec(t, d, `INSERT INTO user_activations (user_id, message_id, chat_id, activated_at) VALUES ($1, 'msg', $2, $3)`, user, chat, chatTablesNow)
		},
	},
	{
		table: "trigger_events",
		seed: func(t *testing.T, d *sql.DB, user, _, chat, _ string) {
			mustExec(t, d, `INSERT INTO trigger_events (id, user_id, kind, dedupe_key, occurred_at, outcome, chat_id, payload)
				VALUES ($1 || '-ev', $1, 'webhook', $1, $3, 'launched', $2, '{"body":"private"}')`, user, chat, chatTablesNow)
		},
	},
	{
		// The trigger lives in another user's (an org's) project, so the
		// purge's projects cascade does not reach its history.
		table: "trigger_changes",
		seed: func(t *testing.T, d *sql.DB, user, _, chat, other string) {
			mustExec(t, d, `INSERT INTO triggers (id, user_id, project_id, name, kind, workflow) VALUES ($1 || '-trg', $1, $2, 'nightly', 'schedule', 'agent')`, user, other)
			mustExec(t, d, `INSERT INTO trigger_changes (id, trigger_id, user_id, chat_id, operation, after_state, created_at)
				VALUES ($1 || '-chg', $1 || '-trg', $1, $2, 'created', '{"message":"private"}', $3)`, user, chat, chatTablesNow)
		},
	},
	{
		table: "attention_incidents",
		seed: func(t *testing.T, d *sql.DB, user, project, chat, _ string) {
			mustExec(t, d, `INSERT INTO attention_incidents (user_id, project_id, incident_key, chat_id, detail) VALUES ($1, $2, 'k', $3, 'private')`, user, project, chat)
		},
	},
	{
		table: "attention_actors",
		seed: func(t *testing.T, d *sql.DB, user, project, chat, _ string) {
			mustExec(t, d, `INSERT INTO attention_actors (run_id, chat_id, user_id, project_id, incident_key, started_at, started_at_ns)
				VALUES ($1 || '-run', $2, $1, $3, 'k', $4, $5)`, user, chat, project, chatTablesNow, chatTablesNow.UnixNano())
		},
	},
	{
		table: "integration_writes",
		seed: func(t *testing.T, d *sql.DB, user, _, chat, _ string) {
			mustExec(t, d, `INSERT INTO integration_writes (id, user_id, chat_id, integration, action, scope, target, status, created_at)
				VALUES ($1 || '-iw', $1, $2, 'github', 'comment', 'acme/app', 'issue 1', 'done', $3)`, user, chat, chatTablesNow)
		},
	},
	{
		table: "video_generation_jobs",
		seed: func(t *testing.T, d *sql.DB, user, _, chat, _ string) {
			mustExec(t, d, `INSERT INTO video_generation_jobs (tool_call_id, user_id, chat_id, driver, model_id, api_model, provider_job, state, created_at, updated_at)
				VALUES ($1 || '-tc', $1, $2, 'gemini', 'veo', 'veo-3', 'job-1', 'done', $3, $3)`, user, chat, chatTablesNow)
		},
	},
}

// seedChatOwner writes a user with one project and one chat, and returns
// their ids.
func seedChatOwner(t *testing.T, d *sql.DB, user string) (project, chat string) {
	t.Helper()
	project, chat = "proj-"+user, "chat-"+user
	mustExec(t, d, `INSERT INTO projects (id, name, path, user_id, is_git_repo, created_at, updated_at, last_active)
		VALUES ($1, $1, '/srv/' || $1, $2, true, $3, $3, $3)`, project, user, chatTablesNow)
	mustExec(t, d, `INSERT INTO chats (id, title, project_id, user_id, created_at, updated_at, last_active)
		VALUES ($1, $1, $2, $3, $4, $4, $4)`, chat, project, user, chatTablesNow)
	return project, chat
}

// TestPurge_RemovesEveryChatKeyedTable proves, one table at a time, that
// account deletion removes the rows of each table keyed by chat that it once
// left behind, and leaves another user's rows of it alone.
func TestPurge_RemovesEveryChatKeyedTable(t *testing.T) {
	for _, tc := range chatKeyedTables {
		t.Run(tc.table, func(t *testing.T) {
			repo, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
			defer cleanup()
			purger := purgetest.New(t, repo)

			const victim, bystander = "user-gone", "user-kept"
			victimProject, victimChat := seedChatOwner(t, rawDB, victim)
			bystanderProject, bystanderChat := seedChatOwner(t, rawDB, bystander)
			tc.seed(t, rawDB, victim, victimProject, victimChat, bystanderProject)
			tc.seed(t, rawDB, bystander, bystanderProject, bystanderChat, bystanderProject)

			count := func(user, chat string) int {
				t.Helper()
				column, key := "user_id", user
				if tc.byChat {
					column, key = "chat_id", chat
				}
				var n int
				if err := rawDB.QueryRow(`SELECT count(*) FROM `+tc.table+` WHERE `+column+` = $1`, key).Scan(&n); err != nil {
					t.Fatalf("count %s: %v", tc.table, err)
				}
				return n
			}
			if n := count(victim, victimChat); n != 1 {
				t.Fatalf("%s: seeded %d rows, want 1", tc.table, n)
			}

			if _, err := purger.Purge(context.Background(), victim); err != nil {
				t.Fatalf("Purge: %v", err)
			}

			if n := count(victim, victimChat); n != 0 {
				t.Errorf("%s: %d rows survived account deletion, want 0 — add it to purgeSteps in accountpurge.go", tc.table, n)
			}
			if n := count(bystander, bystanderChat); n != 1 {
				t.Errorf("%s: the other user has %d rows after the purge, want 1", tc.table, n)
			}
		})
	}
}
