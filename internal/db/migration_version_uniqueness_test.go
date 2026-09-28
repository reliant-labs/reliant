// Copyright (c) 2025 Reliant Labs
package db

import (
	"io/fs"
	"path"
	"testing"
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
func TestMigrationVersionsAreUnique(t *testing.T) {
	files, err := fs.Glob(FS, "migrations/postgres/*.sql")
	if err != nil {
		t.Fatalf("glob embedded migrations: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("expected embedded migrations, got none")
	}

	byVersion := make(map[string][]string, len(files))
	for _, file := range files {
		name := path.Base(file)
		m := migrationVersion.FindStringSubmatch(name)
		if m == nil {
			// Filename shape is TestNewMigrationsCarryARealTimestamp's job.
			continue
		}
		byVersion[m[1]] = append(byVersion[m[1]], name)
	}

	for version, names := range byVersion {
		if len(names) > 1 {
			t.Errorf("version %s is claimed by %d migrations (%v); goose records only the version, so a database "+
				"that applied one of these reports the others as already applied and silently skips their SQL. "+
				"Renumber the newer one with `goose -dir internal/db/migrations/postgres create <name> sql`",
				version, len(names), names)
		}
	}
}
