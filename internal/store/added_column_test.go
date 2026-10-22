// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// TestPreparedQueriesSurviveAnAddedColumn is the prod failure of 2026-10-22:
// a migration re-created chats_with_activity with one more column, and every
// connection that had ListArchivedChats prepared from before it failed it
// with "cached plan must not change result type", because the query selected
// c.* (columns.go). pgx caches prepared statements per connection, as
// pgbouncer does on its server connections, so this runs each query once on
// ONE connection, adds a column to every relation they read, and runs them
// again on the same connection: each must answer exactly what it answered
// before. The arguments are fixed so the statement text, and with it the
// prepared statement, repeats.
func TestPreparedQueriesSurviveAnAddedColumn(t *testing.T) {
	env := storetest.New(t)
	env.SQL.SetMaxOpenConns(1)
	ctx := context.Background()
	user := storetest.SeedUserID
	at := time.Date(2026, 10, 22, 4, 55, 0, 0, time.UTC)

	mustExec(t, env, `INSERT INTO chats (id, title, project_id, user_id, state, created_at, updated_at, last_active)
		VALUES ('chat-archived', 'archived', $1, $2, 3, $3, $3, $3)`, storetest.SeedProjectID, user, at)

	queries := map[string]func() error{
		"ListArchivedChats": func() error {
			_, err := store.ListArchivedChats(ctx, env.ORM, user)
			return err
		},
		"GetThreadWithParent": func() error {
			_, _, err := store.GetThreadWithParent(ctx, env.ORM, "th-none")
			return err
		},
		"GetContextWindowWithThread": func() error {
			_, _, _, _, err := store.GetContextWindowWithThread(ctx, env.ORM, "cw-none")
			return err
		},
		"ListStrandedSpawnToolCalls": func() error {
			_, err := store.ListStrandedSpawnToolCalls(ctx, env.ORM)
			return err
		},
		"GetTrigger": func() error {
			_, err := store.GetTrigger(ctx, env.ORM, "tr-none")
			return err
		},
		"ListTriggersForUser": func() error {
			_, err := store.ListTriggersForUser(ctx, env.ORM, core.TriggerFilters{UserID: user})
			return err
		},
		"ListIntegrationTriggers": func() error {
			_, err := store.ListIntegrationTriggers(ctx, env.ORM, "github")
			return err
		},
		"ListAccessRoutedTriggers": func() error {
			_, err := store.ListAccessRoutedTriggers(ctx, env.ORM, "github", "acct", "repo", at)
			return err
		},
		"ListTriggerEventsPage": func() error {
			_, _, err := store.ListTriggerEventsPage(ctx, env.ORM, core.TriggerEventFilters{TriggerID: "tr-none", UserID: user, Limit: 10})
			return err
		},
		"RecentTriggerFirings": func() error {
			_, err := store.RecentTriggerFirings(ctx, env.ORM, user, []string{"tr-none"}, 10)
			return err
		},
		"FiringsSinceLastSuccess": func() error {
			_, err := store.FiringsSinceLastSuccess(ctx, env.ORM, user, []string{"tr-none"}, 10)
			return err
		},
		"ListWorktreesForReclaim": func() error {
			_, err := store.ListWorktreesForReclaim(ctx, env.ORM)
			return err
		},
		"ListHeldWorktreesForUser": func() error {
			_, err := store.ListHeldWorktreesForUser(ctx, env.ORM, user)
			return err
		},
		"ConsumeOAuthFlow": func() error {
			_, err := store.ConsumeOAuthFlow(ctx, env.ORM, []byte("state-none"), at)
			return err
		},
		"SetWorkflowDraftStatus": func() error {
			_, err := store.SetWorkflowDraftStatus(ctx, env.ORM, "wd-none", core.WorkflowDraftStatusDraft)
			return err
		},
		"RemoveWorkflowDraft": func() error {
			_, err := store.RemoveWorkflowDraft(ctx, env.ORM, "wd-none")
			return err
		},
		"ClaimRunEvents": func() error {
			_, err := store.ClaimRunEvents(ctx, env.ORM, at, at.Add(time.Minute), 10)
			return err
		},
		"ClaimQueuedAgentMessagesForThread": func() error {
			_, err := store.ClaimQueuedAgentMessagesForThread(ctx, env.ORM, "th-none", "chat-none", "")
			return err
		},
	}

	before := map[string]string{}
	for name, q := range queries {
		before[name] = fmt.Sprint(q())
	}

	// What a migration does: chats_with_activity re-created with a column
	// appended (as 20261021231046 did), and a column added to each table.
	mustExec(t, env, `DO $$ BEGIN
		EXECUTE 'CREATE OR REPLACE VIEW chats_with_activity AS SELECT v.*, 0 AS added_by_test FROM ('
			|| regexp_replace(pg_get_viewdef('chats_with_activity'::regclass), ';\s*$', '') || ') v';
	END $$`)
	for _, table := range []string{
		"threads", "context_windows", "tool_calls", "triggers", "trigger_events", "worktrees",
		"oauth_flows", "workflow_drafts", "run_events", "agent_messages",
	} {
		mustExec(t, env, `ALTER TABLE `+table+` ADD COLUMN added_by_test integer`)
	}

	for name, q := range queries {
		if after := fmt.Sprint(q()); after != before[name] {
			t.Errorf("%s after a column was added = %s, want what it answered before: %s", name, after, before[name])
		}
	}
}
