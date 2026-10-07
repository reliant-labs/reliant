// Copyright (c) 2025 Reliant Labs
package db

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/db/migrationcheck"
)

// assertMigrationRule holds the embedded migrations — the ones that ship — to
// one of internal/db/migrationcheck's rules. scripts/check-migrations.sh runs
// the same rules over the files on disk, so the two cannot disagree.
func assertMigrationRule(t *testing.T, rules ...migrationcheck.Rule) {
	t.Helper()
	report, err := migrationcheck.Check(FS, "migrations/postgres")
	if err != nil {
		t.Fatalf("check embedded migrations: %v", err)
	}
	if report.Files == 0 {
		t.Fatal("expected embedded migrations, got none")
	}
	for _, problem := range report.Problems {
		for _, rule := range rules {
			if problem.Rule == rule {
				t.Error(problem)
			}
		}
	}
}
