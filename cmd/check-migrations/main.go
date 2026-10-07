// Copyright (c) 2025 Reliant Labs

// Command check-migrations checks the Postgres migration files on disk against
// the rules in internal/db/migrationcheck: YYYYMMDDHHMMSS_<name>.sql filenames,
// real timestamps, unique versions, and no `-- +goose Down` sections.
//
// scripts/check-migrations.sh runs it, and so does the check-migrations CI
// workflow. Run it from the repository root:
//
//	go run ./cmd/check-migrations
//	go run ./cmd/check-migrations -dir path/to/migrations
//
// It lists every violation and exits 1 when any rule is broken or the
// migrations cannot be read.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/reliant-labs/reliant/internal/db/migrationcheck"
)

func main() {
	dir := flag.String("dir", "internal/db/migrations/postgres", "directory holding the migration files")
	flag.Parse()

	fmt.Printf("🔍 Checking migrations in %s\n", *dir)
	if info, err := os.Stat(*dir); err != nil || !info.IsDir() {
		fmt.Printf("❌ Migration directory not found: %s\n", *dir)
		os.Exit(1)
	}
	report, err := migrationcheck.Check(os.DirFS(*dir), ".")
	if err != nil {
		fmt.Printf("❌ %s: %v\n", *dir, err)
		os.Exit(1)
	}
	fmt.Printf("📁 Found %d migration files\n", report.Files)
	if !report.OK() {
		for _, problem := range report.Problems {
			fmt.Printf("❌ %s\n", problem)
		}
		fmt.Printf("❌ %d migration problem(s) found\n", len(report.Problems))
		os.Exit(1)
	}
	fmt.Println("✅ Migration filenames, versions and contents look good")
}
