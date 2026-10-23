package migratekit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/golang-migrate/migrate/v4"

	"github.com/reliant-labs/forge/pkg/migratekit/compat"
)

// ADOPTING A DATABASE ANOTHER TOOL MIGRATED.
//
// A project that moves onto forge from goose squashes goose's history into one
// baseline migration that declares it (`-- forge:adopts goose <versions>`, see
// compat/adopt.go). Two kinds of database then meet the same binary:
//
//   - a FRESH one, which applies the baseline as SQL like any migration;
//   - one GOOSE ALREADY MIGRATED, whose schema already is the baseline. Running
//     the baseline there fails on its first CREATE TABLE and leaves the schema
//     marked dirty — or, for IF NOT EXISTS DDL, half-succeeds.
//
// So before anything else reads the record, a database golang-migrate has
// never touched is checked for goose's table. If it has one, the baseline is
// ADOPTED — recorded as applied, none of its SQL run — but only when goose's
// applied set is EXACTLY the set the baseline lists:
//
//   - a listed version goose never applied means the database is BEHIND the
//     baseline. This binary no longer carries that migration, so it cannot
//     finish the history; the previous release can.
//   - an applied version the list does not hold means the database is not the
//     schema the baseline describes (a branch's migration on a shared
//     database, or a baseline cut before the last goose migration merged).
//
// Both refuse with *AdoptionError and change nothing. Everything after the
// baseline then applies as ordinary forge migrations, on the same run.
//
// WHY IT RUNS ON THE NORMAL PATH AND NOT AS A COMMAND. The databases this is for
// include desktop installs, where nobody can run a one-off step, and an
// initContainer that runs `db migrate up` and nothing else. A declaration in
// the embedded baseline reaches every one of them; a command reaches the ones
// someone remembers.
//
// WHY goose's TABLE IS LEFT ALONE. It is the previous release's whole view of
// the schema. Leaving it intact is what keeps rolling BACK to that release
// safe: goose finds every version it knows applied and does nothing. It also
// means adoption never re-runs: once golang-migrate records a version, this
// check never looks at goose again.
//
// ADOPTING A SQUASHED FORGE HISTORY (`-- forge:adopts forge <versions>`, which
// `forge db squash` writes). The databases that history built have the
// baseline's schema and a forge record of how they got it: schema_migrations
// at the history's last version, and a schema_migrations_applied row for each
// version applied. This binary carries none of those versions, and without
// the declaration every check misreads them — "AHEAD" when the baseline is
// numbered above the history, "MISSING" below it. With it, a database is a
// candidate when its schema version is one the baseline lists, and it is
// adopted when every listed version is recorded as applied: the baseline gets
// its applied row and becomes the schema's version (Force), none of its SQL
// runs, and the migrations after it apply as usual on the same run.
//
//   - A listed version with no applied row: the database is BEHIND the
//     squash. The release before the squash still carries that migration;
//     this one cannot.
//   - A schema version the list does not hold (when the database shares no
//     migration with this binary): the squash was cut before that migration
//     merged.
//
// A database that predates the applied record has only golang-migrate's high
// water mark, which golang-migrate never applies below: the listed versions
// at or below it count as applied, as bootstrapApplied counts them.
//
// The old rows stay, like goose's table and for the same reason: the release
// before the squash finds every version it knows recorded under its own
// files. A squash numbered above its history, with `-- forge:backward-
// compatible` (squash writes both), is then a version that release reads as
// a compatible schema ahead of it, so rolling back to it keeps working.

// gooseTable is goose's default version table. Unqualified, as goose itself
// creates it: it resolves through the search_path, like every table here.
const gooseTable = "goose_db_version"

// adoptionListCap bounds how many versions an AdoptionError prints per list.
// A database behind by a whole history would otherwise print 146 timestamps;
// the error's fields still carry every one.
const adoptionListCap = 12

// Adopted reports a database this call adopted: the baseline was recorded as
// applied and none of its SQL ran.
type Adopted struct {
	// Tool is the migration tool whose history was adopted (compat.ToolGoose,
	// or compat.ToolForge for a squashed forge history).
	Tool string
	// Version is the baseline's version, now the schema's.
	Version uint
	// Name is the baseline's filename.
	Name string
	// Versions is how many of the tool's versions were verified applied.
	Versions int
}

