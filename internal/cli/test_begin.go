package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/testcapture"
)

// newTestBeginCmd is `forge test begin`: the run directory a Taskfile's lanes
// record into. It runs in a Taskfile's `vars: sh:`, in front of every lane, so
// it is quick (a handful of git reads) and it never fails: a run it cannot
// record still prints a directory the lanes can write into.
func newTestBeginCmd() *cobra.Command {
	var base string
	cmd := &cobra.Command{
		Use:   "begin",
		Short: "Print a run directory for the project's test lanes to record into (plumbing)",
		Long: `Allocates a run directory, .forge/tests/runs/<id>/, writes its run.json
(forge.test.run/v1: commit, tree hash, dirty flag, branch, base, CI run, forge
version, project) and prints the directory's absolute path.

With FORGE_TEST_RUN set, prints that directory instead, so several lanes join
one run; a FORGE_TEST_RUN directory with no run.json yet becomes the run.

begin never fails a lane. What it cannot record it warns about on stderr, and
when it cannot create a run at all (or FORGE_TESTS=off) it prints a scratch
directory, so a lane told to write its coverage there still runs.`,
		Example: `  # Taskfile.yml
  vars:
    RUN: { sh: "forge test begin" }`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, _ := cmdutil.FindProjectRoot()
			dir, _ := cmdutil.ResolutionRoot()
			res := testcapture.Begin(cmd.Context(), testcapture.BeginOptions{
				Dir:          dir,
				ProjectRoot:  root,
				Base:         base,
				ForgeVersion: buildinfo.Version(),
			})
			for _, w := range res.Warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "forge test begin: warning: %s\n", w)
			}
			fmt.Fprintln(cmd.OutOrStdout(), res.Dir)
			return nil
		},
	}
	cmd.Flags().StringVar(&base, "base", "", "Revision the run's change is measured against (default: the CI's pull request or merge group base, when there is one)")
	return cmd
}
