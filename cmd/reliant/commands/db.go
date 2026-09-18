// Copyright (c) 2025 Reliant Labs
package commands

import (
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/db"
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
// of every workload that waits on the schema. It is the SAME goose run over
// the SAME embedded migrations the api-server would do, so the single-migrator
// rule is preserved — the owner moved, it did not multiply. Do not run it
// concurrently with a live api-server for the same database.
func newDBCmd() *cobra.Command {
	dbCmd := &cobra.Command{
		Use:   "db",
		Short: "Database schema commands",
		Long: `Apply or inspect the migrations embedded in this binary.

DATABASE_URL (required) and DATABASE_DRIVER are read from the environment —
the same variables the servers read, so a migration Job and the api-server
cannot disagree about which database they mean.`,
	}

	migrateCmd := &cobra.Command{
		Use:   "migrate",
		Short: "Manage database migrations",
	}

	migrateCmd.AddCommand(&cobra.Command{
		Use:   "up",
		Short: "Apply every pending migration",
		Long: `Apply every migration this binary embeds that the database has not
recorded applied, then exit.

Exits 0 when the schema is already current, so it is safe to run on every
deploy and safe to re-run after a failure.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := db.ResolveDatabaseConfig("")
			if err != nil {
				return err
			}

			// MigrateApply is what ConnectWithConfig does by default: open,
			// then run goose to completion. Reusing it rather than calling
			// RunMigrations against a hand-rolled connection keeps this on the
			// identical code path the api-server takes.
			cfg.Migrate = db.MigrateApply

			sqlDB, err := db.ConnectWithConfig(cfg)
			if err != nil {
				return err
			}
			defer func() { _ = sqlDB.Close() }()

			pending, err := db.PendingMigrations(sqlDB)
			if err != nil {
				return fmt.Errorf("verify schema after migrate: %w", err)
			}
			// Belt-and-braces: goose reported success, so a non-empty set here
			// means the migrations it applied are not the ones this binary
			// embeds — a mismatch worth failing the Job over, because every
			// waiter is about to block on exactly this set.
			if len(pending) > 0 {
				return fmt.Errorf("migrate up reported success but %d migration(s) are still unapplied (oldest: %d)", len(pending), pending[0])
			}

			fmt.Fprintln(cmd.OutOrStdout(), "schema is current")
			return nil
		},
	})

	migrateCmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Print which embedded migrations the database is missing",
		Long: `Report how many of this binary's embedded migrations the database has
applied, and list any it has not.

Opens the database WITHOUT migrating and WITHOUT waiting, so it stays usable
to diagnose the very deadlock the wait produces.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := db.ResolveDatabaseConfig("")
			if err != nil {
				return err
			}

			// Deliberately NOT ConnectWithConfig: both of its policies act on
			// the schema (apply or block until current), and a status command
			// that blocked would be useless exactly when it is needed.
			sqlDB, err := sql.Open("pgx", cfg.URL)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer func() { _ = sqlDB.Close() }()

			if err := sqlDB.Ping(); err != nil {
				return fmt.Errorf("connect to database: %w", err)
			}

			embedded, err := db.EmbeddedMigrationVersions()
			if err != nil {
				return err
			}
			pending, err := db.PendingMigrations(sqlDB)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "applied %d of %d embedded migrations\n", len(embedded)-len(pending), len(embedded))
			if len(pending) == 0 {
				return nil
			}
			fmt.Fprintf(out, "pending:\n")
			for _, v := range pending {
				fmt.Fprintf(out, "  %d\n", v)
			}
			return nil
		},
	})

	dbCmd.AddCommand(migrateCmd)
	return dbCmd
}
