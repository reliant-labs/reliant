// File: internal/codegen/unversionable_entities.go
//
// UNVERSIONABLE ENTITY — a `forge:version` column with no matching field on
// the entity's wire message is provably broken, and broken in a way that
// looks like data corruption rather than a misconfiguration.
//
// Optimistic concurrency is a compare-and-swap against the version the
// CALLER last read: the generated Update carries entity.Version into
// `WHERE version = ?` and increments it in the same statement (see
// forge/pkg/crud's Repo.applyVersionGuard). That predicate only means
// anything if the caller can actually round-trip the value — read it on a
// Get, hand it back on the Update.
//
// With no wire field there is nothing to round-trip. `<entity>FromProto`
// builds the entity from the request, the absent field leaves Version at
// the Go zero, and every Update runs `WHERE version = 0`. The first write
// against a fresh row succeeds and moves the stored version to 1; every
// write after that matches no row and fails Aborted — permanently, for that
// row, with a message telling the caller to re-read and retry when
// re-reading cannot help. An entity in this state is not racy, it is
// simply un-updatable after its first edit.
//
// Both facts are known at generate time from the joined wire+schema
// EntityDef, so this fires for every project that declares the marker,
// which is the point: the marker's whole promise is that a lost update
// becomes a loud error, and the failure mode without the wire field is a
// loud error on writes that never raced at all.
package codegen

import (
	"errors"
	"fmt"
	"strings"
)

// UnversionableEntity is one entity whose `forge:version` column has no
// paired field on the wire message, so no caller can present the version
// the concurrency check requires.
type UnversionableEntity struct {
	Entity string // "WorkOrder"
	Table  string // "work_orders"
	Column string // "version"
}

// FindUnversionableEntities reports the entity's version column when the
// wire message carries no field of the same name.
//
// Returns at most one finding: a second `forge:version` column on one table
// is a different defect (the repo binds the first it finds) and is not this
// check's business.
func FindUnversionableEntities(entity EntityDef) []UnversionableEntity {
	var versionCol string
	for _, col := range entity.Columns {
		if col.Version {
			versionCol = col.Name
			break
		}
	}
	if versionCol == "" {
		return nil
	}
	for _, f := range entity.Fields {
		if f.Name == versionCol {
			// Present on the wire. Whether it is ALSO forge:read-only is the
			// author's call: read-only is the right shape (the repo owns the
			// increment) but a plain field still round-trips correctly,
			// because the repo overwrites whatever the client proposed.
			return nil
		}
	}
	return []UnversionableEntity{{
		Entity: entity.Name,
		Table:  entity.TableName,
		Column: versionCol,
	}}
}

// UnversionableEntitiesError renders findings as the error `forge generate`
// fails with. nil for an empty set.
//
// This fails the build rather than warning, for the same reason
// UnsatisfiableColumnsError does: the generated code would compile and pass
// a smoke test, and only wedge on the SECOND update of a row — long after
// anyone would connect it to the migration that added the marker.
func UnversionableEntitiesError(entities []UnversionableEntity) error {
	if len(entities) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d entity(s) declare a forge:version column that no caller can present:\n", len(entities))
	for _, e := range entities {
		fmt.Fprintf(&b, "  - %s.%s (entity %s): the column opts %s into optimistic concurrency, "+
			"but message %s has no `%s` field — so every Update sends the zero value, "+
			"and every update after a row's first one fails Aborted\n",
			e.Table, e.Column, e.Entity, e.Entity, e.Entity, e.Column)
	}
	b.WriteString("Fix: declare the field on the entity message so the caller can round-trip what it read —\n")
	b.WriteString("  int64 version = <next>;  // forge:read-only\n")
	b.WriteString("The marker keeps it off the Create/Update request shapes (the repo owns the increment); " +
		"it still travels on Get/List responses and on the entity the Update carries, which is what the " +
		"concurrency check compares against. Or drop the COMMENT ON COLUMN to go back to last-writer-wins.")
	return errors.New(b.String())
}
