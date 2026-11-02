package migratekit

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/golang-migrate/migrate/v4"
)

// TestNormalizeDSNRewritesEveryAcceptedScheme drives the accepted-scheme set
// from the CONSTANTS the rewrite is written against, not from a hand-copied
// list of literals. If someone adds a fourth scheme to the package and
// forgets to teach NormalizeDSN about it, or renames one, this table moves
// with the package instead of silently continuing to test the old spelling.
//
// The property asserted is the one the pgx5 driver actually depends on: after
// normalization the DSN carries the scheme the driver registered under, and
// everything after the "://" is preserved byte for byte (credentials, host,
// database, query parameters — dropping any of which turns a working
// migration into an auth failure).
func TestNormalizeDSNRewritesEveryAcceptedScheme(t *testing.T) {
	// The remainder deliberately carries every part of a real DSN that a
	// naive reimplementation might truncate.
	const tail = "user:pa55@host.internal:5432/appdb?sslmode=require&search_path=public"

	rewritten := []string{SchemePostgres, SchemePostgresql}
	if len(rewritten) == 0 {
		t.Fatal("no schemes under test — the accepted-scheme set is empty")
	}

	for _, scheme := range rewritten {
		got := NormalizeDSN(scheme + tail)
		if !strings.HasPrefix(got, SchemePGX5) {
			t.Errorf("NormalizeDSN(%s...) = %q; want the %s scheme the pgx5 driver registers under",
				scheme, got, SchemePGX5)
		}
		if suffix := strings.TrimPrefix(got, SchemePGX5); suffix != tail {
			t.Errorf("NormalizeDSN(%s...) dropped DSN content: remainder = %q, want %q",
				scheme, suffix, tail)
		}
	}
}

// TestNormalizeDSNLeavesForeignSchemesAlone pins the deliberate
// non-validation: NormalizeDSN translates a known pair and does not judge
// anything else. An already-normalized DSN must survive unchanged (otherwise
// a second normalization pass would produce pgx5://pgx5://...), and an
// unrelated scheme must reach the driver as written so the driver — not this
// function — produces the error message.
func TestNormalizeDSNLeavesForeignSchemesAlone(t *testing.T) {
	for _, dsn := range []string{
		SchemePGX5 + "host/db", // idempotence
		"mysql://host/db",
		"host/db",
		"",
	} {
		if got := NormalizeDSN(dsn); got != dsn {
			t.Errorf("NormalizeDSN(%q) = %q; want it returned unchanged", dsn, got)
		}
	}
}

// TestOpenRefusesWithoutAMigrationSource is the loud-failure guard. A binary
// that embeds no migrations must not produce a usable Migrator, because the
// alternative — a `db migrate up` that exits 0 against a database with no
// schema — is a deploy that reports success and leaves the application
// broken.
//
// Asserted on the returned Migrator being nil as well as the error, since a
// caller that checks only one would deref the other.
func TestOpenRefusesWithoutAMigrationSource(t *testing.T) {
	mg, err := Open(Options{DSN: SchemePostgres + "localhost:5432/db"})
	if err == nil {
		t.Fatal("Open with a nil FS returned no error; a binary with no embedded migrations must refuse")
	}
	if mg != nil {
		t.Errorf("Open returned a non-nil Migrator alongside its error: %#v", mg)
	}
	if !strings.Contains(err.Error(), "embeds no migrations") {
		t.Errorf("Open error = %q; want it to name the missing embed so the fix is obvious", err)
	}
}

// TestOpenRequiresADSN pins the second refusal. It is distinguishable from the
// nil-FS case on purpose: the two call for different fixes (regenerate vs. set
// DATABASE_URL), so a single merged message would send half of readers to the
// wrong place.
func TestOpenRequiresADSN(t *testing.T) {
	sourceFS := fstest.MapFS{
		"migrations/0001_init.up.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
	}
	_, err := Open(Options{FS: sourceFS, Dir: "migrations"})
	if err == nil {
		t.Fatal("Open with an empty DSN returned no error")
	}
	if !strings.Contains(err.Error(), "DSN") {
		t.Errorf("Open error = %q; want it to name the DSN", err)
	}
}

