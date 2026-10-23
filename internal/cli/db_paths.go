// File: internal/cli/db_paths.go
//
// Where `forge db ...` reads a project's files from.
//
// Every project-relative input of the db commands — the migrations
// directory, db/seeds/vocab.yaml, db/seeds/custom/, the per-env KCL config —
// resolves against ONE directory: the project root that -C names (or the
// project the CWD sits in). It used to be the CWD for some of them and the
// project for others. `--dir` defaulted to the RELATIVE "db/migrations",
// computed when the command tree was BUILT — before -C had even been parsed —
// and golang-migrate and the seeder then opened it against the process CWD.
// So from /tmp:
//
//	forge db migrate up -C ~/src/app   → open /tmp/db/migrations/.: no such file
//	forge db seed apply -C ~/src/app   → "Seeded 0 row(s) across 0 table(s)"
//
// while the dev gate and the DSN lookup, which already honoured -C, read the
// right project. Half a command looking at one project and half at another is
// worse than either alone: the second line reported success.
//
// The rule now: a relative path a db command reads is relative to the project
// root, whether it came from forge.yaml or from a flag. An absolute path is
// used as given.

package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
)

const defaultMigrationsDir = "db/migrations"

// migrationsDirFlagUsage is the --dir help shared by every db subcommand, so
// the resolution rule is stated once and reads the same everywhere.
const migrationsDirFlagUsage = "Migrations directory (default: forge.yaml database.migrations_dir, else db/migrations; a relative path resolves against the project root, not the CWD)"

// dbProjectRoot is the directory db commands resolve project-relative paths
// against: the project -C names (or the project enclosing the CWD), else the
// resolution root itself when there is no forge.yaml to find.
func dbProjectRoot() string {
	if cfgPath, err := findProjectConfigFile(); err == nil {
		return filepath.Dir(cfgPath)
	}
	if root, err := cmdutil.ResolutionRoot(); err == nil {
		return root
	}
	return "."
}

// dbProjectPath anchors a project-relative path at the db commands' project
// root. An absolute path is returned cleaned and otherwise untouched.
func dbProjectPath(p string) string {
	return filepath.Clean(resolveProjectPath(dbProjectRoot(), p))
}

// resolveMigrationsDir returns the ABSOLUTE migrations directory a db command
// acts on: --dir when given, else forge.yaml's database.migrations_dir, else
// db/migrations — each resolved against the project root. It is called from
// RunE, after -C is installed; resolving it as a flag default (as it once
// was) runs before -C exists.
func resolveMigrationsDir(flagDir string) string {
	dir := flagDir
	if dir == "" {
		dir = defaultMigrationsDir
		if store, err := loadProjectStore(); err == nil && store.Database().MigrationsDir != "" {
			dir = store.Database().MigrationsDir
		}
	}
	return dbProjectPath(dir)
}

// requireMigrationsDir resolves the migrations directory and refuses when it
// does not exist. A missing directory is never "no migrations": it is the
// wrong directory, and every command that reads it would otherwise act on an
// empty schema and report success.
func requireMigrationsDir(flagDir string) (string, error) {
	dir := resolveMigrationsDir(flagDir)
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("migrations directory %s does not exist (project root %s) — pass -C <project> or --dir", dir, dbProjectRoot())
		}
		return "", fmt.Errorf("migrations directory %s: %w", dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("migrations directory %s is not a directory", dir)
	}
	return dir, nil
}
