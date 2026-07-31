// Copyright (c) 2025 Reliant Labs
//
// reliant-dev is the internal forensics CLI for hardening runs.
//
// These commands read the Reliant database directly, so they are not part of
// the `reliant` CLI: a user has no database to point them at. Everything here
// is read-only — every access is a SELECT and no migrations are run.
//
// Build: go build -o bin/reliant-dev ./tools/reliant-dev
// Run:   reliant-dev workflow ps
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "reliant-dev",
		Short: "Reliant internal forensics CLI (reads the database directly)",
		Long: `reliant-dev answers the mechanical questions of a hardening run by reading the
Reliant database directly.

It is a development tool, not part of the shipped ` + "`reliant`" + ` CLI: every command
here opens the database, which a user does not have. Read-only throughout.

Point it at a database with --db-url, or DATABASE_URL, or let it default to the
local dev stack.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(newWorkflowCmd())

	return root
}

// newWorkflowCmd keeps the `workflow <verb>` grouping the commands had in the
// reliant CLI, so `reliant workflow ps` is now `reliant-dev workflow ps`.
func newWorkflowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflow",
		Short: "Inspect workflow runs from the database",
	}

	cmd.AddCommand(newWorkflowPsCmd())
	cmd.AddCommand(newWorkflowNodeCmd())
	cmd.AddCommand(newWorkflowAnalyzeCmd())
	cmd.AddCommand(newWorkflowForensicsCmd())

	return cmd
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
