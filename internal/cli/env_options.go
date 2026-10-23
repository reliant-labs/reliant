package cli

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/kcloptions"
)

// newEnvOptionsCmd is the discovery half of `forge env up -D`.
//
// It is a SUBCOMMAND rather than a section of `forge env up --help` because
// the options are per-env: forge cannot know which env's KCL to read until the
// env argument is parsed, and cobra parses flags and positionals together —
// so a `--help` listing would have to pre-scan os.Args before cobra runs. A
// command that takes the env as its own argument sidesteps that entirely, and
// gives agents a machine-readable surface (--json) at the same time.
func newEnvOptionsCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "options <env>",
		Short: "List the render options an environment's KCL declares",
		Args:  cobra.ExactArgs(1),
		Long: `List the ` + "`-D name=value`" + ` render options that deploy/kcl/<env>/ declares.

An env declares an option by READING it — the call site is the declaration,
so there is nothing to keep in sync:

    _host_runner = option("host_runner", type="str", default="air",
                          help="Host launch runner: air (default) or go-run")

    forge env up dev -D host_runner=go-run

forge discovers these by parsing the env's KCL with its kcl.mod dependencies
resolved. It reports the name, and whatever ` + "`type`" + ` / ` + "`default`" + ` / ` + "`help`" + ` the
declaration passed — those are optional, but an option declared bare shows up
here with nothing to explain it, which is worth fixing for whoever reads it
next.

Options forge derives and binds itself (env, namespace, image_tag,
image_digests, worktree, branch) are not listed: they are not yours to set.

Examples:
  forge env options dev
  forge env options dev --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			envName := args[0]
			projectDir := projectDirForKCL()

			declared, discoverable, err := kcloptions.Discover(projectDir, envName)
			if err != nil {
				return err
			}
			if !discoverable {
				// Distinct from "declares none": the parse reached nothing at
				// all, so saying "no options" would be a lie.
				return fmt.Errorf(
					"could not read the options in deploy/kcl/%s/ — its KCL or kcl.mod "+
						"dependencies did not resolve; `forge env up %s` will report the "+
						"underlying error", envName, envName)
			}

			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				// Encode an explicit empty slice, not nil: `[]` is a valid
				// answer for a project that declares none, `null` reads as
				// "unknown".
				out := make([]kcloptions.Option, 0, len(declared))
				out = append(out, declared...)
				return enc.Encode(out)
			}

			if len(declared) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(),
					"deploy/kcl/%s/ declares no render options.\n", envName)
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "OPTION\tTYPE\tDEFAULT\tDESCRIPTION")
			for _, o := range declared {
				help := o.Help
				if o.Required {
					help = "(required) " + help
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", o.Name, dash(o.Type), dash(o.Default), dash(help))
			}
			return w.Flush()
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit machine-readable JSON (name/type/default/help/required)")
	return cmd
}

// dash renders an absent field as "-" so a bare `option("x")` reads as
// deliberately empty rather than as a broken column.
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
