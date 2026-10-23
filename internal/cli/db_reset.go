// File: internal/cli/db_reset.go
//
// `forge db reset` — drop, recreate, migrate to head, seed. One verb.
//
// # Why this command exists
//
// A dogfood run hit a cycle with no forge-supported escape, from a completely
// ordinary sequence: seed a dev database, then add a foreign key.
//
//	forge db migrate up    -> FK violation; the migration fails part-way,
//	                          and the database is left DIRTY at 7
//	forge db seed reset    -> refuses: dirty. "Clear it: forge db migrate force 7"
//	forge db migrate force 6
//	forge db seed reset    -> refuses: applied 6 is BEHIND latest 00007
//	forge db migrate up    -> FK violation ... which is where we came in
//
// Every documented recovery path refuses, and they refuse for OPPOSITE
// reasons. `seed reset` requires a fully-migrated schema; `migrate up`
// requires rows that only `seed reset` can delete. The rows and the schema
// each block the other's repair.
//
// Note the seed data is not "wrong". org_id was born without a foreign key
// (the stem "org" does not resolve to the entity "Organization"), so
// `forge db seed apply` filled it with documented placeholder synthesis —
// `sample_org_id_14` — while organizations.id holds UUIDs. Nothing is
// inconsistent until the FK is added, which is the ordinary act of tightening
// a schema during the birth window.
//
// The author escaped with raw psql: `TRUNCATE ... CASCADE`, then force, then
// up. That is precisely the manual database surgery `forge db --help` tells
// you not to do, and on a shared or remote dev database it would not have
// been available at all.
//
// reset needs NO dirty-state reasoning because it discards the state. That is
// what makes it the one command that cannot be caught in the loop: it does
// not ask what the schema is, it replaces it.
//
// # Why it is safe
//
// It issues DROP DATABASE, so it carries two gates that `seed reset` does not
// get to skip, plus one they now share:
//
//  1. The env must be confirmed dev (requireDevResetTargetIn — the same
//     fail-closed classifier, no override flag).
//  2. The DSN must reconcile with what the env declares (reconcileClaimedDSN
//     — see db_target.go; this closed a real hole where --env dev --dsn
//     postgres://prod-host/app passed the dev check and acted on prod).
//  3. The resolved host and database are PRINTED and confirmed by typing the
//     database name. --yes skips the prompt for non-interactive use.
//
// Forge models exactly one database (config.DatabaseConfig is singular), so
// reset resets THE database the DSN resolves to. It must be honest about
// which one, never guess silently — this machine runs several postgres
// instances holding real state, which is exactly how a silent guess becomes
// a disaster.

package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/database"
	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// resetOptions is exactly what the flags bind — no more.
//
// An earlier draft carried two extra fields so tests could bypass the dev
// classifier and inject a declared DSN. forge's own dead-code guard rejected
// it, correctly: production never wrote either field, so the tests were green
// on a shape production cannot produce. That is a false green on the two
// gates that stand in front of a DROP DATABASE — the worst possible place for
// one. Tests now drive the REAL path, pointing projectDir at a temp project
// (deploy/kcl/<env>/config.k marks it development) and the declared DSN at a
// KCL fixture through FORGE_KCL_RENDER_FIXTURE, the seam RenderKCL already
// documents.
type resetOptions struct {
	dsn    string
	env    string
	migDir string
	yes    bool

	// projectDir is the root whose deploy/kcl/<env>/ answers both "is this
	// env development" and "which database does it declare". The command
	// resolves it from the working directory at the flag boundary; it is a
	// field rather than a lookup inside so both gates read ONE project.
	projectDir string
}

