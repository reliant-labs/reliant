package migratekit

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// A MIGRATION THAT ARRIVES AFTER THE SCHEMA PASSED IT.
//
// golang-migrate tracks ONE number: the schema is "at 93". Its readUp reads
// that number and applies only migrations ABOVE it. So a migration numbered
// BELOW the current version can never run — golang-migrate does not look
// there, and reports nothing missing. It has no allow-missing option.
//
// With timestamp versions this happens exactly one way: two branches are cut,
// A allocates an earlier timestamp than B, and B merges and DEPLOYS first.
// Main now holds A's migration at a version the database has already passed.
// Every later `up` agrees the schema is current, and A's SQL never runs. The
// first symptom is a query for a column that does not exist, usually in a
// crash-looping pod far from the migration that owns it.
//
// So the migrator checks, before applying anything: every version this binary
// embeds at or below the schema's current version must have a row in
// appliedTable. One that does not was never applied and never will be. That
// is a MissingMigrationError, and nothing runs.
//
// WHY REFUSE RATHER THAN APPLY IT. Applying a late migration out of order is
// expressible (goose's WithAllowMissing does it) and it silently makes the
// schema depend on merge ORDER: A-then-B and B-then-A produce different
// databases from the same commit, and nothing reports which one you got. A
// DROP COLUMN landing after the migration that reads the column is a
// different schema from the reverse. For the least reversible thing that
// ships, the loud refusal beats the quiet guess.
//
// The measurement behind that choice: across 108 migrations of control-plane
// history there were ten same-version collisions (which timestamps remove
// outright) and ZERO out-of-order arrivals. This check guards a case that is
// rare by construction, so refusing costs almost nothing and assuming costs
// a schema nobody reviewed.
//
// TWO FIXES, AND THE BINARY CANNOT TELL WHICH ONE APPLIES. When the late file
// is still on its branch it has run nowhere, and the fix is a rename. But the
// refusal is usually first seen AFTER the file merged: reliant's
// 20261024024221 merged to main below a 20261024035148 that main and prod
// already held, and the first thing to notice was the release's crash-looping
// migrator. By then every database created from main since the merge has run
// the file at its old version, so renaming it alone breaks those databases
// (the copy re-runs SQL they already hold), and `forge db migration rebase`
// refuses it. The fix for a merged file is to REPLACE it: delete it and add
// the same SQL, written to be re-runnable, as a new migration. A database
// that ran the old version runs the copy as a no-op; one that skipped it runs
// it for the first time. The orphaned record the old version leaves behind
// is harmless — verifyApplied judges only the versions this binary embeds.
// The message states both, keyed on the one fact the reader can check:
// whether the file is on their default branch.

// RebaseCommand is the forge command that re-versions a migration, named by
// this package's refusal message.
//
// It is spelled out here rather than imported because pkg/ may not depend on
// forge's internal packages — this error travels into user projects, which
// link only pkg. The internal copy lives in
// internal/linter/migrationlint.RebaseCommand, and a test in internal/cli
// asserts that both spellings resolve to a real CLI command. That test is the
// reason for this comment: this string was advice for a subcommand that did
// not exist, and a user following it hit "unknown command".
const RebaseCommand = "forge db migration rebase"

// NewMigrationCommand is the forge command that creates a migration, named
// by the refusal's fix for a file that has already merged. Spelled out here
// for the same reason as RebaseCommand, and held to a real command by the
// same internal/cli test.
const NewMigrationCommand = "forge db migration new"

// MissingMigration is one version this binary embeds that the database
// skipped: at or below the schema's version, with no applied row.
type MissingMigration struct {
	Version uint
	// Name is the migration's filename, so the fix names a real file.
	Name string
}

// MissingMigrationError reports migrations that can never run: their version
// is at or below the schema's current version, so golang-migrate will not
// look at them again. Nothing was applied.
//
// It is a type so a caller (and a test) can tell "this release is carrying
// SQL that will never execute" from every other migration failure. The fix is
// always to give the migration a version NEWER than the schema has reached,
// never a retry.
type MissingMigrationError struct {
	Missing []MissingMigration
	// Version is the schema version the database has reached.
	Version uint
}

func (e *MissingMigrationError) Error() string {
	var b strings.Builder
	b.WriteString("this binary embeds migration(s) the database has already passed, so they can NEVER be applied — " +
		"usually a branch whose migration was versioned before another branch merged and deployed ahead of it:")
	for _, m := range e.Missing {
		fmt.Fprintf(&b, " version %d (%s);", m.Version, m.Name)
	}
	fmt.Fprintf(&b, " the schema is at version %d, and migrations at or below it are never re-read. "+
		"NOTHING WAS APPLIED. Each file needs a version NEWER than %d, and how depends on whether it has merged to "+
		"your default branch. NOT YET MERGED: it has run nowhere, so rename it — `%s <file>` gives it a fresh timestamp. "+
		"ALREADY MERGED: databases created since the merge have run it at its old version, so a rename would re-run "+
		"SQL they already hold (and rebase refuses it). Replace it instead: delete the file and add the same SQL as a "+
		"new migration (`%s <name>`), written to be re-runnable (CREATE ... IF NOT EXISTS, ADD COLUMN IF NOT EXISTS) — "+
		"a database that ran the old version runs the copy as a no-op, and this one runs it for the first time. "+
		"If instead the SQL was already applied to this database by hand, record that fact with "+
		"`INSERT INTO %s (version, name) VALUES (<version>, '<descriptor>')`",
		e.Version, e.Version, RebaseCommand, NewMigrationCommand, appliedTable)
	return b.String()
}

