package migratekit

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/golang-migrate/migrate/v4"
)

// A VERSION NUMBER IS NOT A MIGRATION'S IDENTITY.
//
// golang-migrate records one integer: the schema is "at 93". It cannot say
// WHICH 93. Two branches that each add a migration numbered 93 both apply
// cleanly to a shared database, and when one of them is renumbered before it
// merges, the database stays stamped with the branch's 91..93 while main's
// 91..93 — different files — never run. Every later `up` agrees the schema is
// current, and the first symptom is a query for a column that does not exist,
// usually in a crash-looping pod far from the migration that owns it.
//
// So the migrator keeps a second fact beside the version: for each version it
// applied, the descriptor of the file it ran (appliedTable). Before applying
// anything, it checks every recorded version this binary also embeds against
// the file the binary has under that number, and refuses on a mismatch.
//
// Only the DESCRIPTOR is compared, never a content hash. A hash would turn
// every comment fix or whitespace strip in an old migration into a refusal to
// boot, while the failure this exists for — a different migration under the
// same number — always changes the name.
//
// Versions applied before this record existed have no row and are not
// checked. They are deliberately NOT backfilled from the binary's own
// filenames: that would certify whatever the database already holds, which is
// exactly the state this check cannot vouch for.

// appliedTable is the per-version record of which file was applied.
// Unqualified, like schema_migrations and compatTable, so it lands in the
// search_path's first schema.
const appliedTable = "schema_migrations_applied"

// appliedLockKey serializes the table's create-and-insert across replicas
// that finish the same release's Up concurrently. See compatLockKey.
const appliedLockKey = 7250513029716893482

// MigrationMismatch is one version whose applied file is not the file this
// binary embeds under that number.
type MigrationMismatch struct {
	Version uint
	// Applied is the descriptor recorded when the version was applied.
	Applied string
	// Embedded is the descriptor this binary carries for the same version.
	Embedded string
}

// MigrationMismatchError reports versions the database applied from a
// different migration file than the one this binary embeds under the same
// number. Nothing was applied.
//
// It is a type so a caller (and a test) can tell "the schema is not the one
// these version numbers describe" from every other migration failure: the
// fix is a human reconciling the schema, never a retry.
type MigrationMismatchError struct {
	Mismatches []MigrationMismatch
}

func (e *MigrationMismatchError) Error() string {
	var b strings.Builder
	b.WriteString("the database applied DIFFERENT migrations under version numbers this binary uses — " +
		"usually a branch's migrations applied to a shared database and then renumbered before merge:")
	for _, m := range e.Mismatches {
		fmt.Fprintf(&b, " version %d was applied as %q, but this binary's %d is %q;", m.Version, m.Applied, m.Version, m.Embedded)
	}
	first := e.Mismatches[0].Version
	fmt.Fprintf(&b, " NOTHING WAS APPLIED. The schema_migrations version does not describe this schema, so "+
		"no later migration can be trusted to apply. Reconcile by hand: apply this binary's migrations from %d "+
		"that the database lacks, set schema_migrations to the highest version the schema now truly reflects, "+
		"then correct the record so each version names the file the schema actually reflects — "+
		"`UPDATE %s SET name = '<descriptor>' WHERE version = <version>` for each one listed above.",
		first, appliedTable)
	// Deleting those rows instead would leave versions at or below the
	// schema's with no record, which is the definition of a MISSING
	// migration (see missing.go) — the next run would refuse for a second
	// reason and the runbook would loop. The record must be corrected in
	// place, not cleared.
	return b.String()
}

// verifyApplied checks every version the database recorded as applied, at or
// below the schema's current version, against the file this binary embeds
// under that number. Versions the binary does not embed are the schema-ahead
// case (classifyAhead's business).
//
// A version with an EMPTY recorded descriptor is skipped: that is a bootstrap
// row, written when appliedTable was created over a database that predates it
// (see bootstrapApplied). It asserts the version was applied and deliberately
// says nothing about which file — the one thing such a database cannot
// vouch for. Comparing against it would manufacture a mismatch on every
// pre-record database.
//
// A version with no row at all is findMissing's business, not a mismatch.
func verifyApplied(records map[uint]string, source sourceSet, state State) error {
	if !state.Applied {
		return nil
	}
	var mismatches []MigrationMismatch
	for _, m := range source {
		if m.Version > state.Version {
			break
		}
		if applied, ok := records[m.Version]; ok && applied != "" && applied != m.Descriptor {
			mismatches = append(mismatches, MigrationMismatch{Version: m.Version, Applied: applied, Embedded: m.Descriptor})
		}
	}
	if len(mismatches) > 0 {
		return &MigrationMismatchError{Mismatches: mismatches}
	}
	return nil
}

// readApplied returns version → descriptor from appliedTable. An absent table
// is an empty record: every database migrated before it existed has none.
func readApplied(ctx context.Context, conn *sql.Conn) (map[uint]string, error) {
	_, out, err := readAppliedRecord(ctx, conn)
	return out, err
}