// String is the line a command prints for an adoption.
func (a *Adopted) String() string {
	if a.Tool == compat.ToolForge {
		return fmt.Sprintf("ADOPTED a database the forge migrations %s squashed had built: %s recorded as applied without running its SQL "+
			"(%d forge versions verified; their applied records left in place)", a.Name, a.Name, a.Versions)
	}
	return fmt.Sprintf("ADOPTED a database %s migrated: %s recorded as applied without running its SQL "+
		"(%d %s versions verified; %s's own table left in place)", a.Tool, a.Name, a.Versions, a.Tool, a.Tool)
}

// AdoptionError is a database another tool migrated whose recorded history is
// not the history the baseline replaces. Nothing was applied.
//
// It is a type so a caller (and a test) can tell "this database is not the
// schema the baseline describes" from every other migration failure. The two
// lists call for different fixes, and the message names both.
type AdoptionError struct {
	// Tool is the tool that migrated the database (compat.ToolGoose).
	Tool string
	// Table is the tool's version table that was read.
	Table string
	// Baseline is the migration that declares the adoption.
	Baseline string
	// Declared is how many versions the baseline lists.
	Declared int
	// Missing is every listed version the database never applied: it is
	// BEHIND the baseline.
	Missing []int64
	// Unexpected is every version the database applied that the baseline
	// does not list.
	Unexpected []int64
}

func (e *AdoptionError) Error() string {
	if e.Tool == compat.ToolForge {
		return e.forgeError()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "this database was migrated by %s (%s), and its history is not the one %s replaces", e.Tool, e.Table, e.Baseline)
	if len(e.Missing) > 0 {
		fmt.Fprintf(&b, "; it has NOT applied %d of the %d %s migration(s) the baseline lists: %s — this binary no longer carries them, "+
			"so it cannot finish %s's history: run the last release that still migrates with %s against this database first "+
			"(it applies them), then this one, which adopts the database without re-running anything",
			len(e.Missing), e.Declared, e.Tool, capVersions(e.Missing), e.Tool, e.Tool)
	}
	if len(e.Unexpected) > 0 {
		fmt.Fprintf(&b, "; it HAS applied %d %s migration(s) the baseline does not list: %s — either the baseline was cut before they "+
			"merged (re-cut it from %s's full history so its `%s %s` lines include them), or they are a branch's migrations "+
			"applied to a shared database (reconcile the schema by hand, then remove their rows: "+
			"`DELETE FROM %s WHERE version_id IN (...)`)",
			len(e.Unexpected), e.Tool, capVersions(e.Unexpected), e.Tool, compat.AdoptsDirective, e.Tool, e.Table)
	}
	b.WriteString(". NOTHING WAS APPLIED. See the forge skill db/deploy-migrations, \"Adopting a database another tool migrated\"")
	return b.String()
}

// forgeError is Error for a squashed forge history, whose remedies differ:
// the release before the squash is the one that finishes the history, and a
// short list is re-cut with `forge db squash`.
func (e *AdoptionError) forgeError() string {
	var b strings.Builder
	fmt.Fprintf(&b, "this database was built by forge migrations, and its history is not the one %s squashed", e.Baseline)
	if len(e.Missing) > 0 {
		fmt.Fprintf(&b, "; it has NOT applied %d of the %d migration(s) the baseline lists: %s — this binary no longer carries them, "+
			"so it cannot finish that history: run the release before the squash against this database first (it applies them), "+
			"then this one, which adopts the database without re-running anything",
			len(e.Missing), e.Declared, capVersions(e.Missing))
	}
	if len(e.Unexpected) > 0 {
		fmt.Fprintf(&b, "; its schema is at version %s, which the baseline does not list — the squash was cut before that "+
			"migration merged: re-run `forge db squash` over the full history, so its `%s %s` lines include it",
			capVersions(e.Unexpected), compat.AdoptsDirective, compat.ToolForge)
	}
	b.WriteString(". NOTHING WAS APPLIED. See the forge skill db/deploy-migrations, \"Squashing a forge history\"")
	return b.String()
}

// capVersions renders up to adoptionListCap versions and counts the rest.
func capVersions(vs []int64) string {
	n := len(vs)
	if n > adoptionListCap {
		n = adoptionListCap
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprint(vs[i])
	}
	s := strings.Join(parts, ", ")
	if len(vs) > n {
		s += fmt.Sprintf(" … and %d more", len(vs)-n)
	}
	return s
}

