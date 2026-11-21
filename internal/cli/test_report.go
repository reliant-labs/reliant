package cli

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/testlocal"
	skipcheck "github.com/reliant-labs/forge/internal/testreport"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// The --upload values. A bare --upload is uploadOn.
const (
	uploadOff      = ""
	uploadOn       = "on"
	uploadRequired = "required"
)

// uploadFlag is --upload[=required]: a switch that also takes one word. It
// reads as a bool to pflag (a bare --upload needs no value, and usage names
// none), and accepts true/false and "required".
type uploadFlag struct{ v *string }

func (f uploadFlag) String() string {
	if f.v == nil {
		return ""
	}
	return *f.v
}

func (f uploadFlag) Set(s string) error {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "true", "on", "1", "yes":
		*f.v = uploadOn
	case "false", "off", "0", "no":
		*f.v = uploadOff
	case uploadRequired:
		*f.v = uploadRequired
	default:
		return fmt.Errorf("use --upload or --upload=required, not --upload=%s", s)
	}
	return nil
}

func (f uploadFlag) Type() string { return "bool" }

// testReportOptions are `forge test report`'s flags.
type testReportOptions struct {
	sel      runSelection
	base     string
	gateJSON string
	format   string
	asJSON   bool
	upload   string
}

// newTestReportCmd is `forge test report`: one commit's runs, merged, judged
// and summarised. It reads the record; it runs nothing.
func newTestReportCmd() *cobra.Command {
	var o testReportOptions
	cmd := &cobra.Command{
		Use:   "report [<run>...]",
		Short: "Summarise a commit's recorded test runs: results, merged coverage, diff coverage (runs nothing)",
		Long: `Summarises recorded test runs: their results, their coverage merged across
lanes, diff coverage against a base, and the failures. It runs no test.

Which runs:
  (none)            the newest run, with every run on its source: the clean
                    runs of its commit, or the runs on its dirty tree
  <run>...          these runs (an id, an id prefix, or a run directory)
  --commit <sha>    every clean-tree run of the commit

Runs from different trees never merge: a dirty run never joins a commit's
report, and two commits' runs are refused (exit 2).

Diff coverage covers the lines ` + "`git diff -U0 <base>...<commit>`" + ` adds or changes. The
base is --base, else the run's recorded base (a pull request's), else the
merge base with the default branch. The delta compares with the base
commit's own local runs, and says "no report for base" when it has none.
Generated code (gen/, *_gen.go, *.pb.go, *-hooks.ts, files headed "Code
generated … DO NOT EDIT.") is not counted, and is reported on its own line.

Exit codes (coverage never decides one):
  0  every recorded test passed
  1  at least one test failed
  2  undetermined: no results, a lane whose results could not be read or that
     exited non-zero with no failure recorded, a package whose every test
     skipped (verify-test-run's zero-evidence rule), or runs from different trees
  4  --upload=required and the report could not be uploaded

--gate-json writes the same gate document ` + "`forge ci verify-test-run --gate-json`" + `
writes, with its zero-evidence and mass-skip checks, so a deploy's tests gate
keeps its meaning.`,
		Example: `  forge test report
  forge test report --commit HEAD --base origin/main
  forge test report --json
  forge test report --format lcov > lcov.info
  forge test report --gate-json gates/test.json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			o.sel.refs = args
			return runTestReport(cmd, o)
		},
	}
	cmd.Flags().StringVar(&o.sel.commit, "commit", "", "Report every clean-tree run of this commit")
	cmd.Flags().StringVar(&o.base, "base", "", "Revision diff coverage and the delta are measured against (default: the run's recorded base, else the merge base with the default branch)")
	cmd.Flags().StringVar(&o.gateJSON, "gate-json", "", "Also write the verdict to `FILE` as a gate document (verify-test-run's shape), for `forge gate record` or `forge env deploy --gate`")
	cmd.Flags().StringVar(&o.format, "format", "text", "Output: text, json (forge.test.report/v1), lcov, cobertura or html")
	cmd.Flags().BoolVar(&o.asJSON, "json", false, "Same as --format json")
	cmd.Flags().Var(uploadFlag{&o.upload}, "upload", "Upload the report to the control plane (--upload=required: exit 4 when it cannot be uploaded)")
	cmd.Flags().Lookup("upload").NoOptDefVal = "true"
	return cmd
}

func runTestReport(cmd *cobra.Command, o testReportOptions) error {
	const verb = "forge test report"
	started := time.Now()
	format := o.format
	if o.asJSON {
		if cmd.Flags().Changed("format") && format != "json" {
			return fmt.Errorf("--json and --format %s disagree; pass one", format)
		}
		format = "json"
	}
	switch format {
	case "text", "json", "lcov", "cobertura", "html":
	default:
		return fmt.Errorf("--format %q: use text, json, lcov, cobertura or html", format)
	}
	if o.sel.commit != "" && len(o.sel.refs) > 0 {
		return errors.New("name runs or --commit, not both")
	}

	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()

	// undetermined finishes the command when there is no report to give:
	// the document still goes out (with its head) so a pipeline reading
	// --json sees why.
	undetermined := func(msg string, suggested ...string) error {
		rep := &testreport.Report{Head: testreport.Head{Schema: testreport.SchemaReport, ExitCode: exitUndetermined, Error: msg}, Runs: []testreport.RunRef{}, Suggested: suggested}
		if o.gateJSON != "" {
			an := skipcheck.Analysis{Undetermined: []skipcheck.Unknown{{Reason: msg}}}
			if err := writeTestRunGate(o.gateJSON, an, started); err != nil {
				return err
			}
		}
		if format == "json" {
			if err := writeJSONDocument(out, rep); err != nil {
				return err
			}
		} else {
			for _, s := range suggested {
				fmt.Fprintf(errOut, "  next: %s\n", s)
			}
		}
		return verdictErr(exitUndetermined, "UNDETERMINED: "+msg+" (this is not a pass)")
	}

	root, err := testRecordRoot(ctx, verb)
	if err != nil {
		return err
	}
	all, problems, err := testlocal.List(root)
	if err != nil {
		return err
	}
	runs, what, err := selectRuns(ctx, root, o.sel, all)
	if err != nil {
		if errors.Is(err, errNoRuns) {
			return undetermined("no test runs are recorded under "+testreport.RunsRoot(root), "task test", "reliant forge test capture --lane unit -- go test -json ./...")
		}
		return undetermined(err.Error(), "reliant forge test runs")
	}
	a, err := testlocal.Assemble(ctx, testlocal.Options{ProjectRoot: root, Runs: runs, AllRuns: all, Base: o.base, Git: testGit})
	if err != nil {
		return undetermined(err.Error(), "reliant forge test runs")
	}

	rep := a.Report("")
	policy, err := testSkipPolicy()
	if err != nil {
		return err
	}
	an := a.SkipAnalysis(policy)
	if zero := testlocal.ZeroEvidence(an); rep.ExitCode == exitOK && len(zero) > 0 {
		rep.ExitCode = exitUndetermined
		rep.Error = fmt.Sprintf("zero evidence: every test skipped in %s, so its pass proves nothing", strings.Join(zero, ", "))
		rep.OK = false
	}
	if o.upload != uploadOff {
		// There is no control plane to send it to yet. Say so once,
		// and keep the run's own verdict unless the caller required it.
		rep.Upload = &testreport.Upload{Status: "skipped", Error: "no control plane records test runs yet; the report stays local"}
		fmt.Fprintln(errOut, "forge test report: --upload: no control plane records test runs yet; the report stays in .forge/tests/")
		if o.upload == uploadRequired && rep.ExitCode != exitWrong {
			rep.ExitCode = exitRefused
			rep.Error = "upload required, and no control plane records test runs yet"
			rep.OK = false
		}
	}
	rep.Suggested = reportSuggestions(rep, a, o)

	if o.gateJSON != "" {
		if err := writeTestRunGate(o.gateJSON, an, started); err != nil {
			return err
		}
	}

	switch format {
	case "json":
		if err := writeJSONDocument(out, rep); err != nil {
			return err
		}
	case "text":
		renderTestReport(out, rep, a, an, what, problems)
	default:
		if a.Coverage == nil {
			fmt.Fprintln(errOut, "forge test report: the runs carry no coverage; the export is empty")
			a.Coverage = &testreport.Coverage{}
		}
		if err := writeCoverageExport(out, format, a, rep); err != nil {
			return err
		}
		fmt.Fprintln(errOut, reportVerdictLine(rep))
	}
	return verdictErr(rep.ExitCode, reportVerdictLine(rep))
}

func writeCoverageExport(w io.Writer, format string, a *testlocal.Assembly, rep *testreport.Report) error {
	switch format {
	case "lcov":
		return testlocal.WriteLCOV(w, a.Coverage)
	case "cobertura":
		return testlocal.WriteCobertura(w, a.Coverage, a.RepoRoot, time.Now())
	case "html":
		title := "Coverage"
		if rep.Project != "" {
			title += " — " + rep.Project
		}
		if rep.Commit != "" {
			title += " @ " + testShortID(rep.Commit)
		}
		return testlocal.WriteHTML(w, a.Coverage, a.RepoRoot, title)
	}
	return fmt.Errorf("unknown export %q", format)
}

func reportVerdictLine(rep *testreport.Report) string {
	switch rep.ExitCode {
	case exitOK:
		return "PASS: every recorded test passed"
	case exitWrong:
		return fmt.Sprintf("FAIL: %d test(s) failed", rep.Totals.Failed+rep.Totals.PackagesFailed)
	case exitRefused:
		return "REFUSED: " + rep.Error
	}
	return "UNDETERMINED: " + rep.Error + " (this is not a pass)"
}

func reportSuggestions(rep *testreport.Report, a *testlocal.Assembly, o testReportOptions) []string {
	var s []string
	if len(rep.Failures) > 0 {
		f := rep.Failures[0]
		if f.Test.Lang == testreport.LangGo && f.Test.Name != "" {
			top := f.Test.TopLevel()
			s = append(s, fmt.Sprintf("go test -run '^%s$' %s", top.Name, f.Test.Package))
		}
	}
	if rep.Coverage != nil {
		base := o.base
		if base == "" && a.BaseSHA != "" {
			base = testShortID(a.BaseSHA)
		}
		target := ""
		if len(rep.Packages) > 0 {
			target = " " + rep.Packages[0].Path
		}
		if base != "" {
			s = append(s, fmt.Sprintf("reliant forge test coverage%s --diff %s", target, base))
		} else {
			s = append(s, "reliant forge test coverage"+target)
		}
	}
	if rep.ExitCode == exitUndetermined {
		s = append(s, "reliant forge test runs")
	}
	return s
}

func renderTestReport(w io.Writer, rep *testreport.Report, a *testlocal.Assembly, an skipcheck.Analysis, what string, problems []string) {
	head := "forge test report"
	if rep.Project != "" {
		head += " — " + rep.Project
	}
	state := "clean"
	if rep.Dirty {
		state = "dirty tree"
	}
	if rep.Commit != "" {
		head += fmt.Sprintf(" @ %s (%s)", testShortID(rep.Commit), state)
	}
	fmt.Fprintln(w, head)
	fmt.Fprintf(w, "Runs: %s\n", what)
	for _, r := range rep.Runs {
		line := fmt.Sprintf("  %-14s %s  %s, exit %d", r.Lane, r.ID, r.Source, r.ExitCode)
		if r.URL != "" {
			line += "  " + r.URL
		}
		fmt.Fprintln(w, line)
	}
	t := rep.Totals
	fmt.Fprintf(w, "Tests: %d passed, %d failed, %d skipped", t.Passed, t.Failed, t.Skipped)
	if t.FlakyRetryPass > 0 {
		fmt.Fprintf(w, " (%d passed on retry)", t.FlakyRetryPass)
	}
	if t.PackagesFailed > 0 {
		fmt.Fprintf(w, "; %d package(s) failed outside any test", t.PackagesFailed)
	}
	fmt.Fprintln(w)

	renderReportCoverage(w, rep)

	if len(rep.Failures) > 0 {
		fmt.Fprintf(w, "Failures (%d):\n", len(rep.Failures))
		for i, f := range rep.Failures {
			if i == 10 {
				fmt.Fprintf(w, "  … and %d more (--json lists all)\n", len(rep.Failures)-10)
				break
			}
			loc := ""
			if f.File != "" {
				loc = fmt.Sprintf("  %s:%d", f.File, f.Line)
			}
			fmt.Fprintf(w, "  ✗ %s (%s)%s\n", f.Test, f.Lane, loc)
			for _, l := range outputHead(f.Output, 6) {
				fmt.Fprintf(w, "      %s\n", l)
			}
		}
	}
	if len(rep.Flakes) > 0 {
		fmt.Fprintf(w, "Flake signals (%d):\n", len(rep.Flakes))
		for _, f := range rep.Flakes {
			fmt.Fprintf(w, "  ~ %s: %s (%s)\n", f.Test, f.Signal, f.Verdict)
		}
	}
	for _, f := range an.Findings {
		if f.Kind == skipcheck.KindFailed {
			continue
		}
		fmt.Fprintf(w, "Skip check: %s %s (%d of %d tests skipped)\n", f.Package, f.Kind, f.Skipped, f.Tests)
	}
	for _, u := range a.Undetermined {
		fmt.Fprintf(w, "Undetermined: %s\n", u)
	}
	for _, n := range append(a.Notes, problems...) {
		fmt.Fprintf(w, "Note: %s\n", n)
	}
	for _, s := range rep.Suggested {
		fmt.Fprintf(w, "  next: %s\n", s)
	}
	fmt.Fprintln(w, reportVerdictLine(rep))
}

func renderReportCoverage(w io.Writer, rep *testreport.Report) {
	c := rep.Coverage
	if c == nil {
		fmt.Fprintln(w, "Coverage: none recorded")
		return
	}
	fmt.Fprintf(w, "Coverage (%s): combined %.1f%%", orDash(string(c.Granularity)), c.Combined)
	for _, l := range sortedLanes(c.Lanes) {
		fmt.Fprintf(w, " · %s %.1f%%", l, c.Lanes[testreport.Lane(l)])
	}
	if c.OnlyE2E > 0 {
		fmt.Fprintf(w, " · only e2e %.1f%%", c.OnlyE2E)
	}
	fmt.Fprintln(w)
	if c.CombinedLabel != "" {
		fmt.Fprintf(w, "  combined is %s\n", c.CombinedLabel)
	}
	for _, l := range c.Languages {
		if len(c.Languages) > 1 {
			fmt.Fprintf(w, "  %s: %.1f%% of %d statements\n", l.Lang, l.Combined, l.Statements)
		}
	}
	if c.Excluded.GeneratedStatements > 0 {
		fmt.Fprintf(w, "  generated code, not counted: %d statements\n", c.Excluded.GeneratedStatements)
	}
	if c.Excluded.FilteredStatements > 0 {
		fmt.Fprintf(w, "  excluded by filter, not counted: %d statements\n", c.Excluded.FilteredStatements)
	}
	if d := c.Diff; d != nil && rep.Base != nil {
		if d.Lines == 0 {
			fmt.Fprintf(w, "  diff vs %s: no changed executable lines\n", testShortID(rep.Base.SHA))
		} else {
			fmt.Fprintf(w, "  diff vs %s: %d of %d changed lines covered (%.1f%%)\n", testShortID(rep.Base.SHA), d.Covered, d.Lines, d.Pct)
		}
	}
	switch {
	case c.Delta != nil:
		fmt.Fprintf(w, "  delta vs base: %+.1f\n", *c.Delta)
	case rep.Base != nil && c.DeltaReason != "":
		fmt.Fprintf(w, "  delta: %s\n", c.DeltaReason)
	}
	if len(rep.Packages) > 0 {
		fmt.Fprintln(w, "  lowest covered:")
	}
	for _, p := range rep.Packages[:min(5, len(rep.Packages))] {
		fmt.Fprintf(w, "    %5.1f%%  %s (%d statements)\n", p.Combined, p.Path, p.Statements)
	}
}

func sortedLanes(m map[testreport.Lane]float64) []string {
	out := make([]string, 0, len(m))
	for l := range m {
		out = append(out, string(l))
	}
	sort.Strings(out)
	return out
}

func outputHead(s string, n int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = append(lines[:n], "…")
	}
	return lines
}
