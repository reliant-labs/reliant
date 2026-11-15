package migratekit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/golang-migrate/migrate/v4"

	"github.com/reliant-labs/forge/pkg/migratekit/compat"
)

// adoptForge adopts a database a squashed forge history built, when the
// baseline declares that history (`-- forge:adopts forge <versions>`). See
// adopt.go, "ADOPTING A SQUASHED FORGE HISTORY".
//
// Concurrency and crash safety are adopt's: the caller holds the run lock,
// the check-and-record holds appliedLockKey and re-reads the state under it,
// and the baseline's applied row is committed before Force writes the
// version. A crash between the two leaves the schema at its old version, so
// the next run is a candidate again; the row's insert is then a no-op.
func adoptForge(ctx context.Context, conn *sql.Conn, m *migrate.Migrate, source sourceSet, baseline compat.Migration, state State) (State, *Adopted, error) {
	if !forgeAdoptionCandidate(source, state) {
		return state, nil, nil
	}
	var adopted *Adopted
	err := withSessionLock(ctx, conn, appliedLockKey, func() error {
		// Another replica may have adopted (and applied more) while this
		// one waited for the lock.
		var err error
		if state, err = stateOf(m); err != nil || !forgeAdoptionCandidate(source, state) {
			return err
		}
		found, err := recordForgeAdoption(ctx, conn, source, baseline, state)
		if err != nil || !found {
			return err
		}
		if err := m.Force(int(baseline.Version)); err != nil {
			return fmt.Errorf("record %s as applied: %w", baseline.Name, err)
		}
		if state, err = stateOf(m); err != nil {
			return err
		}
		adopted = &Adopted{Tool: compat.ToolForge, Version: baseline.Version, Name: baseline.Name, Versions: len(baseline.Adopts.Versions)}
		return nil
	})
	var adoptErr *AdoptionError
	if err != nil && !errors.As(err, &adoptErr) {
		err = fmt.Errorf("adopt the forge history %s squashed: %w", baseline.Name, err)
	}
	if err != nil {
		return state, nil, err
	}
	return state, adopted, nil
}

// forgeAdoptionCandidate reports whether state could be a squashed forge
// history's: a clean schema at a version this binary does not carry. A fresh
// database applies the baseline as SQL; a dirty one meets the dirty refusal;
// one at a version this binary embeds is past adoption.
func forgeAdoptionCandidate(source sourceSet, state State) bool {
	return state.Applied && !state.Dirty && !source.has(state.Version)
}

// recordForgeAdoption records the baseline as applied when the database's
// forge history is the one it declares, and refuses with *AdoptionError when
// it is not. found is false for a database that is not this squash's to
// adopt: one that shares migrations with this binary is a schema AHEAD of it
// (classifyAhead's verdict), not a squashed history. The transaction is
// committed before it returns, so the Force that follows runs on a session
// with none open.
func recordForgeAdoption(ctx context.Context, conn *sql.Conn, source sourceSet, baseline compat.Migration, state State) (found bool, err error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	exists, records, err := readAppliedRecord(ctx, tx)
	if err != nil {
		return false, err
	}
	declared := make(map[int64]bool, len(baseline.Adopts.Versions))
	for _, v := range baseline.Adopts.Versions {
		declared[v] = true
	}
	adoptionErr := func() *AdoptionError {
		return &AdoptionError{Tool: compat.ToolForge, Table: appliedTable, Baseline: baseline.Name, Declared: len(baseline.Adopts.Versions)}
	}
	if !declared[int64(state.Version)] {
		if exists && sharesHistory(records, source) {
			return false, nil
		}
		e := adoptionErr()
		e.Unexpected = []int64{int64(state.Version)}
		return true, e
	}

	// What the database applied. Before the applied record existed, only the
	// high-water mark speaks, and golang-migrate never applies below it.
	applied := map[int64]bool{}
	for v := range records {
		applied[int64(v)] = true
	}
	if !exists {
		for _, v := range baseline.Adopts.Versions {
			if v <= int64(state.Version) {
				applied[v] = true
			}
		}
	}
	if missing, _ := diffAdoption(baseline.Adopts.Versions, applied); len(missing) > 0 {
		e := adoptionErr()
		e.Missing = missing
		return true, e
	}

	stmts := []string{"CREATE TABLE IF NOT EXISTS " + appliedTable + ` (
			version BIGINT PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`}
	if !exists {
		// The record bootstrapApplied would have written, so the release
		// before the squash still finds its versions applied.
		var values []string
		for _, v := range baseline.Adopts.Versions {
			if v <= int64(state.Version) {
				values = append(values, fmt.Sprintf("(%d, '')", v))
			}
		}
		stmts = append(stmts, "INSERT INTO "+appliedTable+" (version, name) VALUES "+strings.Join(values, ", ")+" ON CONFLICT (version) DO NOTHING")
	}
	stmts = append(stmts, fmt.Sprintf("INSERT INTO %s (version, name) VALUES (%d, %s) ON CONFLICT (version) DO NOTHING",
		appliedTable, baseline.Version, quoteLiteral(baseline.Descriptor)))
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return true, err
		}
	}
	return true, tx.Commit()
}

// sharesHistory reports whether the database recorded any migration this
// binary embeds as applied.
func sharesHistory(records map[uint]string, source sourceSet) bool {
	for _, m := range source {
		if _, ok := records[m.Version]; ok {
			return true
		}
	}
	return false
}

// ForeignHistoryError is a database built by migrations this binary does not
// carry: its schema is at a version this binary does not embed, and it
// recorded none of this binary's migrations as applied. Nothing was applied.
//
// It is what a database the pre-squash history built meets when the squashed
// baseline does not declare that history. It used to surface as whichever
// check ran first, and both misled: "AHEAD of the newest migration this
// binary embeds" for a baseline numbered above the history, and "MISSING"
// (re-version the baseline) for one numbered below it.
type ForeignHistoryError struct {
	// Version is the schema version recorded in the database.
	Version uint
	// First and Latest name the migrations this binary embeds.
	First  string
	Latest uint
	// Embedded is how many migrations this binary embeds.
	Embedded int
}

func (e *ForeignHistoryError) Error() string {
	return fmt.Sprintf("database schema is at version %d, but the database has applied none of the %d migration(s) this binary embeds "+
		"(%s through %d): it was built by a migration history this binary does not carry — most often one that `forge db squash` "+
		"replaced. NOTHING WAS APPLIED. If %s squashed that history, declare the versions it replaces on their own lines, "+
		"`%s %s <version> ...` (forge db squash writes them), and this binary adopts the database without running anything. "+
		"Otherwise this release is not the one for this database: run the release whose migrations built it. "+
		"See the forge skill db/deploy-migrations, \"Squashing a forge history\"",
		e.Version, e.Embedded, e.First, e.Latest, e.First, compat.AdoptsDirective, compat.ToolForge)
}

// refuseForeignHistory returns a *ForeignHistoryError for a database built by
// a history this binary does not carry (see the type). It runs after adopt,
// so a squash that declares the history has already adopted the database.
// A database that predates the applied record has no record to judge by and
// is left to the checks after it.
func refuseForeignHistory(ctx context.Context, conn *sql.Conn, source sourceSet, state State) error {
	if len(source) == 0 || !state.Applied || state.Dirty || source.has(state.Version) {
		return nil
	}
	exists, records, err := readAppliedRecord(ctx, conn)
	if err != nil || !exists || sharesHistory(records, source) {
		return err
	}
	return &ForeignHistoryError{Version: state.Version, First: source[0].Name, Latest: source.latest(), Embedded: len(source)}
}
