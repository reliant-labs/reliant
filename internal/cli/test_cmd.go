package cli

import (
	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
)

// `forge test` is the test-record noun. It is NOT the `forge test` that was
// removed (ci_test_run.go says why): that one ran a suite of its own, a second
// spelling of the project's tests that reported on a different suite than the
// one the project runs. Nothing here runs a suite. `begin` and `capture` wrap
// the project's own lane commands, exactly as its Taskfile spells them, and
// record what they produced; everything else in the group reads those records.
func newTestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Record the project's own test lanes (never runs a test suite of its own)",
		Long: `Records the project's own test lanes; forge never runs a test suite of its own.

The project's Taskfile decides how its tests run, with flags anyone can read.
forge wraps those commands unchanged and records what they produced: results,
coverage and the run's commit, tree and CI identity, under
.forge/tests/runs/<id>/.

  RUN=$(forge test begin)
  forge test capture --run "$RUN" --lane unit -- \
    go test -json -coverprofile="$RUN/unit/cover.out" ./...

` + "`capture`" + ` exits with the command's own exit status, always: a record that
cannot be written is a warning, never a different verdict. FORGE_TESTS=off
turns recording off for one invocation.`,
	}
	cmd.AddCommand(newTestBeginCmd(), newTestCaptureCmd())
	cmd.AddCommand(newTestReportCmd(), newTestCoverageCmd(), newTestRunsCmd(), newTestIngestCmd())
	cmd.AddCommand(newTestCollectCmd())
	cmd.AddCommand(newTestRunCmd())
	cmd.AddCommand(newTestFlakesCmd())
	cmd.AddCommand(newTestQuarantineCmd())
	return cmdutil.StrictGroup(cmd)
}
