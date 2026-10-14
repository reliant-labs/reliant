// Copyright (c) 2025 Reliant Labs
package db

import "embed"

// FS holds goose's migration history, FROZEN when reliant moved to forge
// migrations (db/migrations). Its schema is the baseline there, which lists
// every version here so that a database goose migrated is adopted rather than
// rebuilt.
//
// It is still compiled in for one job: the goose bridge (goose_bridge.go). A
// database an older goose release migrated, and that never ran the last one,
// is brought to goose's frozen head by goose itself, from these files, before
// forge's migrator adopts it. Nothing else applies them, and they never change
// (TestGooseHistoryIsFrozen).
//
//go:embed migrations/postgres/*.sql
var FS embed.FS
