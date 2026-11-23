package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/testcapture"
	"github.com/reliant-labs/forge/internal/testlocal"
)

// newTestIngestCmd is `forge test ingest`: reports from a runner forge does
// not wrap, copied into a lane of a run.
func newTestIngestCmd() *cobra.Command {
	var (
		lane, format, runDir string
	)
	cmd := &cobra.Command{
		Use:   "ingest --lane <name> --format <format> <file>...",
		Short: "Copy reports from a runner forge does not wrap (pytest JUnit, jest LCOV, …) into a recorded run",
		Long: `Copies test reports a runner wrote on its own into a lane of a run, so
` + "`forge test report`" + ` reads them beside the lanes ` + "`forge test capture`" + ` recorded.

Formats: results (junit, go-json, vitest) and coverage (lcov, cobertura,
istanbul). A lane takes one results file (several go-json streams are joined)
and any number of coverage files; ingest results and coverage into the same
lane with two calls. Results are secret-scrubbed as capture scrubs a stream.

The run is --run, else $FORGE_TEST_RUN, else a new run of the current
checkout (what ` + "`forge test begin`" + ` creates). The run directory is printed.
An ingested lane has no exit code of its own: its verdict is its results.`,
		Example: `  forge test ingest --lane pytest --format junit report.xml
  forge test ingest --lane pytest --format cobertura coverage.xml --run "$RUN"
  forge test ingest --lane web --format lcov frontends/web/coverage/lcov.info`,
		Args:         cobra.MinimumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, files []string) error {
			if lane == "" || format == "" {
				return fmt.Errorf("--lane and --format are required (--format: %s)", strings.Join(testlocal.IngestFormats, ", "))
			}
			dir := runDir
			if dir == "" {
				if testcapture.Off(osGetenv) {
					return fmt.Errorf("%s=off: recording is turned off, so there is no run to ingest into (pass --run)", testcapture.EnvTests)
				}
				root, _ := cmdutil.FindProjectRoot()
				wd, _ := cmdutil.ResolutionRoot()
				res := testcapture.Begin(cmd.Context(), testcapture.BeginOptions{Dir: wd, ProjectRoot: root, ForgeVersion: buildinfo.Version()})
				for _, w := range res.Warnings {
					fmt.Fprintf(cmd.ErrOrStderr(), "forge test ingest: warning: %s\n", w)
				}
				if !res.Recording {
					return fmt.Errorf("no run could be created to ingest into (pass --run)")
				}
				dir = res.Dir
			}
			r, err := testlocal.Ingest(testlocal.IngestOptions{RunDir: dir, Lane: lane, Format: format, Files: files})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "forge test ingest: lane %s (%s) recorded in %s\n", lane, format, r.LaneDir)
			fmt.Fprintln(cmd.OutOrStdout(), dir)
			return nil
		},
	}
	cmd.Flags().StringVar(&lane, "lane", "", "Lane to record the reports under (letters, digits, '-' and '_')")
	cmd.Flags().StringVar(&format, "format", "", "Report format: "+strings.Join(testlocal.IngestFormats, ", "))
	cmd.Flags().StringVar(&runDir, "run", "", "Run directory to add the lane to (default: $FORGE_TEST_RUN, else a new run)")
	return cmd
}
