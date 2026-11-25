package cli

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestResolveDSN(t *testing.T) {
	t.Setenv("DATABASE_URL", "")

	if _, err := resolveDSN(""); err == nil {
		t.Fatal("expected error when neither flag nor env var set")
	}

	if got, err := resolveDSN("flag-dsn"); err != nil || got != "flag-dsn" {
		t.Fatalf("flag DSN: got (%q, %v), want (\"flag-dsn\", nil)", got, err)
	}

	t.Setenv("DATABASE_URL", "env-dsn")
	if got, err := resolveDSN(""); err != nil || got != "env-dsn" {
		t.Fatalf("env DSN: got (%q, %v), want (\"env-dsn\", nil)", got, err)
	}

	// Flag wins over env.
	if got, err := resolveDSN("flag-dsn"); err != nil || got != "flag-dsn" {
		t.Fatalf("flag-over-env: got (%q, %v), want (\"flag-dsn\", nil)", got, err)
	}
}

func TestDBCommandIncludesExpectedTopLevelSubcommands(t *testing.T) {
	command := newDBCmd()

	if subcommand := command.Commands()[0]; subcommand == nil {
		t.Fatal("expected db command to have subcommands")
	}

	// Add to a root so CommandPath works correctly
	root := &cobra.Command{Use: "forge"}
	root.AddCommand(command)

	if command.CommandPath() != "forge db" {
		t.Fatalf("db command path = %q, want %q", command.CommandPath(), "forge db")
	}

	for _, subcommand := range []string{"migration", "migrate", "introspect", "squash"} {
		if got := commandName(command, subcommand); got == nil {
			t.Fatalf("expected db command to include %q subcommand", subcommand)
		}
	}

	// Retired with the entity-proto subsystem: SQL is the schema; entities
	// are projections of db/migrations, so the proto-sync and ORM-codegen
	// command trees are gone.
	for _, retired := range []string{"proto", "codegen"} {
		if got := commandName(command, retired); got != nil {
			t.Fatalf("db %q subcommand was removed and must not be registered", retired)
		}
	}
}

func TestDBMigrationNewCommandRequiresName(t *testing.T) {
	dbCmd := newDBCmd()
	migrationCmd := commandName(dbCmd, "migration")
	if migrationCmd == nil {
		t.Fatal("migration command not found")
	}

	newCmd := commandName(migrationCmd, "new")
	if newCmd == nil {
		t.Fatal("migration new command not found")
	}

	if err := newCmd.Args(newCmd, nil); err == nil {
		t.Fatal("expected error when migration name is missing")
	}
}

func TestDBMigrateCommandIncludesExpectedLifecycleCommands(t *testing.T) {
	dbCmd := newDBCmd()
	migrateCmd := commandName(dbCmd, "migrate")
	if migrateCmd == nil {
		t.Fatal("migrate command not found")
	}

	for _, subcommand := range []string{"up", "status", "version", "force"} {
		if got := commandName(migrateCmd, subcommand); got == nil {
			t.Fatalf("expected migrate command to include %q", subcommand)
		}
	}
	// Roll forward only: there is no command that runs down SQL.
	for _, forbidden := range []string{"down", "rollback", "redo"} {
		if got := commandName(migrateCmd, forbidden); got != nil {
			t.Fatalf("`forge db migrate %s` must not exist — forge never runs down migrations", forbidden)
		}
	}
}

func commandName(parent *cobra.Command, use string) *cobra.Command {
	for _, command := range parent.Commands() {
		if command.Name() == use {
			return command
		}
	}
	return nil
}
