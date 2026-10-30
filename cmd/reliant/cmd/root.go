// Copyright (c) 2025 Reliant Labs
//
// THE COMMAND TREE — its shape, and the shared surface every command consumes.
//
// This is forge's cmd/<bin>/cmd/root.go: forge finds the binary's command tree
// by this file, and its Deps, Factory, registerCommand and ServiceName seams
// are the ones forge's own command files are written against.
//
//   - Deps        — the dependency set threaded through registered commands.
//   - Factory     — the constructor shape registerCommand takes.
//   - ServiceName — the binary's app identity, and the root command's name.
//
// The server roles are roots of the composition graph (internal/app): each
// root's command comes from rootCommands (roots_gen.go, generated) and runs
// under the process policy in serve.go: `reliant server api`, `reliant server
// worker` and `reliant server gateway`, under the generated `server` group
// (its help is internal/app/roots.go's //forge:group).
// metrics.go and internal/metrics stay declined (.forge/scaffolded.json):
// they would add a `reliant metrics` command to a surface pinned by
// help_text_test and command_surface_test.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	forgecli "github.com/reliant-labs/forge/cli"

	"github.com/reliant-labs/reliant/internal/diskwatch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/runenv"
	"github.com/reliant-labs/reliant/pkg/config"
)

// verbose is the value of the persistent --verbose flag.
var verbose bool

// Deps is the dependency set threaded through the command tree: what a
// registered command needs to run, injected for testability.
type Deps struct {
	// Cmd is the cobra command currently executing. Set per-invocation in
	// each command's RunE.
	Cmd *cobra.Command
	// Out is the writer for user-facing output (defaults to os.Stdout).
	Out *os.File
	// Err is the writer for diagnostics (defaults to os.Stderr).
	Err *os.File
}

// Factory builds one subcommand from deps: the shape registerCommand takes.
type Factory func(deps Deps) *cobra.Command

// ServiceName is this binary's app identity: the root command's name, and the
// service name forge's telemetry reports.
const ServiceName = "reliant"

// registeredCommands holds the subcommands contributed by files that register
// themselves (registerCommand). newRootCmd adds each to the tree.
var registeredCommands []Factory

// registerCommand adds a subcommand constructor to the root tree. Call it from
// an init() in the file that defines the command:
//
//	func init() { registerCommand(newDBCmd) }
//
// Order is init order, which is stable but not meaningful: cobra sorts
// subcommands in help output regardless.
func registerCommand(f Factory) { registeredCommands = append(registeredCommands, f) }

// NewRootCmd creates and returns the root Cobra command with all subcommands registered.
// Exported so tools/docgen/cli can walk the command tree for documentation generation.
func NewRootCmd() *cobra.Command {
	return newRootCmd(Deps{Out: os.Stdout, Err: os.Stderr})
}

// newRootCmd assembles the full command tree from deps.
func newRootCmd(deps Deps) *cobra.Command {
	root := &cobra.Command{
		Use:   ServiceName,
		Short: "Reliant — AI-powered software engineering platform",
		Long: `Reliant is a multi-workflow, multi-workspace agentic assistant for
software engineering. This CLI provides commands for authentication,
project management, daemon control, workflow operations, and running
Reliant server components.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// Global persistent flags available to all subcommands. The target-selection
	// flags (--server/--gateway) are CLIConfig's, persistent on each end-user
	// command group (bindCLIConfig), and connection.go is the only place
	// their values are read.
	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output")
	enableVersionFlag(root)
	// The root config's leaves (proto/reliant/config/v1 ReliantConfig) are persistent
	// flags as well. A process that reads them loads the config in its own
	// RunE, so a command that reads none of them never fails on one.
	config.RegisterReliantConfigFlags(root)

	// Register subcommand groups. The end-user groups carry CLIConfig.
	daemonCmd, authCmd, projectCmd, workflowCmd, triggerCmd := newDaemonCmd(), newAuthCmd(), newProjectCmd(), newWorkflowCmd(), newTriggerCmd()
	bindCLIConfig(authCmd, daemonCmd, projectCmd, triggerCmd, workflowCmd)
	for _, c := range rootCommands(deps) {
		root.AddCommand(c)
	}
	root.AddCommand(daemonCmd)
	root.AddCommand(authCmd)
	root.AddCommand(projectCmd)
	root.AddCommand(workflowCmd)
	root.AddCommand(triggerCmd)
	root.AddCommand(newVersionCmd())
	root.AddCommand(newForgeCmd())
	root.AddCommand(newPreviewURLCmd())

	// Commands whose files register themselves (registerCommand): `db`.
	for _, f := range registeredCommands {
		root.AddCommand(f(deps))
	}

	return root
}

// Execute runs the root command, and exits the process with the failure's
// status when it fails, having printed the error to stderr. The status is
// forge's own resolution (forgecli.ExitCode): `reliant forge …` verbs print the
// same number as their --json document's exit_code, and any other error is 1.
//
// `reliant forge …` in a project pinned to another forge is first handed to
// that forge (handOffToPinnedForge); when it cannot be had, nothing runs.
func Execute() {
	root := NewRootCmd()
	if err := handOffToPinnedForge(root, os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(forgecli.ExitCode(err))
	}
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(forgecli.ExitCode(err))
	}
}

// loadRootConfig loads the reliant binary's root config (proto/reliant/config/v1
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
	out, warnings := diskwatch.ParseReaperConfig(sr.GetMode(), sr.GetInterval(), sr.GetPressureInterval(), sr.GetGocacheInterval(), sr.GetPolicyPath(), managed)
	for _, w := range warnings {
		logging.Warn("Storage reaper config: " + w)
	}
	return out
}
