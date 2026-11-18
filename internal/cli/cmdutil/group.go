package cmdutil

import (
	"fmt"

	"github.com/spf13/cobra"
)

// StrictGroup makes a command-GROUP (a command that exists only to host
// subcommands) reject an unrecognised subcommand instead of reporting
// success.
//
// Cobra's default for a non-runnable parent is to print help and exit 0 for
// ANY leftover argument: `forge db bogus`, `forge env bogus`, a renamed verb
// typed with its old spelling — all printed a help page and exited 0. For a
// human that reads as "nothing happened"; for a scripted agent it reads as
// "the command succeeded", and the run continues on top of work that was
// never done. That is the same defect as a success message that proves
// nothing, just spelled with an exit code.
//
// StrictGroup gives the group an Args validator (which makes Runnable() true,
// so cobra actually validates the leftover args) and a RunE that prints help
// for the bare form. Bare `forge db` still prints help and exits 0 — the user
// asked for the menu and got it. `forge db bogus` now exits non-zero naming
// the offending token, plus a "did you mean" line when one of the group's own
// subcommands is close.
//
// Every group in the tree must go through here; TestEveryCommandGroupRejects
// UnknownSubcommands (internal/cli) walks the assembled tree and fails on any
// parent command that still carries cobra's permissive default.
func StrictGroup(cmd *cobra.Command) *cobra.Command {
	cmd.Args = func(c *cobra.Command, args []string) error {
		if len(args) == 0 {
			return nil
		}
		return fmt.Errorf("unknown command %q for %q%s", args[0], c.CommandPath(), suggestFor(c, args[0]))
	}
	cmd.RunE = func(c *cobra.Command, _ []string) error { return c.Help() }
	return cmd
}

// suggestFor mirrors cobra's own (unexported) findSuggestions so a strict
// group's error reads exactly like the root command's, whose unknown-command
// path cobra handles itself.
func suggestFor(cmd *cobra.Command, typed string) string {
	if cmd.DisableSuggestions {
		return ""
	}
	suggestions := cmd.SuggestionsFor(typed)
	if len(suggestions) == 0 {
		return ""
	}
	out := "\n\nDid you mean this?\n"
	for _, s := range suggestions {
		out += fmt.Sprintf("\t%v\n", s)
	}
	return out
}
