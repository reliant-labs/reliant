package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/database"
	"github.com/reliant-labs/forge/internal/shadowdb"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// devModeValue is the runtime MODE value that marks an environment as
// development (config.Mode() keys off the CONFIG_FIELD_ROLE_MODE field). The
// dev env's config.k is scaffolded with it, and `forge env up` layers it onto the
// dev host env.
const devModeValue = "development"

// newDBSeedCommand builds `forge db seed` — the runtime materializer for
// deterministic, FK-coherent development seed data.
//
// The planner and applier live in pkg/seedplan, the shipped module, so a
// project's TESTS can seed a foreign-key spine by calling a Go function
// (seedplan.SeedGraph) instead of shelling this binary. That is a deliberate
// widening: seeding is a development capability, and a test is development.
//
// What keeps it out of production is not reachability, then, but the two
// things that actually decide it. The migrate path — the one component that
// runs against non-dev databases — has no seed code path at all
// (TestGeneratedMigrateTemplateHasNoSeedPath pins it), and this command
// hard-refuses any non-dev environment with no override flag. A server binary
// seeds nothing because nothing in it calls a seeder, which is the same
// reason it does not send mail.
func newDBSeedCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "seed",
		Short: "Materialize deterministic development seed data at runtime",
		Long: `Materialize deterministic, FK-coherent development seed data directly
into a dev database — no seed files are written into your project.

Seeds are introspected from the APPLIED schema (db/migrations, with real
foreign keys), synthesized deterministically per cell, ordered by a
foreign-key topological sort, and INSERTed idempotently (ON CONFLICT DO
NOTHING). apply/reset refuse any non-dev environment; seeding never runs
against staging or production.

Synthesized values satisfy the schema's constraints BY CONSTRUCTION — CHECK
vocabularies, char_length/varchar caps, numeric ranges — and a single-column
UNIQUE column draws without replacement so it never collides. A constraint
that cannot hold the configured row count (a UNIQUE column backed by a short
CHECK vocabulary) caps that table at plan time with a warning, before any
INSERT. apply is one transaction: it seeds everything or nothing, so a failed
run never leaves a half-populated database and is always safe to retry.

Values you supply in db/seeds/vocab.yaml are validated instead, and an invalid
one is skipped with a warning (that column falls back to built-in synthesis).
Timestamps there are written relative to now ({from: -90d, to: +30d}, -3d,
now), and undescribed timestamp columns land in the four weeks before the day
the seed runs.

An entity that reaches one parent by TWO paths (orders.patient_id, and
orders.prescription_id -> prescriptions.patient_id) carries an invariant the
schema implies but does not state. Seeding it independently produces rows that
contradict the rule, so apply REFUSES and prints the declaration to paste:
COMMENT ON CONSTRAINT ... IS 'forge:ref derived-from=<column>' (or
'authoritative', or 'independent'). Load the db/seeding skill for the table.

Which database: with no --dsn, the one the env declares (or $DATABASE_URL when
it is that same database). --dsn may name ANY loopback database — localhost,
127.0.0.1, ::1 or a unix socket — so a throwaway postgres on a spare port can
be seeded. A non-loopback --dsn must be the env's own database, or carry
--allow-remote-dsn. The environment must be dev either way.

Seeding nothing is an error whenever there is something to seed: a database
whose tables the migrations forge read do not define (the wrong -C or --dir)
fails instead of printing "Seeded 0 row(s)". database.seed.tables: [] in
forge.yaml is the deliberate way to synthesize nothing and apply only
db/seeds/custom/.

Examples:
  forge db seed apply                    # seed the dev database
  forge db seed apply -C ~/src/app       # from anywhere
  forge db seed apply --dsn postgres://postgres:postgres@localhost:55432/scratch?sslmode=disable
  forge db seed status                   # per-table seeded-row counts
  forge db seed reset                    # wipe seeded tables and re-seed`,
	}
	cmd.AddCommand(newDBSeedApplyCommand())
	cmd.AddCommand(newDBSeedStatusCommand())
	cmd.AddCommand(newDBSeedResetCommand())
	return cmdutil.StrictGroup(cmd)
}

// seedOptions is what the seed write commands' flags bind.
type seedOptions struct {
	dsn    string
	env    string
	migDir string
	// allowRemoteDSN lets --dsn name a non-loopback server that is not the
	// env's own database. It opens the DSN gate only — never the dev gate.
	allowRemoteDSN bool
}

