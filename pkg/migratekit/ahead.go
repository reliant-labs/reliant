package migratekit

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/migratekit/compat"
)

// SCHEMA AHEAD OF THE BINARY — the previous release, mid-deploy.
//
// Every rolling deploy runs the PREVIOUS release's code against the NEXT
// release's schema for a while: the new release migrates first, and the old
// ReplicaSet keeps serving — and keeps booting pods, on a reschedule or a
// scale-up — until the rollout completes. An old pod that migrates at boot
// (AUTO_MIGRATE) meets a recorded version it does not embed, and
// golang-migrate reports that as
//
//	no migration found for version 92: read down for version 92 ...: file does not exist
//
// — a hard error that crash-loops the release still carrying the traffic.
//
// Neither obvious answer is right. Failing always breaks every deploy that
// adds a migration; tolerating always runs old code on a schema nobody
// checked it against. The answer is a FACT the older binary can check: whether every version it does not know was declared
// backward-compatible by its author (see package compat). The older binary
// has never seen those files, so it cannot read the declaration itself — the
// NEWER migrator records it in the database as it applies them, in
// compatTable, and the older one reads it back.
//
// Versions a compat-unaware migrator applied have no row, and are treated as
// NOT compatible: an unproven claim is refused, never assumed.

// compatTable is the bookkeeping table the migrator keeps beside golang-
// migrate's schema_migrations: one row per migration version a compat-aware
// binary embedded, with what that migration declared. Unqualified, so it
// lands in the same schema (the search_path's first) schema_migrations does.
const compatTable = "schema_migrations_compat"

// compatLockKey serializes the table's create-and-upsert across replicas
// racing the same release. CREATE TABLE IF NOT EXISTS is not race-free in
// postgres (two creators can collide on the catalog's unique index), and
// golang-migrate's own advisory lock is only held inside its Up call.
const compatLockKey = 7250513029716893481

// Ahead describes a schema one or more versions past the newest migration this
// binary embeds, where every such version was declared backward-compatible.
// It is a SUCCESS state — the binary's code can run — and it is reported, not
// swallowed, because "the database is ahead of the code" is the single most
// important thing an operator mid-deploy needs to see confirmed.
type Ahead struct {
	// Version is the schema version recorded in the database.
	Version uint
	// Latest is the newest migration version this binary embeds.
	Latest uint
	// Versions is every version the database has that this binary does not
	// embed, in order. Each was declared backward-compatible.
	Versions []uint
}

// SchemaAheadError is a schema ahead of the binary that the binary CANNOT
// prove it is compatible with. Nothing was applied.
//
// It is a type, not a string, so a caller's policy (and a test) can tell
// "old code meeting a schema change nobody declared safe" apart from
// every other migration failure.
type SchemaAheadError struct {
	// Version is the schema version recorded in the database.
	Version uint
	// Latest is the newest migration version this binary embeds.
	Latest uint
	// Incompatible is every version this binary does not embed that is not
	// recorded as backward-compatible — either declared otherwise, or
	// applied by a migrator that recorded nothing.
	Incompatible []uint
	// Unrecorded is true when the database holds no compatibility record
	// for the schema's own version: it was migrated by a binary that
	// predates the record, so nothing about the extra versions is known.
	Unrecorded bool
}

func (e *SchemaAheadError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "database schema is at version %d, AHEAD of the newest migration this binary embeds (%d) — "+
		"this is an older release running against a newer release's database", e.Version, e.Latest)
	if e.Unrecorded {
		fmt.Fprintf(&b, "; the database holds no compatibility record for version %d (it was migrated by a binary that predates %s), "+
			"so nothing proves this binary's code runs against it", e.Version, compatTable)
	} else {
		fmt.Fprintf(&b, "; version(s) %s are not declared backward-compatible (%q in the .up.sql), "+
			"so this binary's code is not known to run against them", joinVersions(e.Incompatible), compat.Directive)
	}
	b.WriteString(". NOTHING WAS APPLIED and this binary refuses to serve. Forge rolls forward only: run the release " +
		"that owns this schema (or a newer one). A schema is never stepped back. Write migrations " +
		"expand/contract and mark the expand half `" + compat.Directive + "` so the previous release keeps " +
		"working through a deploy. See the forge skill db/deploy-migrations")
	return b.String()
}

func joinVersions(vs []uint) string {
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = fmt.Sprint(v)
	}
	return strings.Join(parts, ", ")
}

// sourceSet is what this binary embeds: its migrations, in version order.
type sourceSet []compat.Migration

func (s sourceSet) has(version uint) bool {
	for _, m := range s {
		if m.Version == version {
			return true
		}
	}
	return false
}

