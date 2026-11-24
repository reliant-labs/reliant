// Copyright (c) 2026 Reliant Labs
package accountpurge

import (
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
)

// TestEveryChatKeyedTableIsDeletedWithItsChat is the guard for a table that
// stores something per chat: account purge (purgeSteps) and project delete
// (store.ProjectDeleteStatements) must each remove its rows, or a chat_id
// foreign key must cascade them from the chats both delete. A run-scoped
// table with no foreign key, such as node_observations, is otherwise left
// behind for good, and nothing errors.
//
// The set of tables is read from the migrated schema, so a new migration with
// a chat_id column fails here until the table is added to both lists, or to
// chatTablesKept with the reason.
func TestEveryChatKeyedTableIsDeletedWithItsChat(t *testing.T) {
	_, raw, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()

	rows, err := raw.Query(`
		SELECT c.table_name,
		       EXISTS (
		         SELECT 1 FROM information_schema.key_column_usage k
		           JOIN information_schema.referential_constraints rc
		             ON rc.constraint_name = k.constraint_name AND rc.constraint_schema = k.constraint_schema
		          WHERE k.table_schema = c.table_schema AND k.table_name = c.table_name
		            AND k.column_name = 'chat_id' AND rc.delete_rule = 'CASCADE')
		  FROM information_schema.columns c
		  JOIN information_schema.tables t
		    ON t.table_schema = c.table_schema AND t.table_name = c.table_name AND t.table_type = 'BASE TABLE'
		 WHERE c.table_schema = current_schema() AND c.column_name = 'chat_id'
		 ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cascades := map[string]bool{}
	for rows.Next() {
		var table string
		var cascade bool
		if err := rows.Scan(&table, &cascade); err != nil {
			t.Fatal(err)
		}
		cascades[table] = cascade
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !cascades["node_observations"] && len(cascades) < 10 {
		t.Fatalf("the schema read found %d chat-keyed tables; the query is wrong", len(cascades))
	}

	purge := make([]string, len(purgeSteps))
	for i, s := range purgeSteps {
		purge[i] = s.sql
	}
	lists := []struct {
		name, key  string
		statements []string
	}{
		{"account purge (purgeSteps in internal/accountpurge/accountpurge.go)", listAccountPurge, purge},
		{"project delete (chatPurgeSteps in internal/store/project_delete.go)", listProjectDelete, store.ProjectDeleteStatements()},
	}

	tables := make([]string, 0, len(cascades))
	for table := range cascades {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		for _, list := range lists {
			deleted := deletesFrom(list.statements, table) || cascades[table] && deletesFrom(list.statements, "chats")
			_, kept := chatTablesKept[list.key][table]
			gap := slices.Contains(knownChatTableGaps[table], list.key)
			switch {
			case !deleted && !kept && !gap:
				t.Errorf("%s has a chat_id column and %s never deletes it: add a DELETE for it there, or, if it must outlive its chat, add it to chatTablesKept with the reason", table, list.name)
			case deleted && (kept || gap):
				t.Errorf("%s deletes %s now: remove it from chatTablesKept / knownChatTableGaps", list.name, table)
			}
		}
	}
	for key, kept := range chatTablesKept {
		for table := range kept {
			if _, ok := cascades[table]; !ok {
				t.Errorf("chatTablesKept[%s] names %s, which has no chat_id column (any more): remove it", key, table)
			}
		}
	}
	for table := range knownChatTableGaps {
		if _, ok := cascades[table]; !ok {
			t.Errorf("knownChatTableGaps names %s, which has no chat_id column (any more): remove it", table)
		}
	}

	// The ledger tables are kept on their own retention period, and the
	// retention pass must reach each one.
	for _, table := range []string{"step_executions", "node_observations"} {
		if !deletesFrom(store.LedgerRetentionStatements(), table) {
			t.Errorf("the ledger retention pass (internal/db/ledger_retention.go) never deletes from %s", table)
		}
	}
}

const (
	listAccountPurge  = "account purge"
	listProjectDelete = "project delete"
)

// chatTablesKept are tables with a chat_id column that account purge or
// project delete leaves alone on purpose, each with the reason. A new entry
// needs a reason a reviewer can check against the table's migration.
var chatTablesKept = map[string]map[string]string{
	listAccountPurge: {
		"purged_chats":     "the tombstone of a deleted chat: it must outlive the chat so a late writer is refused (chat_write_admission)",
		"purged_workflows": "the tombstone of a deleted chat's workflows, for the same reason",
	},
	listProjectDelete: {
		"purged_chats":        "the tombstone of a deleted chat: it must outlive the chat so a late writer is refused (chat_write_admission)",
		"purged_workflows":    "the tombstone of a deleted chat's workflows, for the same reason",
		"llm_call_usage":      "the count-only usage record, keyed by user (no content: TestLLMUsageTablesHoldNoContent); account purge deletes it, its own 90-day retention otherwise",
		"attention_actors":    "kept on purpose: deleting a chat must not remove its fence (migration 20261121040111_automation_attention)",
		"attention_incidents": "kept on purpose: a resolved incident's fence outlives its chat (migration 20261121040111_automation_attention)",
		"user_activations":    "kept on purpose: deleting a user's history must not reset the milestone (migration 20261113124004_user_activations)",
	},
}

// knownChatTableGaps are the chat-keyed tables a list leaves rows of behind
// today, found when this guard was written (reliant#3503). They are
// listed so the guard holds every new table from now on; delete an entry
// when its list deletes the table, and the guard then holds it too.
var knownChatTableGaps = map[string][]string{
	"attention_actors":            {listAccountPurge},
	"attention_incidents":         {listAccountPurge},
	"user_activations":            {listAccountPurge},
	"chat_message_counts_pending": {listAccountPurge, listProjectDelete},
	"chat_thread_message_counts":  {listAccountPurge, listProjectDelete},
	"integration_writes":          {listAccountPurge, listProjectDelete},
	"provider_backoff":            {listAccountPurge, listProjectDelete},
	"trigger_changes":             {listAccountPurge, listProjectDelete},
	"trigger_events":              {listAccountPurge, listProjectDelete},
	"video_generation_jobs":       {listAccountPurge, listProjectDelete},
	"background_processes":        {listProjectDelete},
	"workflow_drafts":             {listProjectDelete},
}

// deletesFrom reports whether any statement deletes from table.
func deletesFrom(statements []string, table string) bool {
	re := regexp.MustCompile(`(?i)\bDELETE\s+FROM\s+` + regexp.QuoteMeta(table) + `\b`)
	for _, s := range statements {
		if re.MatchString(strings.TrimSpace(s)) {
			return true
		}
	}
	return false
}