// bindSeedWriteFlags registers the flags apply and reset share.
func bindSeedWriteFlags(cmd *cobra.Command, o *seedOptions) {
	cmd.Flags().StringVar(&o.dsn, "dsn", "", "Database to seed: any loopback database (localhost, 127.0.0.1, ::1, unix socket), or the env's own (default: what the env declares, or $DATABASE_URL when it is that database)")
	cmd.Flags().StringVar(&o.env, "env", "dev", "Target environment (must be dev; there is no override)")
	cmd.Flags().StringVar(&o.migDir, "dir", "", migrationsDirFlagUsage)
	cmd.Flags().BoolVar(&o.allowRemoteDSN, "allow-remote-dsn", false, "Let --dsn name a NON-loopback server that is not the env's declared database (the env must still be dev)")
}

func newDBSeedApplyCommand() *cobra.Command {
	var o seedOptions
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Materialize seed data into the dev database (dev-only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDBSeedApply(cmd.Context(), o)
		},
	}
	bindSeedWriteFlags(cmd, &o)
	return cmd
}

func newDBSeedStatusCommand() *cobra.Command {
	var (
		dsn    string
		migDir string
	)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show per-table seeded-row counts vs the seed model",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDBSeedStatus(cmd.Context(), dsn, migDir)
		},
	}
	cmd.Flags().StringVar(&dsn, "dsn", "", "Database connection string (falls back to $DATABASE_URL)")
	cmd.Flags().StringVar(&migDir, "dir", "", migrationsDirFlagUsage)
	return cmd
}

