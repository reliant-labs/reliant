// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/pkg/migratekit"
	"github.com/reliant-labs/forge/pkg/migratekit/compat"
)

// querier is what PlanMigration reads through: a *sql.DB, a *sql.Conn, or the
// read-only *sql.Tx `reliant db migrate status` runs it in.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// MigrationPlan is what Migrate will do to a database, read without changing
// anything. Migrate refuses from it, and `reliant db migrate status` prints
// it, so the preview and the run cannot disagree about what is refused. The
// rest is migratekit's to decide at run time; the plan reports the facts it
// decides from.
type MigrationPlan struct {
	// State is what forge's migrator recorded in schema_migrations.
	State migratekit.State
	// Baseline is the first embedded migration, which builds a fresh
	// database.
	Baseline compat.Migration
	// Embedded is every forge migration version this binary embeds,
	// ascending.
	Embedded []int64
	// Pending is every Embedded version State has not applied.
	Pending []int64
	// Unrecorded is true when forge's migrator has recorded nothing, yet
	// reliant's tables exist: building the baseline there would fail on its
	// first CREATE TABLE.
	Unrecorded bool
}

// gooseBridgeRelease is the release a database from before the forge
// migrations cutover upgrades through: the first tagged release carrying the
// goose bridge, which brought a database goose migrated to goose's frozen head
// and adopted it. Every release up to the goose removal carries it too.
const gooseBridgeRelease = "v1.8.2"

// PlanMigration reads the database's migration state and sets it against what
// this binary embeds. It only reads.
func PlanMigration(ctx context.Context, q querier) (MigrationPlan, error) {
	ms, err := embeddedMigrations()
	if err != nil {
		return MigrationPlan{}, err
	}

	p := MigrationPlan{Baseline: ms[0]}
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
	if err := q.QueryRowContext(ctx, `SELECT to_regclass('chats') IS NOT NULL OR to_regclass('projects') IS NOT NULL`).Scan(&p.Unrecorded); err != nil {
		return MigrationPlan{}, fmt.Errorf("look for reliant's tables: %w", err)
	}
	return p, nil
}

// refusal is Migrate's own refusal, made before the migrator runs: nil when
// the database is fresh or forge's migrator has a record. Everything else is
// migratekit's to refuse.
func (p MigrationPlan) refusal() error {
	if !p.Unrecorded {
		return nil
	}
	return fmt.Errorf("this database has reliant's tables but no migration record (no schema_migrations), "+
		"so building the baseline here would fail on its first CREATE TABLE. NOTHING WAS APPLIED. "+
		"A database from before the forge migrations cutover that no release since has migrated is upgraded by running reliant %s "+
		"(`reliant db migrate up`) once, then this release; any other such database is not a schema a release built: restore its history, or rebuild it",
		gooseBridgeRelease)
}

// Latest is the newest migration this binary embeds.
func (p MigrationPlan) Latest() int64 { return p.Embedded[len(p.Embedded)-1] }

// Verdict is what `reliant db migrate up` will do, in one line, and whether it
// will refuse.
func (p MigrationPlan) Verdict() (verdict string, refuses bool) {
	switch {
	case p.State.Dirty:
		return fmt.Sprintf("refuse: the schema is dirty at version %d (a migration failed partway); repair it by hand, then `reliant db migrate force %d`",
			p.State.Version, p.State.Version), true
	case p.refusal() != nil:
		return "refuse: " + p.refusal().Error(), true
	case p.State.Applied && int64(p.State.Version) > p.Latest():
		return fmt.Sprintf("schema is AHEAD of this binary (version %d; this binary's newest is %d): apply nothing, and start only if every newer version "+
			"was declared backward-compatible (migratekit reads schema_migrations_compat); refuse otherwise", p.State.Version, p.Latest()), false
	case p.State.Applied && len(p.Pending) == 0:
		return fmt.Sprintf("up to date (version %d)", p.State.Version), false
	case p.State.Applied:
		return fmt.Sprintf("apply %d pending migration(s): %s", len(p.Pending), formatVersions(p.Pending)), false
	default:
		return fmt.Sprintf("fresh database: apply %s and %d later migration(s)", p.Baseline.Name, len(p.Embedded)-1), false
	}
}

// formatVersions renders versions for a message, eliding the middle of a long
// list.
func formatVersions(vs []int64) string {
	const edge = 3
	join := func(xs []int64) string {
		parts := make([]string, len(xs))
		for i, v := range xs {
			parts[i] = strconv.FormatInt(v, 10)
		}
		return strings.Join(parts, ", ")
	}
	if len(vs) <= 2*edge {
		return join(vs)
	}
	return fmt.Sprintf("%s, … %d more …, %s", join(vs[:edge]), len(vs)-2*edge, join(vs[len(vs)-edge:]))
}
