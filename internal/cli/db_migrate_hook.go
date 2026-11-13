package cli

// `forge db` under the migrations hook (forge.yaml `database.migrations.tool`).
//
// A project that keeps goose, atlas, flyway, dbmate, golang-migrate or a
// command of its own gets the verbs that mean the same thing whatever applies
// the migrations — `migrate up` and `migrate status` (and `version`, its
// script-facing twin) run the project's tool through internal/migrationhook,
// and `reset` re-migrates with it — and a refusal naming the tool's own
// equivalent for the verbs that act on forge's migrator's state or file
// shape: `migrate force`, `migration new`, `migration rebase`, `squash`.
// Running them would write forge-shaped files into another tool's directory,
// or repair a ledger (schema_migrations_applied) that tool never wrote.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/migrationhook"
)

// projectMigrationHook returns the migration hook of the project the db
// commands act on, with its directory set to migDir (the resolved --dir, or
// the configured one), and false when forge's own migrator applies the
// project's migrations.
func projectMigrationHook(migDir string) (migrationhook.Hook, bool) {
	h, ok := migrationhook.ResolveIn(dbProjectRoot())
	if !ok {
		return h, false
	}
	if migDir != "" {
		if abs, err := filepath.Abs(migDir); err == nil {
			h.Dir = abs
			if rel, rerr := filepath.Rel(h.ProjectDir, abs); rerr == nil {
				h.RelDir = filepath.ToSlash(rel)
			}
		}
	}
	return h, true
}

// runHookMigrate runs the hook's apply (status false) or status argv against
// dsn, streaming the tool's output.
func runHookMigrate(w io.Writer, h migrationhook.Hook, dsn string, status bool) error {
	inv := migrationhook.Invocation{DSN: dsn, Phase: migrationhook.PhaseCLI, Stdout: w, Stderr: os.Stderr}
	if status {
		_, _ = fmt.Fprintf(w, "Migration status from %s (forge.yaml database.migrations.tool):\n", h.Tool)
		return migrationhook.Status(context.Background(), h, inv)
	}
	_, _ = fmt.Fprintf(w, "Applying %s with %s (forge.yaml database.migrations.tool)...\n", h.RelDir, h.Tool)
	if err := migrationhook.Apply(context.Background(), h, inv); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "✅ %s applied the migrations\n", h.Tool)
	return nil
}

// refuseUnderMigrationHook is the error for a db verb that only exists for
// forge's own migrator.
func refuseUnderMigrationHook(verb string, h migrationhook.Hook, instead string) error {
	return fmt.Errorf("`forge db %s` acts on forge's own migrator, but %s applies this project's migrations (forge.yaml database.migrations.tool) — %s",
		verb, h.Tool, instead)
}