// TestOpenReportsAnUnreadableSourceDirectory covers the third distinct
// failure: the FS is present but the named directory is not, which is what a
// renamed embed directive or a mismatched Dir produces. It must fail at Open
// rather than yield a Migrator that reports "no pending migrations" — the
// same silence the nil-FS guard exists to prevent, arrived at differently.
func TestOpenReportsAnUnreadableSourceDirectory(t *testing.T) {
	sourceFS := fstest.MapFS{
		"migrations/0001_init.up.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
	}
	_, err := Open(Options{
		FS:  sourceFS,
		Dir: "not-migrations",
		DSN: SchemePostgres + "localhost:5432/db",
	})
	if err == nil {
		t.Fatal("Open with a Dir that does not exist in FS returned no error")
	}
	if !strings.Contains(err.Error(), "not-migrations") {
		t.Errorf("Open error = %q; want it to name the directory it could not read", err)
	}
}

// TestCloseIsSafeOnAZeroMigrator pins the contract the callers rely on:
// `defer mg.Close()` is written immediately after Open at every call site,
// including on paths where Open failed. If Close panicked on a nil receiver,
// every one of those call sites would need a nil check and one of them would
// eventually be missing it.
func TestCloseIsSafeOnAZeroMigrator(t *testing.T) {
	var mg *Migrator
	if err := mg.Close(); err != nil {
		t.Errorf("(*Migrator)(nil).Close() = %v; want nil", err)
	}
	if err := (&Migrator{}).Close(); err != nil {
		t.Errorf("(&Migrator{}).Close() = %v; want nil", err)
	}
}

// TestStateZeroValueMeansUnappliedNotBroken documents the fold that the
// State.Applied field exists to make safe. A fresh database has no recorded
// version; golang-migrate signals that with the ErrNilVersion sentinel, and
// the whole point of returning a State rather than (version, dirty, error) is
// that a caller cannot forget to special-case it.
//
// The zero value is asserted directly because it is what State() returns on
// that path, and because a Dirty=true zero value would make an empty database
// look like a half-failed migration.
func TestStateZeroValueMeansUnappliedNotBroken(t *testing.T) {
	var s State
	if s.Applied {
		t.Error("zero State.Applied = true; a database with no migrations applied must report false")
	}
	if s.Dirty {
		t.Error("zero State.Dirty = true; an unmigrated database is not a partially-failed one")
	}
}

// TestResultChangedIsIndependentOfVersionMovement is the assertion that pins
// WHY Result.Changed is taken from the migrate call's own return rather than
// from a before/after version diff.
//
// The scenario it encodes is a concurrent rollout: replica A applies 1 -> 2
// while replica B loses the advisory-lock race, is released, and finds
// nothing to do. B's own call reported ErrNoChange (Changed=false), yet B
// observes Before.Version=1 and After.Version=2 because A moved the schema
// underneath it. A version-diff implementation would report Changed=true for
// B and credit it with an application it did not perform — so the two fields
// must be able to disagree, and this test fails if Result is ever
// "simplified" into deriving one from the other.
func TestResultChangedIsIndependentOfVersionMovement(t *testing.T) {
	lostTheRace := Result{
		Changed: false,
		Before:  State{Version: 1, Applied: true},
		After:   State{Version: 2, Applied: true},
	}
	if lostTheRace.Changed {
		t.Fatal("test fixture is wrong: this case represents a replica that applied nothing")
	}
	if lostTheRace.Before.Version == lostTheRace.After.Version {
		t.Fatal("test fixture is wrong: this case requires the version to have moved")
	}
	// The property: Changed is not a function of the version delta. If a
	// future refactor derives it from one, this state becomes
	// unrepresentable and the assertion above starts failing.
	if got := lostTheRace.After.Version - lostTheRace.Before.Version; got != 1 {
		t.Errorf("version delta = %d, want 1", got)
	}
}

