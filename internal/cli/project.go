// Package cli — `forge project`: the project-structure noun.
package cli

import (
	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
)

// newProjectCmd groups the commands that act on the PROJECT AS A WHOLE —
// create it (`new`), retire part of it (`delete`, `disown`), move it across
// forge versions (`migrate`, `upgrade`), or INSPECT it (`map`, `graph`,
// `introspect`, `features`, `annotations`, plus `audit` from the factory
// registry). That is as opposed to acting on a deploy environment
// (`forge env ...`) or being an everyday dev verb (`forge build/test/lint`).
//
// Scaffolding deliberately does NOT live here: writing new code into a
// project is `forge scaffold` (bare = everything the protos imply, with a
// noun = one thing). `project` carried no information for those verbs —
// every forge command operates on a project.
func newProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Create, evolve, and inspect the project as a whole",
	}
	cmd.AddCommand(newNewCmd())
	cmd.AddCommand(newDeleteCmd())
	cmd.AddCommand(newDisownCmd())
	cmd.AddCommand(newRescaffoldCmd())
	cmd.AddCommand(newScaffoldedCmd())
	cmd.AddCommand(newMigrateCmd())
	cmd.AddCommand(newUpgradeCmd())
	cmd.AddCommand(newMapCmd())
	cmd.AddCommand(newShapesCmd())
	cmd.AddCommand(newGraphCmd())
	cmd.AddCommand(newIntrospectCmd())
	cmd.AddCommand(newFeaturesCmd())
	cmd.AddCommand(newAnnotationsCmd())
	cmd.AddCommand(newCapabilitiesCmd())
	cmd.AddCommand(newLibrariesCmd())
	cmd.AddCommand(newProjectFrontendsCmd())
	// `checkouts` is a project-STRUCTURE read: which copies of this
	// project exist on this machine, plus the remote main they are
	// measured against. It belongs on the project noun rather than under
	// `env`, because a checkout is not scoped to an environment — the
	// picker chooses a checkout first and the envs come from rendering it.
	cmd.AddCommand(newProjectCheckoutsCmd())
	cmd.AddCommand(newProjectMemoryCmd())
	// cmdutil.StrictGroup, not a bare group: cobra's default for a
	// non-runnable parent is to print help and exit 0 for ANY unrecognised
	// subcommand, which reports SUCCESS while doing nothing. Every group in
	// the tree goes through the same helper.
	return cmdutil.StrictGroup(cmd)
}
