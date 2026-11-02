package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/database"
	"github.com/reliant-labs/forge/internal/migrationver"
	"github.com/reliant-labs/forge/pkg/migratekit"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// resolveDSN returns the explicit --dsn flag value, falling back to the
// DATABASE_URL environment variable. The flag wins so users can override the
// env var ad-hoc. Returns an error if neither is set so the caller can surface
// a helpful message.
func resolveDSN(flagDSN string) (string, error) {
	if flagDSN != "" {
		return flagDSN, nil
	}
	if env := os.Getenv("DATABASE_URL"); env != "" {
		return env, nil
	}
	return "", fmt.Errorf("database connection string required: pass --dsn or set DATABASE_URL")
}

func newDBCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Database and migration commands",
		Long: `Manage database migrations.

Forge uses a migration-first database model:
- Checked-in SQL migrations in db/migrations/ are the source of truth
- forge's migrator (pkg/migratekit, on golang-migrate) applies them, from
  'forge db migrate' and from the project's own binary alike
- Entity types are projections of the applied schema (forge generate)

To read an environment's data: 'forge db query <env> "<sql>"' runs one
read-only statement and prints the rows.

When something is wedged:

  A migration failed part-way and the state is marked dirty
    No further migration will run until the flag clears, and 'migrate up'
    refuses too. Repair the schema by hand, then:
      forge db migrate force <version>   # record it as applied, runs no SQL
      forge db migrate up
    Or, on a scratch dev database, skip the repair entirely:
      forge db reset                     # DROP, recreate, migrate, seed (dev-only)

  A migration cannot apply because existing rows violate it
    Adding a constraint to a column seeding filled with placeholders wedges
    both repairs against each other: 'seed reset' refuses because the schema
    is behind, 'migrate up' refuses because of the rows. Discard the state:
      forge db reset                     # needs no dirty-state reasoning (dev-only)

  The dev database is full of bad or stale rows
    Do not drop the database by hand:
      forge db seed reset                # delete seeded rows and re-seed (dev-only)
      forge db reset                     # or rebuild the whole database (dev-only)

  Seeded rows are rejected by their own schema
    'forge db seed apply' names the constraint it could not place, and why.
    Load the db/seeding skill for the constraint shapes forge can seed.`,
	}

	cmd.AddCommand(newDBMigrationCommand())
	cmd.AddCommand(newDBMigrateCommand())
	cmd.AddCommand(newDBIntrospectCommand())
	cmd.AddCommand(newDBSquashCommand())
	cmd.AddCommand(newDBSeedCommand())
	cmd.AddCommand(newDBResetCommand())
	cmd.AddCommand(newDBQueryCommand())

	// Every db subcommand reads its project's files from the -C project
	// root, never the CWD — see db_paths.go.
	cmd.Long += `

Project files (db/migrations, db/seeds/vocab.yaml, db/seeds/custom/) are read
from the project root: the one -C names, or the one the current directory is
in. A relative --dir resolves against that root too.`

	return cmdutil.StrictGroup(cmd)
}

