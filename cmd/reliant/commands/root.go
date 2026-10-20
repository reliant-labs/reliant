// Copyright (c) 2025 Reliant Labs
package commands

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/diskwatch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/runenv"
	"github.com/reliant-labs/reliant/pkg/config"
)

// verbose is the value of the persistent --verbose flag.
var verbose bool

// NewRootCmd creates and returns the root Cobra command with all subcommands registered.
// Exported so tools/docgen/cli can walk the command tree for documentation generation.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "reliant",
		Short: "Reliant — AI-powered software engineering platform",
		Long: `Reliant is a multi-workflow, multi-workspace agentic assistant for
software engineering. This CLI provides commands for authentication,
project management, daemon control, workflow operations, and running
Reliant server components.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// Global persistent flags available to all subcommands. The target-selection
	// flags (--server/--gateway/--context) are owned by connection.go, which is
	// the only place their values are read.
	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output")
	registerConnectionFlags(root)
	enableVersionFlag(root)
	// The root config's leaves (proto/config/v1 ReliantConfig) are persistent
	// flags as well. A process that reads them loads the config in its own
	// RunE, so a command that reads none of them never fails on one.
	config.RegisterReliantConfigFlags(root)

	// Register subcommand groups
	root.AddCommand(newServerCmd())
	root.AddCommand(newDaemonCmd())
	root.AddCommand(newAuthCmd())
	root.AddCommand(newProjectCmd())
	root.AddCommand(newWorkflowCmd())
	root.AddCommand(newTriggerCmd())
	root.AddCommand(newVersionCmd())
	root.AddCommand(newForgeCmd())
	root.AddCommand(newPreviewURLCmd())
	root.AddCommand(newDBCmd())

	return root
}

// Execute runs the root command.
func Execute() error {
	root := NewRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	return nil
}

// loadRootConfig loads the reliant binary's root config (proto/config/v1
// ReliantConfig) for the process cmd runs. Every process that reads it — the
// servers and the daemon — calls this first thing in its RunE, before it
// builds anything from the config.
//
// It also records the process's environment tier in internal/runenv, which
// the log level, telemetry, analytics and feature flags all ask: they sit far
// below the entrypoint, and must agree, so the tier is set once here rather
// than threaded to each of them.
func loadRootConfig(cmd *cobra.Command) (*config.ReliantConfig, error) {
	cfg, err := config.LoadReliantConfig(cmd)
	if err != nil {
		return nil, err
	}
	runenv.Configure(cfg.GetRuntime().GetEnvironment())
	return cfg, nil
}

// logSettings is the root config's logger configuration, in the shape
// internal/logging takes.
func logSettings(cfg *config.ReliantConfig) logging.Settings {
	return logging.Settings{Level: cfg.GetLog().GetLevel(), Format: cfg.GetLog().GetFormat()}
}

// storageReaperConfig is the root config's schedule for the daemon's storage
// reaper (internal/diskwatch/reaper.go). A value it cannot use is named in a
// warning and replaced by its default; it never stops the daemon. managed is
// whether the platform declared this a managed daemon (DaemonConfig).
func storageReaperConfig(cfg *config.ReliantConfig, managed bool) diskwatch.ReaperConfig {
	sr := cfg.GetStorageReaper()
	out, warnings := diskwatch.ParseReaperConfig(sr.GetMode(), sr.GetInterval(), sr.GetPressureInterval(), sr.GetPolicyPath(), managed)
	for _, w := range warnings {
		logging.Warn("Storage reaper config: " + w)
	}
	return out
}
