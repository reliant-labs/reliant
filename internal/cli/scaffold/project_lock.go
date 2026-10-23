package scaffold

import (
	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/factory"
)

// projectLockAnnotation marks a scaffold command whose RunE runs holding the
// project's generate lock. TestScaffoldCommandsHoldProjectLock walks the
// whole tree for it, so a noun added later cannot skip the lock unnoticed.
const projectLockAnnotation = "forge.scaffold/project-lock"

// holdProjectLock wraps cmd and every command beneath it so each RunE runs
// holding the project's generate lock (f.Gen.HoldProjectLock), released when
// the command returns.
//
// Scaffold writes the inputs generate reads — protos, migrations, forge.yaml,
// cmd/<bin>/main.go, the scaffold ledger — and then runs the generate
// pipeline over them. Locking only that pipeline run, as forge once did, let
// another agent's generate read a half-scaffolded tree, and let that run's
// revert-on-failure restore files this scaffold had just written. Holding the
// lock for the whole command makes "scaffold, then generate" one unit that a
// concurrent run waits for instead of tearing.
//
// Outside a project (no forge.yaml at the resolution root) there is nothing to
// lock, so the command runs bare and reports the missing project itself.
func holdProjectLock(cmd *cobra.Command, f *factory.Factory) {
	if run := cmd.RunE; run != nil {
		cmd.RunE = func(c *cobra.Command, args []string) error {
			root, err := projectRoot()
			if err != nil {
				return run(c, args)
			}
			release, err := f.Gen.HoldProjectLock(root)
			if err != nil {
				return err
			}
			defer release()
			return run(c, args)
		}
		if cmd.Annotations == nil {
			cmd.Annotations = map[string]string{}
		}
		cmd.Annotations[projectLockAnnotation] = "held"
	}
	for _, sub := range cmd.Commands() {
		holdProjectLock(sub, f)
	}
}