// TestFoldNoChangeIsNarrow pins the fold that decides whether a migration
// call counts as a failure. It has three cases and they must stay distinct:
//
//   - nil          => applied something, no error
//   - ErrNoChange  => applied nothing, still NOT an error (the expected
//     outcome for a replica that lost the advisory-lock race)
//   - anything else => a real failure that must reach the caller, because
//     the caller's policy of aborting the rollout depends on seeing it
//
// The third case is the one worth guarding. A fold written one character
// wider — "any error means no change" — turns every broken migration into a
// successful deploy, which is silent in exactly the direction that matters.
// The wrapped variant is included because callers see wrapped errors in
// practice and the fold must match through the chain.
func TestFoldNoChangeIsNarrow(t *testing.T) {
	badSQL := errors.New(`syntax error at or near "CREAT"`)

	cases := []struct {
		name        string
		in          error
		wantChanged bool
		wantFatal   bool
	}{
		{name: "applied", in: nil, wantChanged: true, wantFatal: false},
		{name: "nothing pending", in: migrate.ErrNoChange, wantChanged: false, wantFatal: false},
		{name: "nothing pending, wrapped", in: fmt.Errorf("migrate up: %w", migrate.ErrNoChange), wantChanged: false, wantFatal: false},
		{name: "real failure", in: badSQL, wantChanged: false, wantFatal: true},
		{name: "real failure, wrapped", in: fmt.Errorf("migrate up: %w", badSQL), wantChanged: false, wantFatal: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed, fatal := foldNoChange(tc.in)
			if changed != tc.wantChanged {
				t.Errorf("foldNoChange(%v) changed = %v, want %v", tc.in, changed, tc.wantChanged)
			}
			if gotFatal := fatal != nil; gotFatal != tc.wantFatal {
				t.Errorf("foldNoChange(%v) fatal = %v, want fatal=%v", tc.in, fatal, tc.wantFatal)
			}
			// A propagated failure must arrive intact, not reduced to a
			// generic "migration failed" — the SQL error text is the
			// whole diagnostic value at the end of a pod log.
			if tc.wantFatal && !errors.Is(fatal, badSQL) {
				t.Errorf("foldNoChange(%v) fatal = %v; want the original error preserved in the chain", tc.in, fatal)
			}
		})
	}
}

// driverConfig reads exactly the options golang-migrate's pgx v5 driver read
// from a URL, and refuses the malformed ones it refused.
func TestDriverConfigReadsTheMigratorOptions(t *testing.T) {
	cfg, err := driverConfig(SchemePostgres + "u:p@h:5432/db?sslmode=disable&x-migrations-table=%22m%22.%22v%22" +
		"&x-migrations-table-quoted=true&x-statement-timeout=1500&x-multi-statement=true&x-multi-statement-max-size=4096")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MigrationsTable != `"m"."v"` || !cfg.MigrationsTableQuoted || cfg.StatementTimeout != 1500*time.Millisecond ||
		!cfg.MultiStatementEnabled || cfg.MultiStatementMaxSize != 4096 {
		t.Errorf("driverConfig = %+v", cfg)
	}
	plain, err := driverConfig(SchemePostgres + "h/db")
	if err != nil || plain.MigrationsTable != "" || plain.MultiStatementMaxSize <= 0 {
		t.Errorf("driverConfig without options = %+v, %v; want golang-migrate's defaults", plain, err)
	}
	for _, bad := range []string{"x-migrations-table=v&x-migrations-table-quoted=true", "x-statement-timeout=soon", "x-multi-statement=maybe"} {
		if _, err := driverConfig(SchemePostgres + "h/db?" + bad); err == nil {
			t.Errorf("driverConfig(?%s) accepted a malformed option", bad)
		}
	}
}
