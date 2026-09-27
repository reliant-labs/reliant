// Copyright (c) 2025 Reliant Labs
package db

import (
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"testing"
	"time"
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
// timestamp. Versions up to lastHandNumberedVersion predate this rule and
// are left alone: renaming an applied migration makes goose run it again.
const lastHandNumberedVersion = 20260926000001

var migrationVersion = regexp.MustCompile(`^(\d{14})_.+\.sql$`)

func TestNewMigrationsCarryARealTimestamp(t *testing.T) {
	files, err := fs.Glob(FS, "migrations/postgres/*.sql")
	if err != nil {
		t.Fatalf("glob embedded migrations: %v", err)
	}
	for _, file := range files {
		name := path.Base(file)
		m := migrationVersion.FindStringSubmatch(name)
		if m == nil {
			t.Errorf("%s: want a YYYYMMDDHHMMSS_<name>.sql filename — create migrations with `goose -dir internal/db/migrations/postgres create <name> sql`", name)
			continue
		}
		version, _ := strconv.ParseInt(m[1], 10, 64)
		if version <= lastHandNumberedVersion {
			continue
		}
		if _, err := time.Parse("20060102150405", m[1]); err != nil {
			t.Errorf("%s: version %s is not a valid YYYYMMDDHHMMSS timestamp: %v", name, m[1], err)
			continue
		}
		hhmm, mmss := m[1][8:12], m[1][10:14]
		if hhmm == "0000" || mmss == "0000" {
			t.Errorf("%s: version %s has a hand-picked time of day; two branches doing the same on the same day claim the same version, "+
				"and goose then treats one's file as the other's. Create it with `goose -dir internal/db/migrations/postgres create <name> sql` "+
				"(or rename it to the current UTC time: `date -u +%%Y%%m%%d%%H%%M%%S`)", name, m[1])
		}
	}
}
