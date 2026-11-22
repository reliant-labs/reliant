package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/reliant-labs/forge/internal/closedset"
	"github.com/reliant-labs/forge/internal/database"
	"github.com/reliant-labs/forge/internal/sqlident"
)

// newClosedSetMigration writes the fix `forge lint --closed-sets` prints for
// the forge:closed-set CHECK named constraint, as a new migration in dir —
// two for a widening, which adds the CHECK NOT VALID and validates it in
// <name>_validate, outside the first one's ACCESS EXCLUSIVE lock.
func newClosedSetMigration(ctx context.Context, constraint, name, dir, dsn string, withContext bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	findings, err := closedset.Analyze(dir, closedset.NewResolver(cwd))
	if err != nil {
		return err
	}
	var match []closedset.Finding
	for _, f := range findings {
		if sqlident.Last(f.Constraint) == sqlident.Last(constraint) {
			match = append(match, f)
		}
	}
	switch {
	case len(match) == 0:
		return noClosedSetFinding(constraint, dir, findings)
	case len(match) > 1:
		var tables []string
		for _, f := range match {
			tables = append(tables, f.Table)
		}
		return fmt.Errorf("constraint %s is out of step on %d tables (%s); fix them one migration each from the SQL `forge lint --closed-sets` prints", constraint, len(match), strings.Join(tables, ", "))
	case match[0].Fix == nil:
		return fmt.Errorf("forge cannot write the fix for %s on %s: %s", match[0].Constraint, match[0].Table, match[0].Problem)
	}
	fix := match[0].Fix
	opts := &database.MigrationOptions{DSN: dsn, Context: withContext, Body: fix.SQL}
	if fix.BackwardCompatible {
		opts.BackwardCompatible = fix.Reason
	} else {
		opts.BreaksRollback = fix.Reason
		fmt.Fprintf(os.Stderr, "⚠️  this fix NARROWS %s (drops %s): rows holding those values fail it — rewrite or delete them first, in the same migration\n",
			match[0].Constraint, strings.Join(match[0].Extra, ", "))
	}
	if err := database.CreateMigration(ctx, name, dir, opts); err != nil {
		return err
	}
	if v := fix.Validate; v != nil {
		// Allocated after the first, so it sorts (and applies) after it.
		return database.CreateMigration(ctx, name+"_validate", dir, &database.MigrationOptions{
			DSN: dsn, Context: withContext, Body: v.SQL, BackwardCompatible: v.Reason,
		})
	}
	return nil
}

// noClosedSetFinding is the refusal for a constraint with nothing to fix. It
// names the constraints that ARE out of step, so a near-miss name
// (access_tokens_scopes for ck_cp_access_tokens_scopes) reads as a typo
// rather than as "this CHECK is fine".
func noClosedSetFinding(constraint, dir string, findings []closedset.Finding) error {
	if len(findings) == 0 {
		return fmt.Errorf("constraint %s has no closed-set finding in %s, and no forge:closed-set CHECK there is out of step: there is nothing to write", constraint, dir)
	}
	var names []string
	for _, f := range findings {
		names = append(names, fmt.Sprintf("%s (on %s)", f.Constraint, f.Table))
	}
	return fmt.Errorf("constraint %s has no closed-set finding in %s: it is not one of the out-of-step forge:closed-set CHECKs, which are %s. Name one of those exactly; a CHECK not listed already holds its set or carries no forge:closed-set declaration (`forge lint --closed-sets` prints each with its fix)",
		constraint, dir, strings.Join(names, ", "))
}