func newDBResetCommand() *cobra.Command {
	var opts resetOptions

	cmd := &cobra.Command{
		Use:   "reset",
		Short: "DROP the dev database, recreate it, migrate to head, and seed (dev-only)",
		Long: `DROP the dev database, recreate it empty, apply every migration, and seed.
One verb for "this scratch database is wedged — just rebuild it".

This is the way out of a state nothing else can exit. When a migration fails
part-way the database is marked dirty, and the two repairs block each other:
'forge db seed reset' refuses because the schema is behind, while
'forge db migrate up' refuses because the rows only seed reset can delete
violate the new constraint. Adding a foreign key to a column that seeding
filled with placeholders is enough to produce it.

reset needs none of that reasoning because it DISCARDS the state rather than
repairing it. There is no dirty flag to clear and no row to fix: the database
is gone and rebuilt from db/migrations.

It is destructive and dev-only, so it is gated three ways:

  - the environment must be confirmed development (from deploy/kcl/<env>/config.k);
    there is no override flag
  - the connection string must be the one that environment declares — a DSN
    forge cannot reconcile with <env> is refused, not assumed. Unlike
    'forge db seed apply', an arbitrary loopback --dsn is NOT accepted: this
    command DROPs the database, and other projects' databases live on
    loopback too
  - the resolved host and database name are printed and must be confirmed;
    pass --yes for non-interactive use

Prefer 'forge db seed reset' when only the ROWS are bad: it keeps your schema
and migration state, so there is nothing to re-migrate afterwards.

Examples:
  forge db reset                    # confirm interactively
  forge db reset --yes              # non-interactive (CI, scripts)
  forge db reset --dsn "$DATABASE_URL" --yes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolve the project root ONCE, here at the command boundary,
			// so both gates below read the same project: the dev
			// classification and the declared-DSN lookup must agree about
			// which deploy/kcl/<env>/ they are talking about, and resolving
			// it twice inside is how they could come to disagree.
			opts.projectDir = projectDirForKCL()
			migDir, err := requireMigrationsDir(opts.migDir)
			if err != nil {
				return err
			}
			opts.migDir = migDir
			return runDBReset(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVar(&opts.dsn, "dsn", "", "Database connection string (falls back to $DATABASE_URL, then what the env declares)")
	cmd.Flags().StringVar(&opts.env, "env", "dev", "Target environment (must be dev; there is no override)")
	cmd.Flags().StringVar(&opts.migDir, "dir", "", migrationsDirFlagUsage)
	cmd.Flags().BoolVar(&opts.yes, "yes", false, "Skip the confirmation prompt (non-interactive use)")
	return cmd
}

// requireDevResetTargetIn is the dev gate for the destructive verb. It reads
// the SAME fail-closed classifier seed apply/reset use (seedEnvIsDevIn: the
// runtime MODE in deploy/kcl/<env>/config.k), with the same absence of an
// override — a destructive command inherits the stricter posture, never the
// looser one.
//
// The project directory is a parameter rather than resolved from the working
// directory, so the gate can be exercised against a temp project without the
// test standing in a different cwd.
func requireDevResetTargetIn(projectDir, env string) error {
	if !seedEnvIsDevIn(projectDir, env) {
		return fmt.Errorf("refusing to reset: environment %q is not dev — `forge db reset` DROPS the database, so it runs only against an environment forge can confirm is development, and there is no override flag.\n"+
			"If you need to rebuild a staging or production database, that is a deliberate operational act and belongs in your deploy tooling, not here", env)
	}
	return nil
}

// resetConfirmationPrompt renders what the user must read before a DROP: the
// resolved SERVER and DATABASE NAME, not an env string that was defaulted.
// The password is never shown.
func resetConfirmationPrompt(dsn string) string {
	identity := databaseIdentity(dsn)
	if identity == "" {
		identity = redactDSNForMessage(dsn)
	}
	name := databaseNameOf(dsn)
	return fmt.Sprintf(`About to DROP and rebuild:

    %s

Everything in that database will be destroyed: every table, every row,
including any data you created by hand. It will be recreated empty, migrated
to head, and seeded.

Type the database name (%s) to confirm, or anything else to abort: `, identity, name)
}

// resetConfirmed reports whether what the user typed confirms the drop.
//
// Echoing the database NAME is the confirmation, not "y". The whole point of
// printing the target is that the user reads it, and a single keystroke is
// too easy to give reflexively for DROP DATABASE — on a machine running
// several postgres instances, reading the name is the step that catches the
// wrong one. Case-sensitive, because postgres identifiers are.
func resetConfirmed(typed, database string) bool {
	return strings.TrimSpace(typed) == database && database != ""
}

// databaseNameOf extracts the database name from a DSN, or "".
func databaseNameOf(dsn string) string {
	id := databaseIdentity(dsn)
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return ""
}

func runDBReset(ctx context.Context, opts resetOptions) error {
	// 1. The environment must be dev. First, because it is the cheapest
	//    refusal and needs no connection.
	if err := requireDevResetTargetIn(opts.projectDir, opts.env); err != nil {
		return err
	}

	// 2. Resolve the DSN *with* the env, not beside it. This is the gap that
	//    used to let `--env dev --dsn postgres://prod-host/app` through: the
	//    dev gate read the env's config file while the DSN came from a flag
	//    nobody checked against it.
	dsn, err := resolveEnvDSN(ctx, opts.dsn, opts.projectDir, opts.env)
	if err != nil {
		return err
	}

	name := databaseNameOf(dsn)
	if name == "" {
		return fmt.Errorf("refusing to reset: %s names no database", redactDSNForMessage(dsn))
	}

	// 3. Print the target and confirm. The DSN has been reconciled by now, so
	//    what is printed is what will be dropped.
	if !opts.yes {
		fmt.Fprint(os.Stdout, resetConfirmationPrompt(dsn))
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if !resetConfirmed(line, name) {
			return fmt.Errorf("aborted: nothing was dropped (type the database name %q to confirm, or pass --yes)", name)
		}
	}

	// 4. Drop and recreate. pgtest already owns this mechanism — the
	//    maintenance connection (postgres will not drop the database you are
	//    connected to), the backend termination that keeps DROP from blocking
	//    on a running dev server, and the identifier quoting that keeps a name
	//    like "control-plane" from being parsed as SQL. Reused rather than
	//    re-written; the gates above are what make calling it safe.
	if err := pgtest.RecreateDatabase(dsn); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Dropped and recreated %s\n", databaseIdentity(dsn))

	// 5. Migrate to head. A freshly created database has no dirty flag and
	//    no rows, so this is the one path on which `migrate up` cannot be
	//    refused by state left behind.
	if err := runMigrateUp(os.Stdout, dsn, opts.migDir); err != nil {
		return fmt.Errorf("reset: recreated %s but could not migrate it: %w", name, err)
	}

	// 6. Seed. A reset that stopped at an empty schema would leave the user
	//    exactly one command short of where they were trying to get.
	return resetSeed(ctx, dsn, opts.migDir, opts.projectDir)
}

// resetSeed materializes the seed dataset into the freshly migrated database.
//
// It calls seedplan directly rather than runDBSeedApply because the gates
// have already been satisfied — re-running the dev check and the pending
// check here would only re-derive answers this command established two steps
// ago, and the pending check in particular has nothing to say about a
// database whose schema forge just applied itself.
func resetSeed(ctx context.Context, dsn, migDir, projectDir string) error {
	db, err := database.ConnectDB(ctx, dsn)
	if err != nil {
		return fmt.Errorf("reset: migrated the database but could not connect to seed it: %w", err)
	}
	defer func() { _ = db.Close() }()

	cfg := liveSeedConfig()
	plan, err := seedplan.BuildLivePlan(ctx, db, migDir, seedShadowServer(projectDir), cfg)
	if err != nil {
		return fmt.Errorf("reset: migrated the database but could not plan seeds: %w", err)
	}
	printSeedWarnings(plan)
	res, err := seedplan.Apply(ctx, db, plan)
	if err != nil {
		return fmt.Errorf("reset: migrated the database but seeding failed: %w", err)
	}
	if err := seedOutcomeError(ctx, db, plan, cfg, res, migDir); err != nil {
		return fmt.Errorf("reset: migrated the database but %w", err)
	}
	printSeedResult("Migrated to head and seeded", plan, cfg, res)
	return applyCustomSeedOverlay(ctx, db, migDir)
}
