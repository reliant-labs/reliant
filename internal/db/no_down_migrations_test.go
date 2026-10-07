// Copyright (c) 2025 Reliant Labs
package db

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/db/migrationcheck"
)

// goose runs whatever follows a `-- +goose Down` marker when asked to roll a
// migration back. We never roll back — recovery is a new migration that rolls
// forward from the state the database is actually in — so a Down section is
// SQL nobody will run on purpose and nobody has tested against real data.
// Fail on any Down marker, empty or not, so a template or copy-paste cannot
// reintroduce one.
//
// The rule itself is migrationcheck.RuleNoGooseDown, shared with
// scripts/check-migrations.sh.
func TestMigrationsHaveNoGooseDownSection(t *testing.T) {
	assertMigrationRule(t, migrationcheck.RuleNoGooseDown)
}
