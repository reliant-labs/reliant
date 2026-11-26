---
name: commands
description: Admin, report and backfill subcommands of the project binary — where they go (commands.go's userCommands), how they load the typed config, and config.RegisterFieldFlags so their --help lists their own flags instead of the server's ~60.
---

# Commands of the project binary

A command that is not a process of its own — an admin task, a report, a backfill an operator runs by hand (`<bin> apikey create`) — is a subcommand of the project binary, not a second binary. It goes in the owned `cmd/<bin>/cmd/commands.go`, whose `userCommands(deps)` root.go adds to the tree.

**Register only the flags the command takes.** `config.RegisterFlags(cmd)` is the server's whole surface — every CORS, OIDC, JWT, TLS and pprof flag — and on an admin command it buries the command's own flags in `--help` while doing nothing. Loading needs no registration: `config.Load(cmd)` reads the env and the config file whether or not a field has a flag. So register the command's own flags with cobra, and the config fields it wants settable on its command line with `config.RegisterFieldFlags`:

```go
func newReportCmd(_ Deps) *cobra.Command {
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