// newDBSquashCommand creates the `forge db squash` subcommand. Squash spins
// up an ephemeral Postgres in docker, applies every migration in --from-dir
// against it with migratekit (runMigrateUp, the `forge db migrate up` path),
// dumps the resulting schema + INSERT-shaped seed data via `pg_dump`, keeps
// only the statements that build the database
// (squashBaseline drops pg_dump's session prelude and psql meta-commands), and
// writes a fresh `<baseline>.up.sql` (forward only — forge writes no down migrations). The use case is the
// "I have N golang-migrate files and want one canonical baseline" workflow
// — common when pulling a long-lived schema into a new project, or when
// collapsing a grown migration history into a checkpoint.
//
// The command is intentionally self-contained: it brings up its own
// container so the developer doesn't need a local Postgres; it tears the
// container down on success or failure; and it never touches existing
// migration files in --from-dir. The output filenames are derived from
// --to (default `00001_baseline`) and land alongside (or in --out-dir).
func newDBSquashCommand() *cobra.Command {
	var (
		fromDir       string
		fromDSN       string
		excludeTables []string
		baseline      string
		outDir        string
		image         string
		dbName        string
		dbUser        string
		dbPass        string
	)

	cmd := &cobra.Command{
		Use:   "squash",
		Short: "Collapse N migrations into one canonical baseline (.up.sql)",
		Long: `Squash applies every migration in --from-dir against an ephemeral Postgres
container, dumps the resulting schema + seed data with pg_dump, and writes
a single forward-only baseline migration. With --from-dsn it dumps an existing
database instead: see "Squashing a database" below.

Output:
  <out-dir>/<baseline>.up.sql    (CREATE statements + INSERTs for non-schema_migrations rows)

No .down.sql is written: forge rolls forward only.

The baseline holds only statements that build the database. pg_dump's session
prelude (the SET statements and set_config('search_path', '', false)) and its
psql meta-commands (\restrict, \unrestrict) are dropped: a migrator runs every
migration of a run on one session, so an emptied search_path would break the
next migration and leak into a server's pool. Function bodies, comments and
inserted values are kept byte for byte.

The baseline is reproducible: its header names the squashed migrations (how
many, first and last version), never a time or a path, so squashing the same
history with the same --image writes the same bytes.

The header declares the history it replaces ("-- forge:adopts forge <every
squashed version>") and that the baseline is backward-compatible. So a
database those migrations built ADOPTS the baseline on its next migrate up or
AutoMigrate: the migrator checks it applied every listed version, records the
baseline as applied, runs none of its SQL, and applies what follows. Move the
squashed files out of the directory, and give the baseline a version ABOVE
them (--to <version>_baseline) so the release before the squash can still roll
back onto an adopted database; a --to inside the history is refused.

This is the canonical "N migrations → one baseline" workflow used when
pulling a long-lived schema into a new project, or when collapsing
historical migrations into a checkpoint. Paths resolve against the project
root (-C, or the project the current directory is in).

Requires:
  - docker (for the ephemeral postgres; pg_dump runs inside it, so its major
    version matches the server's)

The migrations are applied by forge's migrator, the same one
'forge db migrate up' runs.

Squashing a database (--from-dsn):
  A history forge cannot replay (goose's, or any other tool's) is squashed
  from a database it built. Point --from-dsn at a throwaway database the
  COMPLETE history built, such as goose up on an empty database: its rows
  become INSERTs in every fresh database, so the command lists them. pg_dump
  runs in a postgres:<server major>-alpine container (--image overrides).
  When the database has goose_db_version, the baseline declares every version
  goose applied there ("-- forge:adopts goose ..."), so the migrator ADOPTS a
  database goose migrated instead of re-running the history. Tables named
  like another tool's history are listed; leave one out with --exclude-table.

Examples:
  forge db squash
  forge db squash --from-dir db/migrations --to 00001_baseline
  forge db squash --to 20260506_baseline --out-dir db/baselines/
  forge db squash --from-dsn 'postgres://postgres:pw@localhost:5432/goose_built?sslmode=disable'`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if outDir != "" {
				outDir = dbProjectPath(outDir)
			}
			if fromDSN != "" {
				for _, f := range []string{"from-dir", "db-name", "db-user", "db-pass"} {
					if cmd.Flags().Changed(f) {
						return fmt.Errorf("--%s does not apply to --from-dsn, which dumps the database it names and starts no container of its own", f)
					}
				}
				if !cmd.Flags().Changed("image") {
					image = "" // matched to the server's major version
				}
				return runDBSquashFromDSN(cmd.Context(), cmd.OutOrStdout(), squashOptions{
					FromDSN:       fromDSN,
					ExcludeTables: excludeTables,
					Baseline:      baseline,
					OutDir:        outDir,
					Image:         image,
				})
			}
			if len(excludeTables) > 0 {
				return fmt.Errorf("--exclude-table applies only to --from-dsn: a replayed history holds no other tool's tables")
			}
			return runDBSquash(cmd.Context(), squashOptions{
				FromDir:  resolveMigrationsDir(fromDir),
				Baseline: baseline,
				OutDir:   outDir,
				Image:    image,
				DBName:   dbName,
				DBUser:   dbUser,
				DBPass:   dbPass,
			})
		},
	}

	cmd.Flags().StringVar(&fromDir, "from-dir", "", "Source directory holding the migrations to squash (default: the project's migrations directory; a relative path resolves against the project root)")
	cmd.Flags().StringVar(&fromDSN, "from-dsn", "", "Squash an existing database instead of replaying --from-dir: a postgres:// URL of a throwaway database the complete history built")
	cmd.Flags().StringArrayVar(&excludeTables, "exclude-table", nil, "With --from-dsn: a table to leave out of the baseline, such as another tool's history table (pg_dump pattern, e.g. public.flyway_schema_history; repeatable)")
	cmd.Flags().StringVar(&baseline, "to", "00001_baseline", "Baseline filename stem (writes <stem>.up.sql)")
	cmd.Flags().StringVar(&outDir, "out-dir", "", "Output directory for the baseline files (default: same as --from-dir, or the project's migrations directory with --from-dsn; a relative path resolves against the project root)")
	cmd.Flags().StringVar(&image, "image", "postgres:16-alpine", "Postgres docker image used for the ephemeral container (with --from-dsn, the one pg_dump runs in; default postgres:<server major>-alpine)")
	cmd.Flags().StringVar(&dbName, "db-name", "forge_squash", "Database name created inside the ephemeral container")
	cmd.Flags().StringVar(&dbUser, "db-user", "postgres", "Database user (default container superuser)")
	cmd.Flags().StringVar(&dbPass, "db-pass", "forge_squash", "Database password for the ephemeral container")

	return cmd
}

