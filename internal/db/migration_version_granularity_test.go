// Copyright (c) 2025 Reliant Labs
package db

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/db/migrationcheck"
)

// A goose version is the only identity a migration has in goose_db_version:
// the database records "20260926000000 applied" and nothing about which file
// that was. Two branches that each hand-write a migration dated the same day
// with a zeroed time of day both claim that version, and a shared database
// that applied one branch's file then reports the other's as already applied.
// That happened on 2026-09-26: a WIP `agent_messages_synthesized` ran as
// 20260926000000, main's `add_workflow_owner_user_id` was skipped, and chat
// creation failed on the missing column.
//
// `goose create` stamps the current time to the second, which makes a
// collision practically impossible. Hand-picked times (000000, 000001,
// 100000, ...) are what collide, so a new migration must carry a real
// timestamp. Versions up to migrationcheck.LastHandNumberedVersion predate
// this rule and are left alone: renaming an applied migration makes goose run
// it again.
//
// The rules themselves are migrationcheck.RuleFilename and RuleTimestamp,
// shared with scripts/check-migrations.sh.
func TestNewMigrationsCarryARealTimestamp(t *testing.T) {
	assertMigrationRule(t, migrationcheck.RuleFilename, migrationcheck.RuleTimestamp)
}
