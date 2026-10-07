// Copyright (c) 2025 Reliant Labs
package db

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/db/migrationcheck"
)

// A goose version is a migration's ONLY identity in goose_db_version: the
// database records "20260923000000 applied" and nothing about which file ran
// under it. Two files claiming one version is therefore not a lint nit, it is
// a schema corruption waiting for a deploy — and it has happened twice.
//
// On 2026-09-24, #293 added 20260923000000_access_tokens_retire_pats.sql
// against an existing 20260923000000_temporal_payload_blobs.sql. Databases that
// ran main in the hour before #294 renumbered it executed the access-tokens
// SQL under the payload-blobs version, so they have access_tokens, no
// temporal_payload_blobs, and a still-pending 20260925000000 that then fails
// on `CREATE TABLE access_tokens`. api-server could not start, and repairing
// it took an idempotent rewrite plus a whole restore migration
// (20260928193015_restore_temporal_payload_blobs.sql).
//
// TestNewMigrationsCarryARealTimestamp attacks the same root cause from the
// other side: it rejects the hand-picked times of day that make a collision
// likely. This one rejects the collision itself, and unlike that test it has
// no grandfather clause — a duplicate is unsafe at every version, including
// the hand-numbered ones. The two together are cheap; the failure mode is not.
//
// The rule itself is migrationcheck.RuleUniqueVersion, shared with
// scripts/check-migrations.sh.
func TestMigrationVersionsAreUnique(t *testing.T) {
	assertMigrationRule(t, migrationcheck.RuleUniqueVersion)
}