type squashOptions struct {
	FromDir string
	// FromDSN, when set, names the database to squash instead of FromDir
	// (runDBSquashFromDSN), and ExcludeTables the extra tables it leaves out.
	FromDSN       string
	ExcludeTables []string
	Baseline      string
	OutDir        string
	Image         string
	DBName        string
	DBUser        string
	DBPass        string
}

// runDBSquash drives the squash flow end-to-end: prereqs, container
// boot, migrate up, pg_dump, strip, write, teardown. The function is
// careful to tear the container down even on error paths so a failed
// squash doesn't leak a hanging container into `docker ps`.
func runDBSquash(ctx context.Context, opts squashOptions) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker not found on PATH (required for `forge db squash` ephemeral postgres)")
	}
	// pg_dump is invoked inside the ephemeral container via `docker exec`
	// rather than from the host. This removes the postgresql-client
	// install requirement and guarantees the dump tool's major version
	// matches the server's, avoiding cross-version dump-format
	// incompatibilities.

	if opts.FromDir == "" {
		return fmt.Errorf("--from-dir is required")
	}
	absFrom, err := filepath.Abs(opts.FromDir)
	if err != nil {
		return fmt.Errorf("resolve --from-dir: %w", err)
	}
	info, err := os.Stat(absFrom)
	if err != nil {
		return fmt.Errorf("--from-dir %q: %w", opts.FromDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("--from-dir %q is not a directory", opts.FromDir)
	}
	squashed, err := projectMigrations(absFrom)
	if err != nil {
		return fmt.Errorf("--from-dir %q: %w", opts.FromDir, err)
	} else if len(squashed) == 0 {
		return fmt.Errorf("--from-dir %q holds no migrations to squash", opts.FromDir)
	}
	stem := opts.Baseline
	if stem == "" {
		stem = "00001_baseline"
	}
	baselineVersion, haveVersion := migrationver.ParseVersion(stem + ".up.sql")
	if err := checkSquashBaselineVersion(stem, baselineVersion, haveVersion, squashed); err != nil {
		return err
	}

	outDir := opts.OutDir
	if outDir == "" {
		outDir = absFrom
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create --out-dir %q: %w", outDir, err)
	}

	// Container name is timestamped so a previous orphaned squash doesn't
	// collide with a fresh run, and so concurrent squashes in different
	// projects don't step on each other.
	container := fmt.Sprintf("forge-db-squash-%d", time.Now().UnixNano())

	// Allocate a host port via docker's `-p :5432` so multiple squashes
	// on the same machine don't fight for a single port. We discover the
	// allocated port via `docker port` after start.
	_, _ = fmt.Fprintf(os.Stdout, "Starting ephemeral postgres (%s)...\n", opts.Image)
	runArgs := []string{
		"run", "-d", "--rm",
		"--name", container,
		"-e", "POSTGRES_PASSWORD=" + opts.DBPass,
		"-e", "POSTGRES_USER=" + opts.DBUser,
		"-e", "POSTGRES_DB=" + opts.DBName,
		"-p", "127.0.0.1::5432",
		opts.Image,
	}
	if out, err := exec.CommandContext(ctx, "docker", runArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("docker run: %w\n%s", err, out)
	}

	// Best-effort teardown — runs on every exit path so an error during
	// migrate/pg_dump still cleans up the container. Uses Background so
	// teardown still runs after the parent ctx has been canceled.
	defer func() {
		stopCmd := exec.CommandContext(context.Background(), "docker", "stop", container)
		stopCmd.Stdout = nil
		stopCmd.Stderr = nil
		_ = stopCmd.Run()
	}()

	host, port, err := dockerPort(ctx, container, "5432/tcp")
	if err != nil {
		return fmt.Errorf("discover postgres host port: %w", err)
	}

	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		opts.DBUser, opts.DBPass, host, port, opts.DBName)

	if err := waitForPostgres(ctx, dsn, 60*time.Second); err != nil {
		return fmt.Errorf("postgres did not become ready: %w", err)
	}

	// Applied by forge's migrator, as every database the baseline replaces
	// was: its bookkeeping tables are excluded from the dump below.
	_, _ = fmt.Fprintln(os.Stdout, "Applying migrations...")
	if err := runMigrateUp(os.Stdout, dsn, absFrom); err != nil {
		return err
	}

	_, _ = fmt.Fprintln(os.Stdout, "Dumping schema + seed data via pg_dump...")
	// Run pg_dump inside the container via `docker exec` so we reuse
	// the postgres image's bundled pg_dump and don't require the host
	// to have postgresql-client installed at a matching major version.
	dumpCmd := exec.CommandContext(ctx, "docker", "exec",
		"-e", "PGPASSWORD="+opts.DBPass,
		container,
		"pg_dump",
		"--no-owner",
		// Privileges are dumped for the dashboard role's grants and
		// PUBLIC's, the only ones squashBaseline keeps (db_squash_dump.go).
		"--inserts",
		"--exclude-table=public.schema_migrations",
		// migratekit's bookkeeping: rows describing THIS database's
		// history, which a squashed baseline must not carry into another.
		"--exclude-table=public.schema_migrations_compat",
		"--exclude-table=public.schema_migrations_applied",
		"-U", opts.DBUser,
		"-d", opts.DBName,
	)
	dumpCmd.Stderr = os.Stderr
	dumpRaw, err := dumpCmd.Output()
	if err != nil {
		return fmt.Errorf("pg_dump failed: %w", err)
	}

	// pg_dump writes a psql script; a migration may carry only the
	// statements that build the database. See db_squash_dump.go.
	cleaned := squashBaseline(string(dumpRaw))

	upPath := filepath.Join(outDir, stem+".up.sql")

	// Deterministic: a re-squash of the same history writes the same bytes.
	// See db_squash_output.go.
	content := squashHeader(squashed) + cleaned
	// Read the file back as the migrator will: a declaration it cannot
	// parse, or one listing other versions, would refuse or skip adoption on
	// every database the history built.
	if err := checkWrittenForgeAdoption(content, squashed); err != nil {
		return fmt.Errorf("the baseline for %s: %w", upPath, err)
	}
	if err := atomicfile.WriteFile(upPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write baseline up: %w", err)
	}

	_, _ = fmt.Fprintf(os.Stdout, "\n  Wrote %s\n", upPath)
	printSquashNextSteps(os.Stdout, absFrom, stem+".up.sql", baselineVersion, haveVersion, squashed)

	return nil
}

