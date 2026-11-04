package database

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/migratekit/compat"
)

// compatHeader is the compatibility declaration `forge db migration new`
// writes at the top of a new migration: the one the author passed
// (--backward-compatible / --breaks-rollback), or, in a project that requires
// one and got neither, the two choices as commented-out examples. The examples
// are not directives (`--   -- forge:…` is a comment ABOUT one), so
// `forge lint --migration-safety` keeps failing the file until the author
// picks one and says why.
func compatHeader(opts MigrationOptions) (string, error) {
	compatible := oneLine(opts.BackwardCompatible)
	breaks := oneLine(opts.BreaksRollback)
	switch {
	case compatible != "" && breaks != "":
		return "", fmt.Errorf("a migration is backward-compatible or breaks rollback, not both: pass one of --backward-compatible and --breaks-rollback")
	case compatible != "":
		return compat.Directive + " — " + compatible + "\n", nil
	case breaks != "":
		return compat.BreaksRollbackDirective + " " + breaks + "\n", nil
	case opts.RequireCompatDeclaration:
		return "-- This project requires every migration to declare whether the previous release keeps working on\n" +
			"-- the schema it leaves (database.migration_safety.require_compat_declaration). Replace this block with ONE of:\n" +
			"--   " + compat.Directive + " — <why the previous release keeps working on this schema>\n" +
			"--   " + compat.BreaksRollbackDirective + " <why, deliberately, it does not>\n\n", nil
	}
	return "", nil
}

// oneLine keeps a reason on the directive's line.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
