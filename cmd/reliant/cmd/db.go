// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strconv"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/pkg/observe/dbtrace"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/app"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/pkg/config"
)

// newDBCmd registers `reliant db` — the schema owner, detached from the server.
//
// Applying migrations used to be reachable only by starting `reliant server
// api`: it is the one process that holds db.MigrateApply, and every other
// process (gateway, temporal-worker) opens with db.MigrateWait and BLOCKS
// until the schema is current. That coupling deadlocks any deployment that
// brings a waiter up before the api-server. control-plane's dev env is the
// case: the api-server runs on the host, which `forge env up` starts only
// AFTER the cluster rollout — and that rollout waits on a daemon-gateway pod
// waiting on the api-server. Nothing in the cycle can move.
//
// `db migrate up` breaks it by letting a one-shot Job hold MigrateApply ahead
// of every workload that waits on the schema. It is the SAME db.Migrate call
// over the SAME embedded migrations the api-server makes, so the
// single-migrator rule is preserved — the owner moved, it did not multiply.
// Running it beside a live api-server is safe (the migrators' locks make them
// take turns) but pointless.
func init() { registerCommand(newDBCmd) }

func newDBCmd(Deps) *cobra.Command {
	dbCmd := &cobra.Command{
		Use:   "db",
		Short: "Database schema commands",
		Long: `Apply or inspect the migrations embedded in this binary.

DATABASE_URL (required) and DATABASE_DRIVER are read from the environment —
the same variables the servers read, so a migration Job and the api-server
cannot disagree about which database they mean. Migrations run on
DATABASE_DIRECT_URL instead when it is set: the migrator holds session-level
locks, which a transaction-pooling PgBouncer on DATABASE_URL cannot keep.`,
	}

	migrateCmd := &cobra.Command{
		Use:   "migrate",
		Short: "Manage database migrations",
	}

	upCmd := &cobra.Command{
		Use:   "up",
		Short: "Apply every pending migration",
		Long: `Apply every migration this binary embeds that the database has not
recorded applied, then exit.

Exits 0 when the schema is already current, so it is safe to run on every
deploy and safe to re-run after a failure.

A database with reliant's tables but no migration record is refused with
nothing applied. ` + "`db migrate status`" + ` previews what ` + "`up`" + ` will do.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadDatabaseConfig(cmd)
			if err != nil {
				return err
			}
			migrationURL := migrationURL(cfg)

			out := cmd.OutOrStdout()
			// The migrator's report, one fact per line.
			res, err := db.Migrate(cmd.Context(), migrationURL)
			if err != nil {
				return err
			}
			for _, line := range res.Lines() {
				fmt.Fprintln(out, line)
			}

			// Belt-and-braces: the migrator reported success, so a schema
			// short of the newest embedded version means it applied a
			// different set than this binary embeds — a mismatch worth failing
			// the Job over, because every waiter is about to block on exactly
			// this set.
			sqlDB, err := dbtrace.Open("pgx", migrationURL)
			if err != nil {
				return fmt.Errorf("verify schema after migrate: %w", err)
			}
			defer func() { _ = sqlDB.Close() }()
			state, err := db.ReadMigrationState(sqlDB)
			if err != nil {
				return fmt.Errorf("verify schema after migrate: %w", err)
			}
			embedded, err := db.EmbeddedMigrationVersions()
			if err != nil {
				return fmt.Errorf("verify schema after migrate: %w", err)
			}
			latest := embedded[len(embedded)-1]
			if !state.Applied || state.Dirty || int64(state.Version) < latest {
				return fmt.Errorf("migrate up reported success but the schema is at version %s (dirty %t), short of this binary's newest migration %d",
					versionLabel(state.Applied, state.Version), state.Dirty, latest)
			}
			if int64(state.Version) > latest {
				fmt.Fprintf(out, "schema is AHEAD of this binary (version %d, this binary embeds up to %d); nothing was applied\n", state.Version, latest)
				return nil
			}
			fmt.Fprintf(out, "schema is current (version %d)\n", state.Version)
			return nil
		},
	}
	migrateCmd.AddCommand(upCmd)

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Preview what `db migrate up` would do, changing nothing",
		Long: `Read the database's migration state and print what ` + "`db migrate up`" + ` from
this binary would do to it: nothing (up to date), apply pending migrations,
build a fresh database, or refuse — and why.

The first line is the verdict. Exits non-zero when ` + "`up`" + ` would refuse.

Every read runs in one READ ONLY transaction, so Postgres itself rejects any
write: it creates nothing, takes no lock, and does not wait for a migrator.
It is the pre-flight for a database we keep, and stays usable to diagnose the
very deadlock the schema wait produces.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadDatabaseConfig(cmd)
			if err != nil {
				return err
			}
			migrationURL := migrationURL(cfg)

			// Deliberately NOT ConnectWithConfig: both of its policies act on
			// the schema (apply, or block until current).
			sqlDB, err := dbtrace.Open("pgx", migrationURL)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer func() { _ = sqlDB.Close() }()

			plan, err := readOnlyPlan(cmd.Context(), sqlDB)
			if err != nil {
				return err
			}
			if printPlan(cmd.OutOrStdout(), plan) {
				return errors.New("`db migrate up` would refuse this database; nothing was changed")
			}
			return nil
		},
	}
	migrateCmd.AddCommand(statusCmd)

	forceCmd := &cobra.Command{
		Use:   "force <version>",
		Short: "Clear a dirty migration state by recording a version without running SQL",
		Long: `Record <version> as the schema's version and clear the dirty flag, WITHOUT
running any SQL — the way out when a migration failed partway and ` + "`up`" + `
refuses the dirty schema.

Forcing asserts a fact; it does not verify one. Inspect the database and
finish or undo the partial migration FIRST, then force the version the schema
now truly reflects and run ` + "`up`" + `. The versions it asserts are recorded as
this binary's own embedded migrations, so run the release that failed. -1
means no version applied. Never force the baseline onto a database: that
records it without checking that the schema is the one it builds.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			version, err := strconv.Atoi(args[0])
			if err != nil || version < -1 {
				return fmt.Errorf("force: %q is not a migration version (an integer, or -1 for none)", args[0])
			}
			cfg, err := config.LoadDatabaseConfig(cmd)
			if err != nil {
				return err
			}
			migrationURL := migrationURL(cfg)
			if err := db.Force(migrationURL, version); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "forced version %d: the dirty flag is cleared and no SQL was run; `%s up` applies the rest\n",
				version, cmd.Parent().CommandPath())
			return nil
		},
	}
	migrateCmd.AddCommand(forceCmd)

	// DATABASE_URL, DATABASE_DIRECT_URL, --db-driver: proto/reliant/config/v1
	// DatabaseConfig, the one the server roles load, persistent here so every
	// migrate command inherits the flag.
	config.RegisterDatabaseConfigFlags(migrateCmd)
	dbCmd.AddCommand(migrateCmd)
	dbCmd.AddCommand(newDBVaultCmd())
	return dbCmd
}

// migrationURL is the URL a `db migrate` command runs on: the direct
// (session) URL when one is set, else the URL. The migrator holds
// session-level locks, which a transaction-pooling PgBouncer cannot keep.
func migrationURL(cfg *configv1.DatabaseConfig) string {
	_, url, directURL := app.DatabaseURLs(cfg)
	return db.DatabaseConfig{URL: url, DirectURL: directURL}.MigrationURL()
}

// readOnlyPlan runs db.PlanMigration inside one READ ONLY, REPEATABLE READ
// transaction: one consistent snapshot, and any write refused by Postgres.
func readOnlyPlan(ctx context.Context, sqlDB *sql.DB) (db.MigrationPlan, error) {
	tx, err := sqlDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return db.MigrationPlan{}, fmt.Errorf("connect to database: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	return db.PlanMigration(ctx, tx)
}

// printPlan writes the plan, verdict first, and reports whether `up` would
// refuse.
func printPlan(out io.Writer, p db.MigrationPlan) (refuses bool) {
	verdict, refuses := p.Verdict()
	fmt.Fprintf(out, "verdict: %s\n", verdict)
	fmt.Fprintf(out, "forge:   schema_migrations version %s (dirty %t); this binary embeds %d migration(s), %s through %d\n",
		versionLabel(p.State.Applied, p.State.Version), p.State.Dirty, len(p.Embedded), p.Baseline.Name, p.Latest())
	return refuses
}

// versionLabel renders a schema version, or "none" for a database with no
// migration recorded.
func versionLabel(applied bool, version uint) string {
	if !applied {
		return "none"
	}
	return fmt.Sprint(version)
}