func newDBSeedResetCommand() *cobra.Command {
	var o seedOptions
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Delete seeded rows (child-first) and re-seed (dev-only)",
		Long: `Delete the rows forge seeded, child-first so foreign keys stay satisfied,
then seed again from the applied schema.

This is the command to reach for instead of dropping and recreating the
database by hand. It is the supported way to get back to a clean dev dataset
after bad seed data, a vocab.yaml change, or hand-edited rows — your schema and
migration state are left alone, so there is nothing to re-migrate afterwards.

It does NOT repair a broken migration state. reset seeds, and seeding requires
a fully-migrated schema, so on a database with pending or dirty migrations it
refuses exactly as 'seed apply' does — clear that first with
'forge db migrate up' or 'forge db migrate force <version>'.

Only rows matching forge's deterministic seed data are deleted; rows you or
your application created are left in place.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDBSeedReset(cmd.Context(), o)
		},
	}
	bindSeedWriteFlags(cmd, &o)
	return cmd
}

// seedShadowServer resolves the postgres SERVER the project's migrations get
// applied to so their schema can be introspected.
//
// This lives in the CLI, not in seedplan, because resolving it means knowing
// how a forge project stores its database coordinates (forge.yaml, .env, the
// per-env KCL config) — see internal/shadowdb. seedplan takes the answer as a
// parameter so it stays a library: it seeds whatever database it is handed
// and has no opinion about where projects keep their config. An empty result
// means "no reachable configured server", and pgtest's embedded postgres is
// used instead.
//
// projectDir is passed rather than derived from the migrations directory: a
// project may configure database.migrations_dir anywhere, and the
// grandparent of an arbitrary directory is not its project.
func seedShadowServer(projectDir string) string {
	return shadowdb.Resolve(projectDir)
}

// seedConfigFromProject is the project's dev dataset configuration: the
// library defaults (20 rows per table, salt 0) — the salt and row counts every
// consumer of the dataset shares. There is no forge.yaml seed block — the
// defaults are the contract, and `forge env up --no-seed` skips auto-seed for
// one run.
//
// It is CLOCK-FREE: Now is unset, which seedplan resolves to its fixed
// reference instant. `forge generate` renders committed files from this config
// (the frontend mock fixtures), and generated output must be a pure function
// of its inputs — anchored at today, every committed src/mocks/*_gen.ts went
// stale at 00:00Z and a project's generated-code CI check turned red daily
// with no source change. A command that WRITES a dev database, where dates
// should read as current, uses liveSeedConfig instead.
func seedConfigFromProject() seedplan.Config {
	return seedplan.DefaultConfig()
}

// liveSeedConfig is seedConfigFromProject anchored at today (seedNow), for the
// commands that seed a running dev database — `forge db seed apply|status|reset`,
// `forge db reset` and `forge env up`'s auto-seed — so relative and synthesized
// timestamps read as current rather than as January 2024. Its output moves
// with the calendar: never render a committed file from it.
func liveSeedConfig() seedplan.Config {
	c := seedConfigFromProject()
	c.Now = seedNow()
	return c
}

// seedNow is the instant a runtime seed anchors relative time to: the start of
// the current UTC day. A day, not the instant, so every seed run on one day
// writes the same dataset — a `seed reset` reproduces what `seed apply` wrote
// an hour earlier, which is what makes a seeded row a stable thing to debug
// against.
func seedNow() time.Time {
	return seedClock().UTC().Truncate(24 * time.Hour)
}

// seedClock is the wall clock seedNow reads — a var so a test can run forge
// on two different calendar days.
var seedClock = time.Now

// errNonDevSeedTarget is the structural CLI gate: apply/reset refuse any
// environment forge cannot confirm is dev, and there is deliberately no
// override flag (an override is exactly the conventional hole this gate
// exists to close).
func requireDevSeedTarget(env string) error {
	dev, err := seedTargetIsDev(env)
	if err != nil {
		return err
	}
	if !dev {
		return fmt.Errorf("refusing to seed: environment %q is not dev — seed data is a dev-only feature and there is no override flag.\n"+
			"Seeds never run against staging/production; for a deliberate non-dev demo dataset use psql directly", env)
	}
	return nil
}

// seedTargetIsDev classifies the target environment as dev by reading the
// runtime MODE (the same value config.Mode() reads). It is
// FAIL-CLOSED: any environment it cannot positively confirm as development is
// treated as non-dev.
func seedTargetIsDev(env string) (bool, error) {
	cfgPath, err := findProjectConfigFile()
	if err != nil {
		return false, err
	}
	return seedEnvIsDevIn(filepath.Dir(cfgPath), env), nil
}

// seedEnvIsDevIn is the pure, testable core of the dev classifier. It reads the
// runtime MODE from the per-env KCL config (deploy/kcl/<env>/config.k, where the
// dev env is scaffolded with `environment = "development"`) and returns true
// only for "development"/"dev". FAIL-CLOSED — any environment it cannot
// positively confirm as development (no config source, unset mode,
// production/test/staging) is treated as non-dev.
func seedEnvIsDevIn(projectDir, env string) bool {
	mode, ok := envModeFromKCLConfig(projectDir, env)
	if !ok {
		return false
	}
	return isDevMode(mode)
}

// isDevMode reports whether a runtime MODE string names the development
// environment. Trimmed + case-insensitive; anything else is non-dev.
func isDevMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "development", "dev":
		return true
	}
	return false
}

// kclConfigModeRE extracts the `environment = "value"` MODE assignment from a
// per-env config.k AppConfig instance. KCL uses `=`; the `:` form is accepted
// defensively. Matches the config field NAME the loader/gate assume for the
// MODE role.
var kclConfigModeRE = regexp.MustCompile(`(?m)^\s*environment\s*[:=]\s*"([^"]*)"`)

// envModeFromKCLConfig reads the runtime `environment` MODE value from the
// per-env config source, deploy/kcl/<env>/config.k. Returns (value, true) when
// the file exists and sets `environment`; ("", false) otherwise (absent file,
// or the field unset, inheriting the schema default). Best-effort text read:
// the value is a plain string literal in a forge-scaffolded instance, so a full
// KCL evaluation would be disproportionate for a fail-closed gate.
func envModeFromKCLConfig(projectDir, env string) (string, bool) {
	path := filepath.Join(projectDir, "deploy", "kcl", env, "config.k")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	m := kclConfigModeRE.FindSubmatch(data)
	if m == nil {
		return "", false
	}
	return string(m[1]), true
}