// adopter returns the migration that declares an adoption. scanSource has
// already refused any but the first.
func (s sourceSet) adopter() (compat.Migration, bool) {
	if len(s) > 0 && s[0].Adopts != nil {
		return s[0], true
	}
	return compat.Migration{}, false
}

// validateAdoption refuses an adoption declared anywhere but the FIRST
// migration. A database another tool migrated has run none of this project's
// migrations, so the only one it can already reflect is the one that replaced
// that tool's history — the baseline every other migration builds on.
// Declaring it later would record the earlier migrations as applied on a
// database that never ran them.
//
// It refuses at Open and at boot on every database, fresh ones included, so
// the mistake fails the first test run rather than the first adopted
// database. `forge lint` reports the same rule at review time.
func validateAdoption(source sourceSet) error {
	for i, m := range source {
		if m.Adopts == nil || i == 0 {
			continue
		}
		return fmt.Errorf("migration %s declares %q, but only the FIRST migration — the baseline that replaced %s's history — may: "+
			"a database %s migrated has run none of this project's migrations, and %s comes before it",
			m.Name, compat.AdoptsDirective, m.Adopts.Tool, m.Adopts.Tool, source[0].Name)
	}
	if baseline, ok := source.adopter(); ok && baseline.Adopts.Tool == compat.ToolForge {
		if carried := source.carried(baseline.Adopts.Versions); len(carried) > 0 {
			return fmt.Errorf("migration %s declares `%s %s` for version(s) %s, which this binary still carries: a squash replaces "+
				"the migrations it lists, so a listed version can be neither the baseline's own nor a file beside it — "+
				"number the baseline outside the history it squashed, and move the squashed files out of the directory",
				baseline.Name, compat.AdoptsDirective, compat.ToolForge, capVersions(carried))
		}
	}
	return nil
}

// carried returns the versions in vs that the set embeds.
func (s sourceSet) carried(vs []int64) []int64 {
	var out []int64
	for _, v := range vs {
		if v > 0 && s.has(uint(v)) {
			out = append(out, v)
		}
	}
	return out
}

// adopt adopts a database another tool migrated, when the embedded baseline
// declares that history and golang-migrate has recorded nothing yet. It
// returns the state to proceed from — re-read, because this call or a racing
// replica may have just changed it — and what it adopted, if anything.
//
// CONCURRENCY. Both callers already hold the run lock (runlock.go), so
// replicas of a release adopt one at a time, through either entry point. On
// top of that, the check-and-record holds appliedLockKey and RE-READS the
// state under it. That is what keeps it correct against a binary that
// predates the run lock, and it serializes adoption with bootstrapApplied,
// which decides the applied record's first rows just as adoption does. A
// replica that waited finds the version recorded and does nothing. The
// version is written through golang-migrate itself (Force, under its own lock
// and into whatever table it is configured with) while appliedLockKey is
// still held, so no replica can observe a half-adopted database.
//
// appliedLockKey is held at SESSION level, not inside the record's
// transaction, because golang-migrate's Force runs on this same session and
// opens a transaction of its own (runlock.go, ONE SESSION).
//
// CRASH SAFETY. The applied row is committed first, then Force writes the
// version. A crash before the commit leaves nothing; a crash between the two
// leaves the row without the version, so the database still reads as
// unadopted and the next run adopts it again — the row's insert is a no-op
// the second time.
func adopt(ctx context.Context, conn *sql.Conn, m *migrate.Migrate, source sourceSet, state State) (State, *Adopted, error) {
	baseline, ok := source.adopter()
	if !ok {
		return state, nil, nil
	}
	if baseline.Adopts.Tool == compat.ToolForge {
		return adoptForge(ctx, conn, m, source, baseline, state)
	}
	if state.Applied {
		return state, nil, nil
	}
	tool := baseline.Adopts.Tool
	var adopted *Adopted
	err := withSessionLock(ctx, conn, appliedLockKey, func() error {
		// Another replica may have adopted (and even applied more) while
		// this one waited for the lock.
		var err error
		if state, err = stateOf(m); err != nil || state.Applied {
			return err
		}
		found, err := recordAdoption(ctx, conn, baseline)
		if err != nil || !found {
			// Not found: a fresh database, where the baseline applies
			// as SQL.
			return err
		}
		if err := m.Force(int(baseline.Version)); err != nil {
			return fmt.Errorf("record %s as applied: %w", baseline.Name, err)
		}
		if state, err = stateOf(m); err != nil {
			return err
		}
		adopted = &Adopted{Tool: tool, Version: baseline.Version, Name: baseline.Name, Versions: len(baseline.Adopts.Versions)}
		return nil
	})
	var adoptErr *AdoptionError
	if err != nil && !errors.As(err, &adoptErr) {
		err = fmt.Errorf("adopt %s-migrated database: %w", tool, err)
	}
	if err != nil {
		return state, nil, err
	}
	return state, adopted, nil
}

