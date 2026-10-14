// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/reliant-labs/forge/pkg/migratekit"
	"github.com/reliant-labs/forge/pkg/migratekit/compat"
)

// querier is what PlanMigration reads through: a *sql.DB, a *sql.Conn, or the
// read-only *sql.Tx `reliant db migrate status` runs it in.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// MigrationPlan is what Migrate will do to a database, read without changing
// anything. Migrate's goose bridge decides from it, and `reliant db migrate
// status` prints it, so the preview and the run cannot disagree about the
// goose half. The forge half past the baseline is migratekit's to decide at
// run time; the plan reports the facts it decides from.
type MigrationPlan struct {
	// State is what forge's migrator recorded in schema_migrations.
	State migratekit.State
	// Baseline is the first embedded migration, the one that adopts goose.
	Baseline compat.Migration
	// Embedded is every forge migration version this binary embeds,
	// ascending.
	Embedded []int64
	// Pending is every Embedded version State has not applied.
	Pending []int64
	// Goose is the database's goose history, read only while forge has
	// recorded nothing: once it has, goose's table is never read again. Nil
	// when there is no goose_db_version table or forge has a record.
	Goose *GooseHistory
	// Unrecorded is true when neither tool recorded anything, yet reliant's
	// tables exist: building the baseline there would fail on its first
	// CREATE TABLE.
	Unrecorded bool
}

// PlanMigration reads the database's migration state and sets it against what
// this binary embeds. It only reads.
func PlanMigration(ctx context.Context, q querier) (MigrationPlan, error) {
	ms, err := embeddedMigrations()
	if err != nil {
		return MigrationPlan{}, err
	}
	baseline := ms[0]
	if baseline.Adopts == nil || baseline.Adopts.Tool != compat.ToolGoose {
		return MigrationPlan{}, fmt.Errorf("the first embedded migration, %s, declares no goose history to adopt", baseline.Name)
	}
	// The bridge applies goose's embedded files and the migrator then checks
	// them against the baseline's list, so the two must be the same set. A
	// test pins it; a binary built otherwise must not touch a database.
	files, err := gooseFileVersions()
	if err != nil {
		return MigrationPlan{}, err
	}
	if !slices.Equal(files, baseline.Adopts.Versions) {
		return MigrationPlan{}, fmt.Errorf("this binary's goose history (%d files) is not the history %s adopts (%d versions); it was built from a broken tree",
			len(files), baseline.Name, len(baseline.Adopts.Versions))
	}

	p := MigrationPlan{Baseline: baseline}
	for _, m := range ms {
		p.Embedded = append(p.Embedded, int64(m.Version))
	}
	if p.State, err = readMigrationState(ctx, q); err != nil {
		return MigrationPlan{}, fmt.Errorf("read schema_migrations: %w", err)
	}
	p.Pending = pendingVersions(p.State, p.Embedded)
	if p.State.Applied || p.State.Dirty {
		return p, nil
	}

	goose, found, err := readGooseHistory(ctx, q, baseline.Adopts.Versions)
	if err != nil {
		return MigrationPlan{}, err
	}
	if found {
		p.Goose = goose
		return p, nil
	}
	if err := q.QueryRowContext(ctx, `SELECT to_regclass('chats') IS NOT NULL OR to_regclass('projects') IS NOT NULL`).Scan(&p.Unrecorded); err != nil {
		return MigrationPlan{}, fmt.Errorf("look for reliant's tables: %w", err)
	}
	return p, nil
}

// needsGooseCatchUp reports whether the goose bridge has work to do: a goose
// database forge has not adopted, behind goose's frozen head.
func (p MigrationPlan) needsGooseCatchUp() bool {
	return p.Goose != nil && len(p.Goose.Missing) > 0
}

// bridgeRefusal is the goose bridge's own refusal, made before goose runs:
// nil when the database is fresh, adopted, adoptable, or behind and safely
// catchable-up. Everything else is migratekit's to refuse.
func (p MigrationPlan) bridgeRefusal() error {
	if p.Unrecorded {
		return fmt.Errorf("this database has reliant's tables but no migration history (no goose_db_version, no schema_migrations), " +
			"so it is not a schema any release built: building the baseline here would fail on its first CREATE TABLE. NOTHING WAS APPLIED. " +
			"Restore goose_db_version with the database, or rebuild the database")
	}
	g := p.Goose
	if g == nil || (len(g.Unexpected) == 0 && len(g.RolledBack) == 0) {
		return nil
	}
	msg := fmt.Sprintf("this database's goose history (goose_db_version) is not part of the history %s replaces, so it cannot be caught up and adopted", p.Baseline.Name)
	if len(g.Unexpected) > 0 {
		msg += fmt.Sprintf("; it HAS applied %d goose version(s) the frozen history does not hold: %s — a branch's migration applied to a shared database, "+
			"or a goose release this binary does not know (reconcile the schema by hand, then delete those goose_db_version rows; or rebuild the database)",
			len(g.Unexpected), formatVersions(g.Unexpected))
	}
	if len(g.RolledBack) > 0 {
		msg += fmt.Sprintf("; its latest goose_db_version row for version(s) %s records a ROLLBACK: goose counts any row as applied and would not re-run them, "+
			"so the schema cannot be brought to goose's head (reconcile those versions by hand)", formatVersions(g.RolledBack))
	}
	return fmt.Errorf("%s. NOTHING WAS APPLIED", msg)
}

// Latest is the newest migration this binary embeds.
func (p MigrationPlan) Latest() int64 { return p.Embedded[len(p.Embedded)-1] }

// Verdict is what `reliant db migrate up` will do, in one line, and whether it
// will refuse.
func (p MigrationPlan) Verdict() (verdict string, refuses bool) {
	later := len(p.Embedded) - 1 // forge migrations after the baseline
	switch {
	case p.State.Dirty:
		return fmt.Sprintf("refuse: the schema is dirty at version %d (a migration failed partway); repair it by hand, then `reliant db migrate force %d`",
			p.State.Version, p.State.Version), true
	case p.bridgeRefusal() != nil:
		return "refuse: " + p.bridgeRefusal().Error(), true
	case p.State.Applied && int64(p.State.Version) > p.Latest():
		return fmt.Sprintf("schema is AHEAD of this binary (version %d; this binary's newest is %d): apply nothing, and start only if every newer version "+
			"was declared backward-compatible (migratekit reads schema_migrations_compat); refuse otherwise", p.State.Version, p.Latest()), false
	case p.State.Applied && len(p.Pending) == 0:
		return fmt.Sprintf("up to date (version %d)", p.State.Version), false
	case p.State.Applied:
		return fmt.Sprintf("apply %d pending migration(s): %s", len(p.Pending), formatVersions(p.Pending)), false
	case p.needsGooseCatchUp():
		return fmt.Sprintf("goose catch-up %d, then adopt: run %d goose migration(s) the database is missing (%s) with goose, then adopt %s without running its SQL, then apply %d later migration(s)",
			len(p.Goose.Missing), len(p.Goose.Missing), formatVersions(p.Goose.Missing), p.Baseline.Name, later), false
	case p.Goose != nil:
		return fmt.Sprintf("adopt: goose_db_version holds exactly the %d versions %s replaces; record it as applied without running its SQL, then apply %d later migration(s)",
			len(p.Goose.Declared), p.Baseline.Name, later), false
	default:
		return fmt.Sprintf("fresh database: apply %s and %d later migration(s)", p.Baseline.Name, later), false
	}
}