// openSeedDB resolves the DSN and opens a live connection, and (for
// apply/reset) refuses when migrations are pending — seeds apply only against
// a fully-migrated schema.
//
// o.env is the environment the caller claimed. When it is non-empty the DSN is
// resolved THROUGH it (resolveSeedWriteDSN) rather than beside it: the dev
// gate used to classify the environment while resolveDSN independently took
// --dsn or $DATABASE_URL, so `--env dev --dsn postgres://prod-host/app` passed
// the dev check and then wrote to production. `seed status` passes "" — it is
// read-only and has no env flag to reconcile against.
func openSeedDB(ctx context.Context, o seedOptions, migDir string, checkPending bool) (*sql.DB, error) {
	resolved, err := resolveSeedTargetDSN(ctx, o)
	if err != nil {
		return nil, err
	}
	db, err := database.ConnectDB(ctx, resolved)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	if checkPending {
		block, perr := seedplan.MigrationsPending(ctx, db, migDir)
		if perr != nil {
			_ = db.Close()
			return nil, perr
		}
		if block != nil {
			_ = db.Close()
			return nil, errors.New(seedBlockedMessage(block))
		}
	}
	return db, nil
}

// resolveSeedTargetDSN resolves the DSN a seed command will write through.
//
// With an env, the DSN goes through the seed DSN policy — see
// resolveSeedWriteDSN in db_target.go for which DSNs pass and why. Without one
// (read-only `seed status`), it falls back to the historic resolution: there
// is no env claim to check the DSN against, and nothing is written.
func resolveSeedTargetDSN(ctx context.Context, o seedOptions) (string, error) {
	if o.env == "" {
		return resolveDSN(o.dsn)
	}
	return resolveSeedWriteDSN(ctx, o.dsn, projectDirForKCL(), o.env, o.allowRemoteDSN)
}

// seedBlockedMessage turns a MigrationBlock into a refusal that names the next
// command to run.
//
// The two states need OPPOSITE advice, and collapsing them is what made this a
// dead end in a dogfood run. A PENDING database simply has not caught up:
// `forge db migrate up` is exactly right. A DIRTY database has a migration
// that failed part-way, and `migrate up` refuses it again — so advising that
// sends the author in a circle. They escaped by hand-editing schema_migrations
// with raw SQL and hand-repairing rows, which is precisely the manual database
// surgery forge exists to prevent. `forge db migrate force <version>` already
// existed; the message simply never named it.
//
// Order matters in the dirty case. `forge db seed reset` is the blunter exit
// for a scratch database — and it runs THIS SAME CHECK, so on a dirty database
// it refuses identically. Naming it first would be a second dead end wearing
// the first one's clothes, so it is named as the step after the flag clears.
//
// The numbered path itself lives in dirtyRecoveryMessage, shared with the
// `forge db migrate` commands. Only the seed-specific steps and closing
// paragraph are supplied here.
func seedBlockedMessage(block *seedplan.MigrationBlock) string {
	if !block.Dirty {
		// The PENDING refusal is one half of a cycle, and saying only
		// "migrate up" is what closed it. When `migrate up` cannot apply
		// because EXISTING rows violate the migration it is adding — a
		// foreign key over a column seeding filled with placeholders — the
		// two commands block each other: seeding needs the schema caught up,
		// and catching up needs the rows gone. Naming `forge db reset` here
		// gives the scratch-database reader the exit that needs neither.
		return fmt.Sprintf(`refusing to seed: %s. Run `+"`forge db migrate up`"+` first.

If `+"`forge db migrate up`"+` itself fails because existing rows violate the migration it is applying, the two commands are blocking each other and neither can go first. On a scratch dev database, discard the state instead of repairing it:

  forge db reset        # DROP, recreate, migrate to head, and seed (dev-only)`, block.Reason)
	}

	return dirtyRecoveryMessage(
		"refusing to seed: ",
		block.Version,
		[]string{
			"Catch up:        forge db migrate up",
			"Re-seed:         forge db seed apply",
		},
		"If this is a scratch dev database whose contents do not matter, you do not have to repair anything: `forge db reset` DROPs the database, recreates it, migrates to head and seeds. There is no dirty flag to clear and no schema to reconstruct when the database is new, which is why it is the one exit that cannot be refused by the state you are trying to escape. (`forge db seed reset` is the narrower tool — it keeps your schema and only replaces rows — but it runs this same check, so it needs steps 1-2 done first.)",
	)
}

