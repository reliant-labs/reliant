// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// THE COMMAND TREE — its shape, and every command on it.
//
// This is the file to edit when you want a different CLI. Add a persistent
// flag, reorder the tree, give the root command a Long description, wrap
// RunE — it is ordinary cobra code and forge will not revert it.
//
// The command logic lives under cmd/app/cmd as a real cobra package,
// dir-nested by category (the devspace idiom). This file owns the SHARED
// surface every command group consumes:
//
//   - Deps        — the dependency set threaded through the tree. Add a field
//     here (a metrics sink, an audit writer) and every command can take it.
//   - Factory     — the constructor shape every group command exports
//     (func(Deps) *cobra.Command).
//
// EVERY FILE IN THIS PACKAGE IS YOURS. There is no generated file in the
// command tree: ServiceName is a constant below (the project name, fixed at
// scaffold), and the one genuinely derived migration fact — whether this
// project has an embedded migration set yet — lives in the db package it is a
// fact about, as db.Source(). That function compiles whether or not you have
// written a migration, so writing your first one rewrites nothing here.
//
// The COMPONENT tree is assembled EXPLICITLY in the composition root
// (cmd/app/main.go): main names every service/worker/operator group
// constructor and passes them to Execute, which forwards them to newRootCmd.
// No group self-registers, so the whole component tree is readable in main.go.
// Because the groups only depend on THIS package for Deps/Serve (and main.go —
// not this package — imports the groups), there is no cmd↔group import cycle.
//
// The one exception is registeredCommands below, for a command whose FILE is
// conditional (db.go, scaffolded only with migrations). Explicit naming cannot
// express "call this only if that file exists", so that single case registers
// itself. See registerCommand for why the alternative is worse.
//
// The command tree:
//
//   - cmd/app/main.go        — composition root: cmd.Execute(<every group ctor>).
//   - cmd/app/cmd/root.go    — this file (root + Deps + Factory + ServiceName).
//   - cmd/app/cmd/serve.go   — the shared serve() pipeline (typed mount).
//   - cmd/app/cmd/server.go  — `app server` (every service).
//   - cmd/app/cmd/version.go — `app version`.
//   - cmd/app/cmd/db.go      — `app db migrate ...` (if migrations).
//   - cmd/app/cmd/commands.go — userCommands(deps): YOUR extension point.
//   - cmd/app/cmd/services/<svc>.go — ONE FILE PER SERVICE.
//   - cmd/app/cmd/workers/<w>.go    — ONE FILE PER WORKER.
//   - cmd/app/cmd/operators/<o>.go  — ONE FILE PER OPERATOR.
package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Deps is the dependency set threaded through the command tree (the gh
// pattern): everything a command needs to run, injected for testability.
// Commands read deps.Cmd for config flags (config.Load) and deps.Out for
// user-facing output. Exported so the group SUBPACKAGES take it.
type Deps struct {
	// Cmd is the cobra command currently executing — config.Load reads its
	// flag set. Set per-invocation in each command's RunE.
	Cmd *cobra.Command
	// Out is the writer for user-facing output (defaults to os.Stdout).
	Out *os.File
	// Err is the writer for diagnostics (defaults to os.Stderr).
	Err *os.File
}

// Factory builds one subcommand from deps. Every group command (services/,
// workers/, operators/) exports a constructor of this shape; main.go names them
// explicitly and hands them to Execute.
type Factory func(deps Deps) *cobra.Command

// ServiceName is this binary's APP IDENTITY — the logical service name reported
// on OpenTelemetry traces/metrics (semconv service.name), and the root
// command's own name.
//
// It is a constant here rather than a config.Config field because identity is a
// build-time property of the binary, not a per-env knob. It was briefly a
// generated constant so that renaming the project could move it; that bought a
// whole regenerated file for a string that is written once at scaffold and then
// only ever changed by a rename you are performing by hand anyway — at which
// point editing this line is the smaller half of the job.
const ServiceName = "app"

// registeredCommands holds subcommands contributed by files that may or may
// not exist in this package. newRootCmd adds each to the tree.
//
// WHY A REGISTRY AND NOT A CALL. Naming newDBCmd(deps) directly in newRootCmd
// would not compile for a project scaffolded without migrations, because db.go
// is not written for such a project. Writing it under a build-time conditional
// is worse: this file is scaffold-once, so the branch taken on the day the
// project was born would be frozen with no later run able to correct it.
// Registration inverts that — the file that DEFINES the command is the file
// that adds it, so the set is exactly the set of files on disk, and a project
// gains or loses `db migrate` by gaining or losing db.go.
//
// This is the ONE registry in the command tree, and it is deliberately not the
// pattern for your own commands: the service/worker/operator groups are named
// explicitly in main.go so the tree is readable there, and your own additions
// go in commands.go's userCommands. Use this only for a command whose FILE is
// conditional.
var registeredCommands []Factory

// registerCommand adds a subcommand constructor to the root tree. Call it from
// an init() in the file that defines the command:
//
//	func init() { registerCommand(newDBCmd) }
//
// Order is init order (file name, within a package), which is stable but not
// meaningful — cobra sorts subcommands in help output regardless.
func registerCommand(f Factory) { registeredCommands = append(registeredCommands, f) }

// newRootCmd assembles the full command tree from deps plus the explicit set of
// group-command factories main.go passes in. Kept separate from Execute so
// tests can drive the tree without going through os.Exit. There is no registry:
// the group commands are exactly the ones main.go named.
func newRootCmd(deps Deps, groupCmds ...Factory) *cobra.Command {
	root := &cobra.Command{
		Use:   ServiceName,
		Short: ServiceName + " service",
	}

	root.AddCommand(newServerCmd(deps))
	root.AddCommand(newVersionCmd(deps))

	// Commands contributed by an OPTIONAL file in this package — today that is
	// `db migrate`, from the db.go that is scaffolded only for a project
	// configured for migrations. See registerCommand below.
	for _, f := range registeredCommands {
		root.AddCommand(f(deps))
	}

	for _, f := range groupCmds {
		root.AddCommand(f(deps))
	}

	// userCommands (cmd/app/cmd/commands.go, user-owned): second
	// binaries and custom tooling register as code.
	for _, c := range userCommands(deps) {
		root.AddCommand(c)
	}
	return root
}

// Execute is the program entry point cmd/app/main.go calls. main.go passes
// every service/worker/operator constructor explicitly (the tree is assembled
// in the composition root, not a dynamic registry).
func Execute(groupCmds ...Factory) {
	deps := Deps{Out: os.Stdout, Err: os.Stderr}
	root := newRootCmd(deps, groupCmds...)
	if err := root.ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
