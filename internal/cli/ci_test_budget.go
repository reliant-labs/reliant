package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/testbudget"
)

// newCITestBudgetCmd builds `forge ci test-budget`: the measured half of the
// fast/slow test tiers (forge/pkg/testkit.Slow).
//
// Like verify-test-run it runs nothing. It reads the record of the -short run
// the project already made, because a second spelling of the suite would time
// a different suite than the one the project runs (see test_removed.go).
func newCITestBudgetCmd() *cobra.Command {
	var (
		from     string
		budget   = testbudget.DefaultBudget
		warnOnly bool
	)
	const name = "forge ci test-budget"

	cmd := &cobra.Command{
		Use:   "test-budget",
		Short: "Fail when a test not marked slow took longer than the budget in a `go test -short -json` run (reads output; runs nothing)",
		Long: "Read a `go test -short -json` stream and report every top-level test that took\n" +
			"longer than --budget (default 2s), with its file and line.\n\n" +
			"  go test -short -json ./... | tee test.json | forge ci test-budget\n" +
			"  go test -short -json ./... > test.json; forge ci test-budget --from test.json\n\n" +
			"WHY. The -short tier is the blocking one, so every second in it is paid by\n" +
			"every change. A slow test belongs in the full tier, and says so:\n\n" +
			"  testkit.Slow(t, \"builds the forge binary\")      // forge/pkg/testkit\n" +
			"  if testing.Short() { t.Skip(\"builds the forge binary\") }\n\n" +
			"Under -short a marked test skips, so every test that RAN in a -short run claims\n" +
			"to be fast. One over budget is a slow test nobody marked. `forge lint\n" +
			"--slow-tests` flags slow work it can recognize in source; this catches the rest,\n" +
			"whatever made the test slow.\n\n" +
			"FEED IT A -short RUN. forge cannot see the flags a stream was made with. A test\n" +
			"whose own body checks testing.Short or calls testkit.Slow is reported as MARKED:\n" +
			"either it is still slow under -short (its bypass leaves slow work behind), or the\n" +
			"run was made without -short.\n\n" +
			"Top-level tests only: that is where the marker goes, and a table whose cases sum\n" +
			"past the budget is still that long in the blocking tier. A failing or unbuildable\n" +
			"package also fails the command, so piping a red run through it (in a shell\n" +
			"without `set -o pipefail`) cannot report green.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if budget <= 0 {
				return cliutil.UserErr(name, fmt.Sprintf("--budget must be positive, got %v", budget), "", "pass a duration such as --budget 2s")
			}
			in, closeIn, err := openTestRunInput(cmd, name, from)
			if err != nil {
				return err
			}
			defer closeIn()

			rep, err := testbudget.Check(in, budget)
			if err != nil {
				where := "standard input"
				if from != "" {
					where = from
				}
				if errors.Is(err, testbudget.ErrNoInput) {
					return cliutil.UserErr(name,
						"UNDETERMINED — "+where+" held no `go test -json` events, so there is no run to time (this is not a pass)",
						where, "produce the stream first: `go test -short -json ./... > test.json` then `"+name+" --from test.json`")
				}
				return cliutil.WrapUserErr(name, "UNDETERMINED — could not read "+where+" (this is not a pass)", where,
					"check the path and permissions, or re-run the suite with `-json`", err)
			}
			if root := moduleRootFrom("."); root != "" {
				testbudget.Locate(&rep, root)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "test budget %v: %d test(s) ran, %d over budget\n", budget, rep.Ran, len(rep.Overruns))
			for _, o := range rep.Overruns {
				where := o.Package
				if o.File != "" {
					where = fmt.Sprintf("%s:%d", o.File, o.Line)
				}
				note := ""
				if o.Marked {
					note = "  (marked slow — still slow under -short, or this run was not -short)"
				}
				fmt.Fprintf(out, "  ✗ %-50s %6.2fs  %s%s\n", o.Test, o.Elapsed.Seconds(), where, note)
			}

			if len(rep.FailedPackages) > 0 {
				return cliutil.UserErr(name,
					fmt.Sprintf("the run contains failures in %d package(s): %v", len(rep.FailedPackages), rep.FailedPackages), "",
					"fix the failing tests — and note that `go test | forge ...` hides go test's exit status unless the shell has `set -o pipefail`")
			}
			if len(rep.Overruns) == 0 {
				return nil
			}
			if warnOnly {
				fmt.Fprintln(out, "\n(--warn-only: reported, not enforced)")
				return nil
			}
			return cliutil.UserErr(name,
				fmt.Sprintf("%d test(s) not marked slow took longer than %v", len(rep.Overruns), budget), "",
				"make each one fast, or mark it slow so it runs in the full tier: testkit.Slow(t, \"<what makes it slow>\") "+
					"(forge/pkg/testkit), or `if testing.Short() { t.Skip(\"<why>\") }`")
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "Read the 'go test -json' stream from this file instead of stdin")
	cmd.Flags().DurationVar(&budget, "budget", testbudget.DefaultBudget, "Longest a test not marked slow may take")
	cmd.Flags().BoolVar(&warnOnly, "warn-only", false, "Report overruns without failing (adoption ramp; unreadable input and test failures still fail)")
	return cmd
}

// moduleRootFrom walks up from dir to the nearest go.mod, or returns "".
func moduleRootFrom(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(abs, "go.mod")); err == nil {
			return abs
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return ""
		}
		abs = parent
	}
}