func runDBSeedApply(ctx context.Context, o seedOptions) error {
	if err := requireDevSeedTarget(o.env); err != nil {
		return err
	}
	migDir, err := requireMigrationsDir(o.migDir)
	if err != nil {
		return err
	}
	db, err := openSeedDB(ctx, o, migDir, true)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	cfg := liveSeedConfig()
	plan, err := seedplan.BuildLivePlan(ctx, db, migDir, seedShadowServer(dbProjectRoot()), cfg)
	if err != nil {
		return err
	}
	printSeedWarnings(plan)
	res, err := seedplan.Apply(ctx, db, plan)
	if err != nil {
		return err
	}
	if err := seedOutcomeError(ctx, db, plan, cfg, res, migDir); err != nil {
		return err
	}
	printSeedResult("Seeded", plan, cfg, res)
	return applyCustomSeedOverlay(ctx, db, migDir)
}

// seedOutcomeError turns "the seeder wrote nothing" into an error whenever
// nothing is not a legitimate answer.
//
// It used to be one: `seed apply -C <proj>` run from another directory read
// migrations from the CWD, found none, planned zero tables, and printed
// "Seeded 0 row(s) across 0 table(s)" with exit 0 — on a database that was
// either fully migrated or entirely empty. A success line over a no-op is the
// worst outcome a seeder has, because the next step (a UI, a test, an agent)
// trusts it.
//
// Nothing is legitimate in exactly three cases, and each is checked:
//
//   - forge.yaml says `database.seed.tables: []` — synthesize nothing, on
//     purpose; db/seeds/custom/ is the dataset.
//   - the database was already seeded — apply is idempotent, so a re-run
//     inserts nothing and the tables still hold rows.
//   - there is nothing to seed: the migrations define no tables AND the
//     database holds none.
//
// Everything else — a database with tables whose schema the migrations forge
// read does not define, a `tables:` scope naming no real table, or a plan that
// inserted nothing into tables that are still empty — is an error naming the
// directory that was read.
func seedOutcomeError(ctx context.Context, db *sql.DB, plan *seedplan.Plan, cfg seedplan.Config, res *seedplan.Result, migDir string) error {
	if cfg.Tables != nil && len(cfg.Tables) == 0 {
		return nil
	}
	if len(plan.Tables()) == 0 {
		if len(cfg.Tables) > 0 {
			return fmt.Errorf("seeded nothing: database.seed.tables in forge.yaml names %s, but none of them is a table the migrations in %s define",
				strings.Join(cfg.Tables, ", "), migDir)
		}
		live, err := liveTableCount(ctx, db)
		if err != nil {
			return err
		}
		if live > 0 {
			return fmt.Errorf("seeded nothing: the database has %d table(s), but the migrations in %s define none, so forge planned no rows. "+
				"That is the wrong migrations directory — check -C (the project) and --dir", live, migDir)
		}
		return nil
	}
	if res.Total() > 0 {
		return nil
	}
	rows, err := seedplan.Status(ctx, db, plan)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.Count > 0 {
			return nil // already seeded: idempotent re-run
		}
	}
	return fmt.Errorf("seeded nothing: the plan targets %d table(s) (%s) from %s, but inserted 0 rows and every one of them is still empty",
		len(rows), strings.Join(plan.Tables(), ", "), migDir)
}