// findMissing returns every embedded version at or below the schema's version
// with no applied row.
//
// It runs AFTER bootstrapApplied, and that ordering is the whole correctness
// argument. A database migrated before appliedTable existed has no rows for
// ANY version, which is indistinguishable here from every migration being
// missing — bootstrap resolves that first by recording what the schema's own
// version already vouches for. After it, an absent row means the version was
// genuinely never applied.
func findMissing(records map[uint]string, source sourceSet, state State) *MissingMigrationError {
	if !state.Applied {
		return nil
	}
	var missing []MissingMigration
	for _, m := range source {
		if m.Version > state.Version {
			break
		}
		if _, ok := records[m.Version]; !ok {
			missing = append(missing, MissingMigration{Version: m.Version, Name: m.Name})
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].Version < missing[j].Version })
	return &MissingMigrationError{Missing: missing, Version: state.Version}
}

// bootstrapApplied gives a database that predates appliedTable the record it
// would have had, ONCE, at the moment the table is created.
//
// WHAT IT WRITES AND WHY ONLY THAT. Every embedded version at or below the
// schema's current version gets a row, because golang-migrate's high-water
// mark is precisely the claim that those ran. The descriptor is left EMPTY,
// never backfilled from this binary's filenames: a descriptor states which
// FILE was applied, and the one fact an old database cannot vouch for is
// which file it ran under a given number. Writing this binary's names there
// would certify exactly the state the mismatch check exists to catch.
//
// WHY AT CREATION AND NOWHERE ELSE. The bootstrap and a genuinely missing
// migration look identical from a single row's absence — both are "no row at
// a version the schema has passed". They are told apart by WHEN: before the
// table exists, an absent row means nobody was recording; after, it means the
// version never ran. Creating the table and backfilling it in ONE transaction
// makes that boundary a fact in the database rather than a heuristic. A
// backfill that ran on every startup would re-absolve every missing migration
// forever and silently disable the check.
func bootstrapApplied(ctx context.Context, conn *sql.Conn, source sourceSet, state State) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("bootstrap applied-migration record: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// The advisory lock serializes replicas racing the same release;
	// CREATE TABLE IF NOT EXISTS is not race-free in postgres. See
	// compatLockKey.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("SELECT pg_advisory_xact_lock(%d)", int64(appliedLockKey))); err != nil {
		return fmt.Errorf("bootstrap applied-migration record: %w", err)
	}

	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT to_regclass('"+appliedTable+"') IS NOT NULL").Scan(&exists); err != nil {
		return fmt.Errorf("bootstrap applied-migration record: %w", err)
	}
	if exists {
		// Already bootstrapped by an earlier run or another replica.
		return nil
	}

	if _, err := tx.ExecContext(ctx, "CREATE TABLE "+appliedTable+` (
		version BIGINT PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("bootstrap applied-migration record: %w", err)
	}

	// A fresh database has applied nothing, so there is nothing to vouch
	// for and the table starts empty — which is what makes the check live
	// from the project's very first migration.
	if state.Applied {
		var values []string
		for _, m := range source {
			if m.Version > state.Version {
				break
			}
			values = append(values, fmt.Sprintf("(%d, '')", m.Version))
		}
		if len(values) > 0 {
			if _, err := tx.ExecContext(ctx, "INSERT INTO "+appliedTable+" (version, name) VALUES "+
				strings.Join(values, ", ")+" ON CONFLICT (version) DO NOTHING"); err != nil {
				return fmt.Errorf("bootstrap applied-migration record: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("bootstrap applied-migration record: %w", err)
	}
	return nil
}

// checkApplied is the whole pre-apply verdict on the applied record: bootstrap
// a database that predates it, then refuse both a version applied from a
// different file (mismatch) and a version that can never be applied (missing).
//
// The two are reported separately because they call for different fixes — a
// mismatch is a schema a human must reconcile, a missing migration is a file
// to re-version — and mismatch is checked first: it means the recorded
// versions do not describe this schema at all, which makes every other
// conclusion drawn from them unreliable.
func checkApplied(ctx context.Context, conn *sql.Conn, source sourceSet, state State) error {
	if err := bootstrapApplied(ctx, conn, source, state); err != nil {
		return err
	}
	records, err := readApplied(ctx, conn)
	if err != nil {
		return err
	}
	if err := verifyApplied(records, source, state); err != nil {
		return err
	}
	if err := findMissing(records, source, state); err != nil {
		return err
	}
	return nil
}
