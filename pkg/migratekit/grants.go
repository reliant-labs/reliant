package migratekit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"

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

// GrantsRowsDir holds the script's per-table rows, relative to the same
// directory as [GrantsFile]: one file per table (pkg/dbgrants.Files), spliced
// into the script in file-name order.
const GrantsRowsDir = "grants/readonly"

// Grants reports what a run did with the read-only grants. Nil on a Result
// means the project embeds none.
type Grants struct {
	// Applied is true when the script ran.
	Applied bool
	// RoleMissing is true when the database has no [dbgrants.RoleName], so
	// the script was not run. That is the normal state of a database nobody
	// provisioned the role for, not a fault.
	RoleMissing bool
	// SecurityDefiner lists the SECURITY DEFINER functions the role can
	// execute (dbgrants.SecurityDefinerExposure): a way around its column
	// grants. A warning for the caller to log, never a failure.
	SecurityDefiner []string
}

// grantsScript reads the read-only grants script that sits beside the
// migrations' directory dir in fsys, with its per-table rows spliced in. ""
// when the project embeds none.
func grantsScript(fsys fs.FS, dir string) (string, error) {
	if fsys == nil {
		return "", nil
	}
	p := path.Join(path.Dir(dir), GrantsFile)
	b, err := fs.ReadFile(fsys, p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read embedded grants (%s): %w", p, err)
	}
	// fs.Glob returns the names sorted: the order dbgrants.Render uses.
	names, err := fs.Glob(fsys, path.Join(path.Dir(dir), GrantsRowsDir, "*_gen.sql"))
	if err != nil {
		return "", fmt.Errorf("list embedded grants rows (%s): %w", GrantsRowsDir, err)
	}
	rows := make([]string, 0, len(names))
	for _, name := range names {
		row, err := fs.ReadFile(fsys, name)
		if err != nil {
			return "", fmt.Errorf("read embedded grants row (%s): %w", name, err)
		}
		rows = append(rows, string(row))
	}
	script, err := dbgrants.Assemble(string(b), rows)
	if err != nil {
		return "", fmt.Errorf("embedded grants (%s): %w", p, err)
	}
	return script, nil
}

// applyGrants applies the run's read-only grants script on conn, or reports
// nil when the project has none. A failed script fails the run.
//
// REVOKE and GRANT take locks, so the script runs under the run's limits
// (guardedStatement): it waits behind a long reader for lock_timeout, is
// retried, and fails the run with a *LockTimeoutError if the reader outlasts
// the attempts. The role lookup takes no conflicting lock and stays outside. A
// script added to a run must go through guardedStatement too.
func applyGrants(ctx context.Context, conn *sql.Conn, script string, run RunOptions) (*Grants, error) {
	if script == "" {
		return nil, nil
	}
	g := &Grants{}
	exists, err := dbgrants.RoleExists(ctx, conn, grantsRole)
	if err != nil {
		return nil, err
	}
	if !exists {
		g.RoleMissing = true
		return g, nil
	}
	if err := guardedStatement(ctx, conn, run, "read-only grants", func() error {
		return dbgrants.Apply(ctx, conn, script)
	}); err != nil {
		return nil, err
	}
	g.Applied = true
	// Read after the grants, from the catalog, on the same session: a function
	// created by this very run is seen. Informational, so a failed lookup does
	// not fail a run whose grants succeeded.
	if fns, err := dbgrants.SecurityDefinerExposure(ctx, conn, grantsRole); err == nil {
		g.SecurityDefiner = fns
	}
	return g, nil
}

func uniqueSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	sort.Strings(in)
	out := in[:1]
	for _, v := range in[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}