// liveTableCount counts the user tables in the target database — everything
// but the catalogs and golang-migrate/migratekit bookkeeping.
func liveTableCount(ctx context.Context, db *sql.DB) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM information_schema.tables
		WHERE table_type = 'BASE TABLE'
		  AND table_schema NOT IN ('pg_catalog', 'information_schema')
		  AND table_name NOT LIKE 'schema\_migrations%'`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count tables in the target database: %w", err)
	}
	return n, nil
}

// printSeedResult reports what a seed run inserted, saying plainly when
// nothing was synthesized on purpose or because the rows already exist.
func printSeedResult(verb string, plan *seedplan.Plan, cfg seedplan.Config, res *seedplan.Result) {
	switch {
	case cfg.Tables != nil && len(cfg.Tables) == 0:
		fmt.Fprintln(os.Stdout, "database.seed.tables is [] in forge.yaml — no rows synthesized; applying db/seeds/custom/ only.")
		return
	case len(plan.Tables()) == 0:
		fmt.Fprintln(os.Stdout, "No seedable tables: the migrations define none.")
		return
	case res.Total() == 0:
		fmt.Fprintf(os.Stdout, "%s 0 new row(s): all %d table(s) already hold seed data (apply is idempotent; `%s db seed reset` re-seeds).\n",
			verb, len(plan.Tables()), Name())
		return
	}
	fmt.Fprintf(os.Stdout, "%s %d row(s) across %d table(s):\n", verb, res.Total(), len(res.Tables))
	for _, tr := range res.Tables {
		fmt.Fprintf(os.Stdout, "  %-32s %d\n", tr.Table, tr.Inserted)
	}
}

// printSeedWarnings surfaces the plan's warnings: db/seeds/vocab.yaml
// validation problems, and the constraint-satisfaction notes the planner
// records (a row target capped to a UNIQUE column's vocabulary). Warnings
// never fail the seed — an invalid vocab value is skipped and a fully-invalid
// column falls back to built-in synthesis; a MALFORMED vocab file is a hard
// error from BuildLivePlan instead.
func printSeedWarnings(plan *seedplan.Plan) {
	for _, w := range plan.Warnings() {
		fmt.Fprintf(os.Stdout, "warning: %s\n", w)
	}
}

// applyCustomSeedOverlay applies the user-owned db/seeds/custom/*.sql overlay
// (lexicographic order) AFTER the runtime dataset — the sanctioned hook for
// hand-authored / domain-flavored demo data. Missing dir is a no-op.
func applyCustomSeedOverlay(ctx context.Context, db *sql.DB, migDir string) error {
	dir := filepath.Join(filepath.Dir(migDir), "seeds", "custom")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // no overlay directory — nothing to apply
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	if len(files) == 0 {
		return nil
	}
	sort.Strings(files)
	for _, name := range files {
		raw, rerr := os.ReadFile(filepath.Join(dir, name))
		if rerr != nil {
			return fmt.Errorf("read custom seed %s: %w", name, rerr)
		}
		if strings.TrimSpace(stripSQLComments(string(raw))) == "" {
			continue // fully-commented example — skip
		}
		if _, eerr := db.ExecContext(ctx, string(raw)); eerr != nil {
			return fmt.Errorf("apply custom seed %s: %w", name, eerr)
		}
		fmt.Fprintf(os.Stdout, "Applied custom overlay %s\n", name)
	}
	return nil
}

// stripSQLComments removes leading -- line comments to detect a
// fully-commented example file (so applying the scaffolded example.sql is a
// clean no-op).
func stripSQLComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "--") {
			continue
		}
		b.WriteString(t)
	}
	return b.String()
}

func runDBSeedStatus(ctx context.Context, dsn, flagDir string) error {
	migDir, err := requireMigrationsDir(flagDir)
	if err != nil {
		return err
	}
	db, err := openSeedDB(ctx, seedOptions{dsn: dsn}, migDir, false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	plan, err := seedplan.BuildLivePlan(ctx, db, migDir, seedShadowServer(dbProjectRoot()), liveSeedConfig())
	if err != nil {
		return err
	}
	printSeedWarnings(plan)
	rows, err := seedplan.Status(ctx, db, plan)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(os.Stdout, "No seedable tables (no entities in the applied schema).")
		return nil
	}
	fmt.Fprintf(os.Stdout, "%-32s %8s %10s\n", "TABLE", "ROWS", "SEED_TARGET")
	for _, r := range rows {
		fmt.Fprintf(os.Stdout, "%-32s %8d %10d\n", r.Table, r.Count, r.Expected)
	}
	return nil
}

func runDBSeedReset(ctx context.Context, o seedOptions) error {
	if err := requireDevSeedTarget(o.env); err != nil {
		return err
	}
	migDir, err := requireMigrationsDir(o.migDir)
	if err != nil {
		return err
	}
	db, err := openSeedDB(ctx, o, migDir, true)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	cfg := liveSeedConfig()
	plan, err := seedplan.BuildLivePlan(ctx, db, migDir, seedShadowServer(dbProjectRoot()), cfg)
	if err != nil {
		return err
	}
	printSeedWarnings(plan)
	res, err := seedplan.Reset(ctx, db, plan)
	if err != nil {
		return err
	}
	if err := seedOutcomeError(ctx, db, plan, cfg, res, migDir); err != nil {
		return err
	}
	printSeedResult("Reset and seeded", plan, cfg, res)
	return applyCustomSeedOverlay(ctx, db, migDir)
}
