---
name: commands
description: Admin, report and backfill subcommands in a package independent of the server graph, including moving existing commands and registering only their own config flags.
---

# Commands of the project binary

A short-lived admin task, report or backfill (`<bin> apikey create`) can be a subcommand of the project binary rather than a second binary. Put it in `cmd/<bin>/usercmd/` and return it from `usercmd.Commands()`. The assembled `cmd/<bin>/cmd` package imports `usercmd` and adds the returned commands to the tree. Test these commands with `go test ./cmd/<bin>/usercmd` even if the composition graph does not compile; building the full binary still requires the graph. Keep `usercmd` independent of `cmd` and `internal/app`: import just the clients or stores you use, inject their narrow interfaces as needed, and take context and output from Cobra.

## Move an existing command

Forge keeps your existing `cmd/<bin>/cmd/commands.go` and its `userCommands(deps)` hook intact. To separate CLI-only commands, create the sibling `usercmd` package, move the constructors and their tests there, and export `Commands() []*cobra.Command`. Remove any dependency on `cmd.Deps`: use `cmd.OutOrStdout()` and `cmd.ErrOrStderr()` on the running Cobra command, or define your own narrow dependencies in `usercmd`. In `cmd/commands.go`, import `usercmd` and return `usercmd.Commands()` from `userCommands`; if other commands still live in `cmd`, append them there. Do not edit `roots_gen.go`.

## Config flags

**Register only the flags the command takes.** `config.RegisterFlags(cmd)` is the server's whole surface — every CORS, OIDC, JWT, TLS and pprof flag — and on an admin command it buries the command's own flags in `--help` while doing nothing. Loading needs no registration: `config.Load(cmd)` reads the env and the config file whether or not a field has a flag. So register the command's own flags with cobra, and the config fields it wants settable on its command line with `config.RegisterFieldFlags`:

```go
func newReportCmd() *cobra.Command {
	var since time.Duration
	c := &cobra.Command{
		Use:   "report",
		Short: "Print the weekly usage report",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cmd) // DATABASE_URL and the rest from env / --config
			if err != nil {
				return err
			}
			return printReport(cmd.Context(), cmd.OutOrStdout(), cfg, since)
		},
	}
	c.Flags().DurationVar(&since, "since", 7*24*time.Hour, "how far back to report")
	config.RegisterFieldFlags(c, "log_level") // --config and --log-level, nothing else
	return c
}
```

`RegisterFieldFlags` takes field paths as `RequiredWithin` does (`"log_level"`, `"<block>.<field>"`; a block path registers its leaves), always registers `--config`, and panics on a path naming no flagged field or a sensitive one, so a typo fails the first time the command is built. With no paths it registers `--config` alone. A command that loads a subset of a large config and demands only its own required fields loads with `config.LoadReach(cmd, []string{"database_url"})`.

The generated root commands (`server`, and each `//forge:root`) register the full set, because they are the processes that read it; `db migrate` registers none, because its only setting is `DATABASE_URL`.