// dockerPort returns the host:port that docker mapped for the given
// container's internal port spec (e.g. "5432/tcp"). Output of
// `docker port <name> <spec>` is one line of `0.0.0.0:32768` (or
// `[::]:32768`) per binding. We take the first IPv4 binding since that's
// what `127.0.0.1::5432` gives us.
func dockerPort(ctx context.Context, container, spec string) (string, string, error) {
	out, err := exec.CommandContext(ctx, "docker", "port", container, spec).Output()
	if err != nil {
		return "", "", fmt.Errorf("docker port: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		// Skip IPv6 bindings since the DSN must be IPv4-friendly for the
		// migrate / pg_dump CLIs running on the host. Any "0.0.0.0:N" or
		// "127.0.0.1:N" line is fine.
		if strings.HasPrefix(line, "[") {
			continue
		}
		if i := strings.LastIndex(line, ":"); i > 0 {
			host := line[:i]
			port := line[i+1:]
			if host == "0.0.0.0" {
				host = "127.0.0.1"
			}
			return host, port, nil
		}
	}
	return "", "", fmt.Errorf("no IPv4 binding for %s in %q", spec, string(out))
}

// waitForPostgres polls until `pg_isready` (or a fallback connect) reports
// ready, or the timeout fires. The container takes ~1-2s to accept
// connections on first boot; we poll every 500ms and bail with a useful
// error rather than letting the migrator hit a connection-refused.
func waitForPostgres(ctx context.Context, dsn string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	last := error(nil)
	for time.Now().Before(deadline) {
		db, err := database.ConnectDB(ctx, dsn)
		if err != nil {
			last = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if err := db.PingContext(ctx); err != nil {
			_ = db.Close()
			last = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		_ = db.Close()
		return nil
	}
	if last != nil {
		return fmt.Errorf("timed out after %s: %w", timeout, last)
	}
	return fmt.Errorf("timed out after %s", timeout)
}

// newDBMigrationCommand creates the migration subcommand.
func newDBMigrationCommand() *cobra.Command {
	var (
		migDir      string
		dsn         string
		withContext bool

		backwardCompatible string
		breaksRollback     string
		closedSet          string
	)

	migrationCmd := &cobra.Command{
		Use:   "migration",
		Short: "Create and re-version forward-only SQL migration files",
		Long: `Create a new forward-only SQL migration in db/migrations/, or re-version one.

Versions are 14-digit UTC timestamps (<YYYYMMDDHHMMSS>_<name>.up.sql), never
sequential numbers. A sequential allocator picks max+1 by reading the
directory, which is collision-free on one checkout and collision-prone across
branches: every branch cut from the same commit reads the same highest number
and picks the same next one. A timestamp cannot collide without two branches
allocating in the same second. Existing sequential files are never renamed —
a 14-digit timestamp sorts after any 5-digit number, so adoption costs nothing.

Commands:
  new     allocate a fresh timestamp and scaffold the .up.sql file
  rebase  re-version an existing file whose version the schema has passed

The .up.sql file opens with a short header saying where the schema lives —
` + "`forge project shapes --kind table`" + `, ` + "`forge db introspect`" + ` — rather than a copy
of it. --context writes the full context into the file instead:
  - Current schema (parsed from existing migrations, or from DB with --dsn)
  - Previous migration content
  - Migration history

Examples:
  forge db migration new add_users_table
  forge db migration new add_preferences --context
  forge db migration new add_preferences --dsn "$DATABASE_URL"
  forge db migration new "backfill account status" --dir db/migrations
  forge db migration rebase db/migrations/20260101120000_add_users.up.sql
  forge db migration rebase --all-pending`,
	}

	newCmd := &cobra.Command{
		Use:   "new [name]",
		Short: "Create a new forward-only migration with a fresh UTC timestamp version",
		Long: `Create a new forward-only migration in the migrations directory.

The file is named <YYYYMMDDHHMMSS>_<name>.up.sql, using a UTC timestamp
allocated to sort after every version already in the directory. Never
hand-type a version number: max+1 is what makes parallel branches claim the
same one.

There is no .down.sql. Forge rolls forward — a bad migration is repaired by a
new migration written against the state the database is actually in.

Declare, in the file, whether the PREVIOUS release keeps working on the
schema this migration leaves — every rolling deploy runs it there:
  --backward-compatible "<why>"  writes  -- forge:backward-compatible — <why>
  --breaks-rollback "<why>"      writes  -- forge:breaks-rollback <why>
A project with database.migration_safety.require_compat_declaration gets
both choices as a comment to replace when neither flag is given, and
` + "`forge lint --migration-safety`" + ` fails the file until it says one.

The file opens with a short header saying where the schema lives. --context
writes the reconstructed schema, the previous migration and the history into
it instead (on a large project, thousands of lines); --dsn does the same with
the schema read from a live database.

Examples:
  forge db migration new add_users_table
  forge db migration new add_preferences --context
  forge db migration new add_preferences --dsn "$DATABASE_URL"
  forge db migration new add_retired_at --backward-compatible "additive column the previous release never reads"
  forge db migration new --closed-set access_tokens_scopes

--closed-set <constraint> writes the migration ` + "`forge lint --closed-sets`" + ` prints for a
CHECK declared 'forge:closed-set <set>' whose value list differs from the set:
the CHECK replaced whole with the set's values, the declaration re-issued, and
the compatibility line (a widening is backward-compatible, a narrowing breaks
rollback). The name defaults to <constraint>_closed_set.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if closedSet != "" {
				return cobra.MaximumNArgs(1)(cmd, args)
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if closedSet != "" {
				if cmd.Flags().Changed("backward-compatible") || cmd.Flags().Changed("breaks-rollback") {
					return fmt.Errorf("--closed-set writes its own compatibility line (a widening is backward-compatible, a narrowing breaks rollback); drop --backward-compatible / --breaks-rollback")
				}
				name := closedSet + "_closed_set"
				if len(args) == 1 {
					name = args[0]
				}
				return newClosedSetMigration(cmd.Context(), closedSet, name, resolveMigrationsDir(migDir), dsn, withContext)
			}
			for _, flag := range []string{"backward-compatible", "breaks-rollback"} {
				if v, _ := cmd.Flags().GetString(flag); cmd.Flags().Changed(flag) && strings.TrimSpace(v) == "" {
					return fmt.Errorf("--%s takes the reason: why the previous release keeps working on this schema, or why it deliberately does not", flag)
				}
			}
			opts := &database.MigrationOptions{
				DSN:                dsn,
				Context:            withContext,
				BackwardCompatible: backwardCompatible,
				BreaksRollback:     breaksRollback,
			}
			if store, err := loadProjectStore(); err == nil {
				opts.RequireCompatDeclaration = store.Database().MigrationSafety.RequireCompatDeclaration
			}
			return database.CreateMigration(cmd.Context(), args[0], resolveMigrationsDir(migDir), opts)
		},
	}
	newCmd.Flags().StringVar(&migDir, "dir", "", migrationsDirFlagUsage)
	newCmd.Flags().StringVar(&dsn, "dsn", "", "Database connection string for live schema introspection (implies --context)")
	newCmd.Flags().BoolVar(&withContext, "context", false,
		"write the schema, the previous migration and the history into the file's header instead of a pointer to them")
	// pflag renders the first `back-quoted` word of a usage string as the
	// flag's value name, so the backticks go on the value and nowhere else
	// (TestFlagValueNamesAreOneWord).
	newCmd.Flags().StringVar(&backwardCompatible, "backward-compatible", "",
		"declare that the previous release keeps working on this schema, and `why` (writes \"-- forge:backward-compatible — <why>\")")
	newCmd.Flags().StringVar(&breaksRollback, "breaks-rollback", "",
		"declare that the previous release deliberately does NOT keep working on this schema, and `why` (writes \"-- forge:breaks-rollback <why>\")")
	newCmd.MarkFlagsMutuallyExclusive("backward-compatible", "breaks-rollback")
	newCmd.Flags().StringVar(&closedSet, "closed-set", "",
		"write the migration that brings the forge:closed-set CHECK named `constraint` back to its set (what `forge lint --closed-sets` prints)")
	migrationCmd.AddCommand(newCmd)
	migrationCmd.AddCommand(newDBMigrationRebaseCommand())

	return cmdutil.StrictGroup(migrationCmd)
}

// newDBMigrationRebaseCommand creates the `db migration rebase` subcommand.
// The command string every refusal and lint message names is
// migrationlint.RebaseCommand; TestRebaseCommandExists asserts it resolves
// here.
func newDBMigrationRebaseCommand() *cobra.Command {
	var (
		migDir     string
		allPending bool
	)

	cmd := &cobra.Command{
		Use:   "rebase [file...]",
		Short: "Re-version migrations to fresh timestamps that sort after everything merged",
		Long: `Re-version one or more migrations to fresh UTC timestamps.

WHEN YOU NEED THIS. A migration whose version is below the schema's current
version can never run: golang-migrate applies only what is above its recorded
high-water mark, so the file is not pending, it is invisible. That happens one
way — branches A and B are cut, A allocates the earlier timestamp, and B
merges and deploys first. Main now holds A's SQL at a version the database has
already passed, and every later ` + "`up`" + ` reports the schema current. The first
symptom is a query for a column that does not exist.

The file has not run anywhere, so re-versioning it is safe and is the fix.
Both the migrator's refusal and ` + "`forge lint`" + `'s version rules point here.

Each file keeps its name stem and gains a fresh timestamp that sorts after
every version in the directory AND after the highest version on the default
branch, allocated by the same allocator as ` + "`forge db migration new`" + `. Tracked
files are moved with ` + "`git mv`" + `. A batch keeps its relative order.

WHAT IT REFUSES. A migration already on the default branch keeps its version,
always. Some database has recorded it as applied under its current filename,
and renaming it would leave that database with a recorded version whose file
no longer exists, and the renamed copy re-runs SQL that database already holds.
Write a new forward migration instead. When the merged file is the one a
database has already passed (the migrator refuses it as never-applicable),
replace it: delete it and add the same SQL, written re-runnably (IF NOT
EXISTS), with ` + "`forge db migration new`" + ` — a database that ran it runs the
copy as a no-op.

Examples:
  forge db migration rebase db/migrations/20260101120000_add_users.up.sql
  forge db migration rebase --all-pending`,
		Args: func(cmd *cobra.Command, args []string) error {
			if allPending && len(args) > 0 {
				return fmt.Errorf("--all-pending rebases every migration new on this branch; do not also name files")
			}
			if !allPending && len(args) == 0 {
				return fmt.Errorf("name the migration file(s) to re-version, or pass --all-pending")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := resolveMigrationsDir(migDir)
			paths := make([]string, len(args))
			for i, arg := range args {
				paths[i] = resolveMigrationFileArg(dir, arg)
			}
			if allPending {
				pending, err := database.PendingMigrations(dir)
				if err != nil {
					return err
				}
				if len(pending) == 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "No migrations are new on this branch — nothing to re-version.\n")
					return nil
				}
				paths = pending
			}

			results, err := database.RebaseMigrations(dir, paths)
			if err != nil {
				return err
			}
			if len(results) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "Nothing to re-version.\n")
				return nil
			}
			for _, r := range results {
				fmt.Fprintf(cmd.OutOrStdout(), "%s → %s\n", filepath.Base(r.OldPath), filepath.Base(r.NewPath))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "\nRe-versioned %d migration(s). Review the rename, then `forge db migrate up`.\n", len(results))
			return nil
		},
	}
	cmd.Flags().StringVar(&migDir, "dir", "", migrationsDirFlagUsage)
	cmd.Flags().BoolVar(&allPending, "all-pending", false, "Re-version every migration added on this branch (above the default branch's merge-base)")

	return cmd
}

// resolveMigrationFileArg anchors a migration file named on the command line.
// A bare filename names a file IN the migrations directory — the natural way
// to name one — and any other relative path is project-relative, like every
// other path a db command reads.
func resolveMigrationFileArg(migDir, arg string) string {
	if filepath.IsAbs(arg) {
		return filepath.Clean(arg)
	}
	if filepath.Base(arg) == arg {
		return filepath.Join(migDir, arg)
	}
	return dbProjectPath(arg)
}

// newDBMigrateCommand creates the migrate subcommand.
func newDBMigrateCommand() *cobra.Command {
	migrateCmd := &cobra.Command{
		Use:   "migrate",
		Short: "Apply, inspect and repair migration state with forge's migrator",
		Long: `Apply, inspect, and repair migration state with forge's migrator
(forge/pkg/migratekit, built on golang-migrate) — the same one the project's
own '<binary> db migrate up' and a server's AUTO_MIGRATE run, so every path
takes the same run lock, keeps the same applied record, and reaches the same
verdicts:

  - a version applied from a different file, or one the schema passed without
    applying, is refused before anything runs;
  - a database goose migrated is ADOPTED when the first migration declares
    its history ('-- forge:adopts goose ...');
  - a schema AHEAD of the directory is reported, and refused unless every
    newer version was declared '-- forge:backward-compatible'.

There is no "down": forge rolls forward only. A bad migration is repaired by a
new forward migration written against the state the database is actually in.

Migrations are read from db/migrations/ by default. Nothing else needs to be
installed.

The connection string can be provided via --dsn or, if the flag is omitted,
the DATABASE_URL environment variable.

Examples:
  forge db migrate up --dsn=<dsn>
  forge db migrate up                   # picks up $DATABASE_URL
  DATABASE_URL=... forge db migrate status
  forge db migrate version
  forge db migrate force 20240102150405`,
	}

	migrateCmd.AddCommand(newDBMigrateUpCommand())
	migrateCmd.AddCommand(newDBMigrateStatusCommand())
	migrateCmd.AddCommand(newDBMigrateVersionCommand())
	migrateCmd.AddCommand(newDBMigrateForceCommand())

	return cmdutil.StrictGroup(migrateCmd)
}

func newDBMigrateUpCommand() *cobra.Command {
	var (
		dsn              string
		migDir           string
		lockTimeout      string
		statementTimeout string
		lockAttempts     int
	)

	upCmd := &cobra.Command{
		Use:   "up",
		Short: "Apply pending migrations",
		Long: `Apply pending migrations.

Every migration runs under a lock_timeout (default 5s) and a statement_timeout
(default 30m): a migration that cannot get its lock fails fast instead of
queueing every query on the table behind it. A timed-out migration is rolled
back and tried again with a growing wait, up to --lock-attempts times (default
5), then fails the run with the schema unchanged. One migration overrides the
timeouts for itself with a header comment:

  -- forge:lock-timeout 30s
  -- forge:statement-timeout 2h

A duration is 500ms, 30s, 5m, 1h; "off" disables the timeout.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			limits, err := migratekit.ParseLimits(lockTimeout, statementTimeout, lockAttempts)
			if err != nil {
				return err
			}
			resolved, err := resolveDSN(dsn)
			if err != nil {
				return err
			}
			dir, err := requireMigrationsDir(migDir)
			if err != nil {
				return err
			}
			return runMigrateUpWith(cmd.OutOrStdout(), resolved, dir, limits)
		},
	}

	upCmd.Flags().StringVar(&dsn, "dsn", "", "Database connection string (falls back to $DATABASE_URL)")
	upCmd.Flags().StringVar(&migDir, "dir", "", migrationsDirFlagUsage)
	upCmd.Flags().StringVar(&lockTimeout, "lock-timeout", "", "How long one attempt at a migration waits for a lock, e.g. 5s, 30s, off (default 5s; a migration's own -- forge:lock-timeout wins)")
	upCmd.Flags().StringVar(&statementTimeout, "statement-timeout", "", "Longest one statement may run, e.g. 30m, 2h, off (default 30m; a migration's own -- forge:statement-timeout wins)")
	upCmd.Flags().IntVar(&lockAttempts, "lock-attempts", 0, "Attempts at a migration whose lock times out, the first included (default 5; 1 never retries)")

	return upCmd
}

func newDBMigrateStatusCommand() *cobra.Command {
	var (
		dsn    string
		migDir string
	)

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show migration status",
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := resolveDSN(dsn)
			if err != nil {
				return err
			}
			dir, err := requireMigrationsDir(migDir)
			if err != nil {
				return err
			}
			return runMigrateStatus(cmd.OutOrStdout(), resolved, dir)
		},
	}

	statusCmd.Flags().StringVar(&dsn, "dsn", "", "Database connection string (falls back to $DATABASE_URL)")
	statusCmd.Flags().StringVar(&migDir, "dir", "", migrationsDirFlagUsage)

	return statusCmd
}

