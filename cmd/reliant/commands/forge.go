// Copyright (c) 2025 Reliant Labs
package commands

import (
	"github.com/spf13/cobra"

	forgecli "github.com/reliant-labs/forge/cli"
)

func newForgeCmd() *cobra.Command {
	root := forgecli.NewRootCmd()
	// `reliant forge …` is signed in wherever Reliant is: point forge's
	// credential helper at this binary before any forge command runs. Done
	// in the hook, not here — this constructor runs for EVERY reliant
	// invocation, and a daemon must export its own pinned helper, not
	// inherit an unpinned one from building the command tree.
	inner := root.PersistentPreRunE
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		useReliantSessionForForge()
		if inner != nil {
			return inner(cmd, args)
		}
		return nil
	}
	return root
}