// queryer is what reading a record needs: the session itself, or a
// transaction open on it (database/sql blocks a Conn while its Tx is open).
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// readAppliedRecord is readApplied that also says whether appliedTable
// exists: no table (the database predates the record) and an empty one (it
// applied nothing yet) are different facts to adoption.
func readAppliedRecord(ctx context.Context, q queryer) (exists bool, records map[uint]string, err error) {
	if err := q.QueryRowContext(ctx, "SELECT to_regclass('"+appliedTable+"') IS NOT NULL").Scan(&exists); err != nil {
		return false, nil, fmt.Errorf("read applied-migration record: %w", err)
	}
	out := map[uint]string{}
	if !exists {
		return false, out, nil
	}
	rows, err := q.QueryContext(ctx, "SELECT version, name FROM "+appliedTable)
	if err != nil {
		return true, nil, fmt.Errorf("read applied-migration record: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			v    int64
			name string
		)
		if err := rows.Scan(&v, &name); err != nil {
			return true, nil, fmt.Errorf("read applied-migration record: %w", err)
		}
		out[uint(v)] = name
	}
	return true, out, rows.Err()
}

// recordApplied records the descriptor of every embedded version in
// (before, after] — what the Up that moved the schema from before to after ran.
//
// It runs AFTER golang-migrate's Up, because until then nothing was applied;
// a crash in between leaves those versions unrecorded, which only means they
// go unchecked. Existing rows are never overwritten: a row states what was
// applied, and a later binary disagreeing with it is the mismatch
// verifyApplied reports, not a correction. A replica that lost the
// advisory-lock race sees the same (before, after] and inserts nothing new.
//
// DO NOTHING also protects the empty descriptors bootstrapApplied writes.
// Those rows mean "applied, by a migrator that did not record which file",
// and this binary's filename is not evidence of what that database ran — so
// an upgrade must leave them empty rather than fill them in with a guess.
func recordApplied(ctx context.Context, conn *sql.Conn, source sourceSet, before, after State) error {
	values := appliedRows(source, before, after)
	if len(values) == 0 {
		return nil
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("record applied migrations: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmts := []string{
		fmt.Sprintf("SELECT pg_advisory_xact_lock(%d)", int64(appliedLockKey)),
		"CREATE TABLE IF NOT EXISTS " + appliedTable + ` (
			version BIGINT PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		"INSERT INTO " + appliedTable + " (version, name) VALUES " + strings.Join(values, ", ") +
			" ON CONFLICT (version) DO NOTHING",
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("record applied migrations: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("record applied migrations: %w", err)
	}
	return nil
}

// appliedRows renders the appliedTable rows for every embedded version in
// (before, after], each under this binary's descriptor.
func appliedRows(source sourceSet, before, after State) []string {
	if !after.Applied {
		return nil
	}
	var values []string
	for _, m := range source {
		if (before.Applied && m.Version <= before.Version) || m.Version > after.Version {
			continue
		}
		values = append(values, fmt.Sprintf("(%d, %s)", m.Version, quoteLiteral(m.Descriptor)))
	}
	return values
}

// recordFailedRun records what an Up that FAILED did apply: every version
// before the one that failed.
//
// golang-migrate applies pending versions one at a time and stops at the first
// that fails, leaving the schema dirty AT it. Each version before it ran to
// completion, and golang-migrate's version says so. Left out of this record,
// they become indistinguishable from migrations the schema passed without
// running, so the very recovery a dirty schema calls for — repair it, force the
// failed version, run Up — was refused, naming every one of them as MISSING
// (missing.go). The failed version itself is recorded only when a Force
// asserts it (see Migrator.Force).
func recordFailedRun(ctx context.Context, conn *sql.Conn, m *migrate.Migrate, source sourceSet, before State) error {
	after, err := stateOf(m)
	if err != nil {
		return fmt.Errorf("record the migrations applied before the failure: %w", err)
	}
	return recordApplied(ctx, conn, source, before, after.settled())
}

// forceApplied makes the applied record agree with a forced version, in tx,
// on a session that holds appliedLockKey. See Migrator.Force.
//
// A row above version is deleted: the schema no longer claims it, and a later
// file under that number would otherwise trip as a mismatch. Every embedded
// version the force newly asserts — above the version the schema had settled
// at (before), up to and including the forced one — is recorded under this
// binary's descriptor, as Up records what it applies. Versions at or below the
// settled one keep whatever they have: a force asserts the schema's new
// position, not that an older gap was filled.
//
// A database with no record yet is left without one. Creating it here would
// skip bootstrapApplied, which backfills only when IT creates the table, and
// every older version would then read as missing; the next Up bootstraps it
// from the forced version instead, as it does any database that predates it.
func forceApplied(ctx context.Context, tx *sql.Tx, source sourceSet, before State, version int) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT to_regclass('"+appliedTable+"') IS NOT NULL").Scan(&exists); err != nil {
		return fmt.Errorf("read applied-migration record: %w", err)
	}
	if !exists {
		return nil
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE version > %d", appliedTable, version)); err != nil {
		return fmt.Errorf("record forced version: %w", err)
	}
	if version < 0 {
		return nil
	}
	values := appliedRows(source, before.settled(), State{Version: uint(version), Applied: true})
	if len(values) == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO "+appliedTable+" (version, name) VALUES "+
		strings.Join(values, ", ")+" ON CONFLICT (version) DO NOTHING"); err != nil {
		return fmt.Errorf("record forced version: %w", err)
	}
	return nil
}

// quoteLiteral renders s as a postgres string literal. The descriptor comes
// from a filename in the binary's own embed, but a filename may hold a quote,
// and a statement built by concatenation must not care.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