func newDBMigrateVersionCommand() *cobra.Command {
	var (
		dsn    string
		migDir string
	)

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Show the current migration version",
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := resolveDSN(dsn)
			if err != nil {
				return err
			}
			dir, err := requireMigrationsDir(migDir)
			if err != nil {
				return err
			}
			return runMigrateVersion(cmd.OutOrStdout(), resolved, dir)
		},
	}

	versionCmd.Flags().StringVar(&dsn, "dsn", "", "Database connection string (falls back to $DATABASE_URL)")
	versionCmd.Flags().StringVar(&migDir, "dir", "", migrationsDirFlagUsage)

	return versionCmd
}

func newDBMigrateForceCommand() *cobra.Command {
	var (
		dsn    string
		migDir string
	)

	forceCmd := &cobra.Command{
		Use:   "force [version]",
		Short: "Clear a dirty migration state by recording a version without running SQL",
		Long: `Record <version> as the applied migration version WITHOUT running any SQL.

Reach for this when a migration failed part-way and golang-migrate marked the
state dirty. Nothing else will run until that flag clears — including
'forge db migrate up', which refuses on a dirty version — so this is the way
out of that loop.

Forcing asserts a fact; it does not verify one. Forge cannot know how much of
the failed migration actually landed, so repair the schema FIRST (inspect it
with 'forge db introspect', then finish or undo the partial migration by hand),
and force only once the database matches what that version intended. Forcing
past a migration whose SQL never ran leaves the schema permanently behind what
forge believes is applied.

The applied record (schema_migrations_applied) follows the version: the
versions a force asserts are recorded as this directory's files, and rows above
the forced version are dropped, so the next 'up' accepts the database.

Examples:
  forge db introspect                    # see what actually landed
  forge db migrate force 20240102150405  # then clear the flag
  forge db migrate up                    # and catch up`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := resolveDSN(dsn)
			if err != nil {
				return err
			}
			dir, err := requireMigrationsDir(migDir)
			if err != nil {
				return err
			}
			return runMigrateForce(cmd.OutOrStdout(), resolved, dir, args[0])
		},
	}

	forceCmd.Flags().StringVar(&dsn, "dsn", "", "Database connection string (falls back to $DATABASE_URL)")
	forceCmd.Flags().StringVar(&migDir, "dir", "", migrationsDirFlagUsage)

	return forceCmd
}