func (s sourceSet) latest() uint {
	if len(s) == 0 {
		return 0
	}
	return s[len(s)-1].Version
}

// classifyAhead decides what a schema at `state` means for a binary embedding
// `source`. It returns (nil, nil) when the schema is NOT ahead — the ordinary
// case, where golang-migrate's Up is the right thing to run.
//
// When it is ahead, the verdict comes from compatTable: every recorded version
// the binary does not know, up to the schema's version, must be declared
// compatible, and the schema's own version must be recorded at all.
func classifyAhead(ctx context.Context, conn *sql.Conn, source sourceSet, state State) (*Ahead, error) {
	if !state.Applied || state.Dirty || source.has(state.Version) {
		return nil, nil
	}
	records, err := readCompat(ctx, conn)
	if err != nil {
		return nil, err
	}
	aheadErr := &SchemaAheadError{Version: state.Version, Latest: source.latest()}
	if _, ok := records[state.Version]; !ok {
		aheadErr.Unrecorded = true
		aheadErr.Incompatible = []uint{state.Version}
		return nil, aheadErr
	}
	var versions []uint
	for v := range records {
		if v <= state.Version && !source.has(v) {
			versions = append(versions, v)
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	for _, v := range versions {
		if !records[v] {
			aheadErr.Incompatible = append(aheadErr.Incompatible, v)
		}
	}
	if len(aheadErr.Incompatible) > 0 {
		return nil, aheadErr
	}
	return &Ahead{Version: state.Version, Latest: source.latest(), Versions: versions}, nil
}

// readCompat returns version → declared-compatible from compatTable. An absent
// table is an empty record, not an error: every database migrated before this
// record existed has none.
func readCompat(ctx context.Context, conn *sql.Conn) (map[uint]bool, error) {
	var exists bool
	if err := conn.QueryRowContext(ctx, "SELECT to_regclass('"+compatTable+"') IS NOT NULL").Scan(&exists); err != nil {
		return nil, fmt.Errorf("read migration compatibility record: %w", err)
	}
	out := map[uint]bool{}
	if !exists {
		return out, nil
	}
	rows, err := conn.QueryContext(ctx, "SELECT version, backward_compatible FROM "+compatTable)
	if err != nil {
		return nil, fmt.Errorf("read migration compatibility record: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			v  int64
			ok bool
		)
		if err := rows.Scan(&v, &ok); err != nil {
			return nil, fmt.Errorf("read migration compatibility record: %w", err)
		}
		out[uint(v)] = ok
	}
	return out, rows.Err()
}

// recordCompat upserts one row per embedded migration with what it declares.
//
// It runs BEFORE golang-migrate's Up, on every run, including one with nothing
// to apply. Before, so a crash between applying a version and recording it
// cannot leave an applied version unrecorded (which an older binary would
// then have to refuse). Every run, so a database migrated before this record
// existed gains it on the first run of a binary that keeps it. Rows for
// versions not yet applied are harmless: a reader only considers versions at
// or below the schema's.
//
// The statement is built from LITERALS — numbers this package parsed from
// filenames, and true/false — so it needs no array binding and runs the same
// under every database/sql postgres driver.
func recordCompat(ctx context.Context, conn *sql.Conn, source sourceSet) error {
	if len(source) == 0 {
		return nil
	}
	values := make([]string, len(source))
	for i, m := range source {
		values[i] = fmt.Sprintf("(%d, %t)", m.Version, m.BackwardCompatible)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("record migration compatibility: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmts := []string{
		fmt.Sprintf("SELECT pg_advisory_xact_lock(%d)", int64(compatLockKey)),
		"CREATE TABLE IF NOT EXISTS " + compatTable + ` (
			version BIGINT PRIMARY KEY,
			backward_compatible BOOLEAN NOT NULL,
			recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		"INSERT INTO " + compatTable + " (version, backward_compatible) VALUES " + strings.Join(values, ", ") +
			" ON CONFLICT (version) DO UPDATE SET backward_compatible = EXCLUDED.backward_compatible, recorded_at = now()" +
			" WHERE " + compatTable + ".backward_compatible IS DISTINCT FROM EXCLUDED.backward_compatible",
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("record migration compatibility: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("record migration compatibility: %w", err)
	}
	return nil
}

// scanSource reads what the binary embeds, and refuses a set whose adoption
// declaration (adopt.go) is misplaced.
func scanSource(fsys fs.FS, dir string) (sourceSet, error) {
	ms, err := compat.Scan(fsys, dir)
	if err != nil {
		return nil, err
	}
	source := sourceSet(ms)
	if err := validateAdoption(source); err != nil {
		return nil, err
	}
	return source, nil
}