// recordAdoption records baseline as applied when the database's goose
// history is exactly the one it declares, and refuses with *AdoptionError
// when it is not. found is false for a database goose never touched. The
// transaction is committed before it returns, so the Force that follows runs
// on a session with none open.
func recordAdoption(ctx context.Context, conn *sql.Conn, baseline compat.Migration) (found bool, err error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	applied, found, err := readGooseApplied(ctx, tx)
	if err != nil || !found {
		return found, err
	}
	missing, unexpected := diffAdoption(baseline.Adopts.Versions, applied)
	if len(missing) > 0 || len(unexpected) > 0 {
		return true, &AdoptionError{
			Tool: baseline.Adopts.Tool, Table: gooseTable, Baseline: baseline.Name, Declared: len(baseline.Adopts.Versions),
			Missing: missing, Unexpected: unexpected,
		}
	}

	stmts := []string{
		"CREATE TABLE IF NOT EXISTS " + appliedTable + ` (
			version BIGINT PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		fmt.Sprintf("INSERT INTO %s (version, name) VALUES (%d, %s) ON CONFLICT (version) DO NOTHING",
			appliedTable, baseline.Version, quoteLiteral(baseline.Descriptor)),
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return true, err
		}
	}
	return true, tx.Commit()
}

// readGooseApplied returns the set of versions goose considers applied, and
// whether goose's table exists at all.
//
// goose keeps an append log, not a set: one row per apply, and — in older
// releases — one more with is_applied=false per rollback (newer ones delete the
// row instead). The LATEST row for a version decides, exactly as goose itself
// reads it (EnsureDBVersion walks rows by descending id). Version 0 is the
// row goose writes when it creates the table, not a migration.
func readGooseApplied(ctx context.Context, tx *sql.Tx) (map[int64]bool, bool, error) {
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT to_regclass('"+gooseTable+"') IS NOT NULL").Scan(&exists); err != nil {
		return nil, false, fmt.Errorf("look for %s: %w", gooseTable, err)
	}
	if !exists {
		return nil, false, nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT version_id, is_applied FROM "+gooseTable+" ORDER BY id")
	if err != nil {
		return nil, true, fmt.Errorf("read %s: %w", gooseTable, err)
	}
	defer func() { _ = rows.Close() }()
	latest := map[int64]bool{}
	for rows.Next() {
		var (
			v       int64
			applied bool
		)
		if err := rows.Scan(&v, &applied); err != nil {
			return nil, true, fmt.Errorf("read %s: %w", gooseTable, err)
		}
		latest[v] = applied
	}
	if err := rows.Err(); err != nil {
		return nil, true, fmt.Errorf("read %s: %w", gooseTable, err)
	}
	out := map[int64]bool{}
	for v, applied := range latest {
		if applied && v != 0 {
			out[v] = true
		}
	}
	return out, true, nil
}

// diffAdoption compares the versions a baseline declares against the set a
// database applied. Set membership, never a highest-version comparison: see
// compat/adopt.go on why the maximum proves nothing under out-of-order
// application.
func diffAdoption(declared []int64, applied map[int64]bool) (missing, unexpected []int64) {
	want := make(map[int64]bool, len(declared))
	for _, v := range declared {
		want[v] = true
		if !applied[v] {
			missing = append(missing, v)
		}
	}
	for v := range applied {
		if !want[v] {
			unexpected = append(unexpected, v)
		}
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
	sort.Slice(unexpected, func(i, j int) bool { return unexpected[i] < unexpected[j] })
	return missing, unexpected
}
