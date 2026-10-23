package migratekit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path"

	"github.com/reliant-labs/forge/pkg/dbgrants"
)

// THE READ-ONLY GRANTS ARE A POST-STEP OF EVERY RUN.
//
// A project's `forge generate` projects the applied schema and the proto
// `forge:secret` markers into db/grants/readonly_gen.sql (pkg/dbgrants), and
// the project embeds it beside its migrations. After a run applies the
// migrations, the same session — still under the run lock — applies that file,
// so the read-only role's privileges are converged by every migration:
// a new table is granted, a new column is granted, and a column marked secret
// stays out.
//
// IT RUNS WHETHER OR NOT ANYTHING WAS PENDING. The role may not have existed
// when the last migration ran (CloudNativePG creates a managed role when the
// Cluster is reconciled, which can be after the first migration Job), and
// the script is idempotent, so a no-op run is the cheapest place to catch up.
//
// IT DOES NOT RUN WHEN THE SCHEMA IS AHEAD OF THIS BINARY. The script is this
// binary's view of the schema: an older release would REVOKE the access to a
// table only the newer release knows, and the newer release's own run
// converges the grants anyway.
//
// A FAILURE IS THE RUN'S FAILURE. The migrations are committed by then, but a
// column that has become secret keeps its old grant until the script succeeds,
// so the run reports it rather than serving on. A rerun finds nothing pending
// and applies the grants again.

// grantsRole is the role the run checks for before applying the grants. It is
// dbgrants.RoleName; a variable only so tests that share a Postgres server (and
// so a role namespace) can each use a role of their own.
var grantsRole = dbgrants.RoleName

// GrantsFile is the grants script's path relative to the directory that holds
// the migrations' directory — "grants/readonly_gen.sql" beside
// "migrations/", which is where a project's embed puts it.
const GrantsFile = "grants/readonly_gen.sql"

// Grants reports what a run did with the read-only grants. Nil on a Result
// means the project embeds none.
type Grants struct {
	// Applied is true when the script ran.
	Applied bool
	// RoleMissing is true when the database has no [dbgrants.RoleName], so
	// the script was not run. That is the normal state of a database nobody
	// provisioned the role for, not a fault.
	RoleMissing bool
}

// grantsScript reads the grants script that sits beside the migrations'
// directory dir in fsys. ok is false when the project embeds none.
func grantsScript(fsys fs.FS, dir string) (script string, ok bool, err error) {
	if fsys == nil {
		return "", false, nil
	}
	p := path.Join(path.Dir(dir), GrantsFile)
	b, err := fs.ReadFile(fsys, p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read embedded read-only grants (%s): %w", p, err)
	}
	return string(b), true, nil
}

// applyGrants applies the run's grants script on conn, or reports nil when the
// project has none.
func applyGrants(ctx context.Context, conn *sql.Conn, script string) (*Grants, error) {
	if script == "" {
		return nil, nil
	}
	exists, err := dbgrants.RoleExists(ctx, conn, grantsRole)
	if err != nil {
		return nil, err
	}
	if !exists {
		return &Grants{RoleMissing: true}, nil
	}
	if err := dbgrants.Apply(ctx, conn, script); err != nil {
		return nil, err
	}
	return &Grants{Applied: true}, nil
}