func newDBIntrospectCommand() *cobra.Command {
	var (
		dsn    string
		table  string
		format string
	)

	cmd := &cobra.Command{
		Use:   "introspect",
		Short: "Inspect the migrated database schema",
		Long: `Connect to a PostgreSQL database and display the current schema.

Shows tables, columns, types, constraints, indexes, and foreign keys.

Examples:
  forge db introspect --dsn "postgres://user:pass@localhost/mydb?sslmode=disable"
  forge db introspect --dsn "$DATABASE_URL" --table users
  forge db introspect --dsn "$DATABASE_URL" --format json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDBIntrospect(cmd.Context(), dsn, table, format)
		},
	}

	cmd.Flags().StringVar(&dsn, "dsn", "", "PostgreSQL connection string (required)")
	cmd.Flags().StringVar(&table, "table", "", "Filter to a specific table")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, json")
	_ = cmd.MarkFlagRequired("dsn")

	return cmd
}

func runDBIntrospect(ctx context.Context, dsn, tableFilter, format string) error {
	db, err := database.ConnectDB(ctx, dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	tables, err := database.IntrospectSchema(ctx, db, tableFilter)
	if err != nil {
		return fmt.Errorf("introspecting schema: %w", err)
	}

	if len(tables) == 0 {
		if tableFilter != "" {
			return fmt.Errorf("table %q not found", tableFilter)
		}
		fmt.Println("No tables found in the public schema.")
		return nil
	}

	switch format {
	case "json":
		out, err := database.FormatSchemaJSON(tables)
		if err != nil {
			return err
		}
		fmt.Println(out)
	default:
		fmt.Print(database.FormatSchemaText(tables))
	}

	return nil
}
