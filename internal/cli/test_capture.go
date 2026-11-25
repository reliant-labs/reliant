package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/testcapture"
)

// newTestCaptureCmd is `forge test capture`: run one lane's command unchanged
// and record it. Its contract is the exit status — the command's own, always —
// because it wraps every lane a project gates on: a capture that turned a red
// lane green, or a green one red, would be worse than no record.
func newTestCaptureCmd() *cobra.Command {
	var (
		run, lane, results, format, render string
	)
	cmd := &cobra.Command{
		Use:   "capture --run <dir> --lane <name> [--results <file>] -- <command> [args...]",
		Short: "Run a test lane's command unchanged and record its results (plumbing)",
		Long: `Runs <command> exactly as given (no shell, stdin and environment passed
through) and exits with its exit status: 128+n when signal n killed it, 127
when it was not found. Signals sent to capture are forwarded to it.

The command's stdout is read as a ` + "`go test -json`" + ` stream: each line is stored,
secret-scrubbed, in <run>/<lane>/results.json, and printed as the text ` + "`go test`" + `
prints (` + "`go test -v`" + `'s when the command passes -v; --render raw prints the
stream unchanged). A line that is not an event passes through verbatim, and a
stream with no events is stored as-is and recorded "results": "unparsed".

A lane whose runner writes a report file instead (JUnit XML, Vitest JSON)
names it with --results: stdout then passes through untouched and the file is
copied, scrubbed, into the lane directory after the command exits.

The command sees FORGE_TEST_RUN (the run), FORGE_TEST_LANE and
FORGE_TEST_LANE_DIR (where its coverage goes: a -coverprofile, a GOCOVERDIR, a
Vitest reportsDirectory), and the lane directory exists before it starts.
After it exits, <lane>/capture.json (forge.test.lane/v1) records the command,
its start, end and exit status, the results and the coverage files found.

Nothing about the record changes the exit status. An unwritable run, a full
disk or a bad --lane is a warning on stderr; the command runs regardless.
Without --run, FORGE_TEST_RUN is the run, else a new one is begun. With
FORGE_TESTS=off the command runs and nothing is recorded.`,
		Example: `  forge test capture --run "$RUN" --lane unit -- \
    go test -json -coverprofile="$RUN/unit/cover.out" ./...
  forge test capture --run "$RUN" --lane web --results web/junit.xml -- \
    npx vitest run --reporter=junit --outputFile=web/junit.xml`,
		Args:         cobra.MinimumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, _ := cmdutil.FindProjectRoot()
			dir, _ := cmdutil.ResolutionRoot()
			res := testcapture.Capture(cmd.Context(), testcapture.CaptureOptions{
				RunDir:  run,
				Lane:    lane,
				Results: results,
				Format:  format,
				Render:  render,
				Command: args,
				Stdin:   cmd.InOrStdin(),
				Stdout:  cmd.OutOrStdout(),
				Stderr:  cmd.ErrOrStderr(),
				Begin: testcapture.BeginOptions{
					Dir:          dir,
					ProjectRoot:  root,
					ForgeVersion: buildinfo.Version(),
				},
			})
			return captureVerdict(res, lane, args)
		},
	}
	// Everything after the command name belongs to the command.
	cmd.Flags().SetInterspersed(false)
	cmd.Flags().StringVar(&run, "run", "", "Run directory to record into (default: $FORGE_TEST_RUN, else a new run)")
	cmd.Flags().StringVar(&lane, "lane", "", "Lane name: letters, digits, '-' and '_' (e.g. unit, integration, web)")
	cmd.Flags().StringVar(&results, "results", "", "Report file the command writes (JUnit XML, Vitest JSON), copied into the lane after it exits")
	cmd.Flags().StringVar(&format, "format", "", "Format of --results: go-json, junit or vitest (default: detected)")
	cmd.Flags().StringVar(&render, "render", testcapture.RenderAuto, "How a go test -json stream is printed: auto (verbose when the command passes -v), text, verbose or raw")
	return cmd
}

// captureVerdict turns the command's exit status into capture's own.
func captureVerdict(res testcapture.CaptureResult, lane string, argv []string) error {
	if res.StartErr != nil {
		return exitCodeError{code: res.ExitCode, msg: res.StartErr.Error()}
	}
	if res.ExitCode == 0 {
		return nil
	}
	where := ""
	if res.LaneDir != "" {
		where = " (recorded in " + res.LaneDir + ")"
	}
	name := strings.TrimSpace(lane)
	if name == "" {
		name = "-"
	}
	return exitCodeError{
		code:    res.ExitCode,
		msg:     fmt.Sprintf("forge test capture: lane %s: %s exited with status %d%s", name, argv[0], res.ExitCode, where),
		verdict: true,
	}
}
