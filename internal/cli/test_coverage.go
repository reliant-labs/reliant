package cli

import (
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/testlocal"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// schemaTestCoverageView names `forge test coverage --json`.
const schemaTestCoverageView = "forge.test.coverage-view/v1"

// testCoverageDoc is `forge test coverage --json`: the views the flags asked
// for, and the forge.test.coverage/v1 document they were computed from
// (narrowed to the path and lane asked for), unchanged, under coverage. The
// stored document stays exactly its own schema; what a command adds to it is
// this one's.
type testCoverageDoc struct {
	testreport.Head
	// Path and Lane are the filters, when given.
	Path    string                     `json:"path,omitempty"`
	Lane    string                     `json:"lane,omitempty"`
	Summary testreport.CoverageSummary `json:"summary"`
	// By is "package" or "file": what Rollup groups by.
	By     string              `json:"by"`
	Rollup []testreport.Rollup `json:"rollup"`
	// BaseSHA and Diff are present with --diff.
	BaseSHA string                  `json:"base_sha,omitempty"`
	Diff    *testreport.DiffSummary `json:"diff,omitempty"`
	// Coverage is the forge.test.coverage/v1 document; absent when there
	// is none.
	Coverage  *testreport.Coverage `json:"coverage,omitempty"`
	Suggested []string             `json:"suggested,omitempty"`
}

type testCoverageOptions struct {
	sel    runSelection
	lane   string
	diff   string
	by     string
	asJSON bool
}

// newTestCoverageCmd is `forge test coverage`: the recorded coverage of a
// commit's runs, per package or file, optionally narrowed to a path, a lane,
// or the lines a diff changed.
func newTestCoverageCmd() *cobra.Command {
	var o testCoverageOptions
	cmd := &cobra.Command{
		Use:   "coverage [<path>]",
		Short: "Show recorded coverage per package or file, for a path, a lane or a diff (runs nothing)",
		Long: `Shows the coverage the recorded runs carry, merged across lanes: a combined
figure, each lane's, and a row per package (or per file with --by file).

<path> narrows it to a repo-relative file or directory. --lane keeps one
lane's counts. --diff <base> adds diff coverage: the lines
` + "`git diff -U0 <base>...<commit>`" + ` adds or changes, and which of them no test
reached.

The runs are the newest run's source by default (the clean runs of its
commit), --commit <sha> for a commit's clean runs, or --run <id> for one run
and the runs on its tree. Generated code is not counted. Coverage never
changes the exit code: 0 when coverage was found, 2 when there is none.`,
		Example: `  forge test coverage
  forge test coverage internal/billing --by file
  forge test coverage --diff origin/main
  forge test coverage --lane e2e --json`,
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := ""
			if len(args) == 1 {
				p = args[0]
			}
			return runTestCoverage(cmd, o, p)
		},
	}
	cmd.Flags().StringVar(&o.sel.commit, "commit", "", "Read every clean-tree run of this commit")
	cmd.Flags().StringVar(&o.sel.run, "run", "", "Read this run (id, prefix or directory) and the runs on its tree")
	cmd.Flags().StringVar(&o.lane, "lane", "", "Keep only this lane's coverage")
	cmd.Flags().StringVar(&o.diff, "diff", "", "Show diff coverage against this base revision")
	cmd.Flags().StringVar(&o.by, "by", "package", "Roll up by package or file")
	cmd.Flags().BoolVar(&o.asJSON, "json", false, "Print the forge.test.coverage/v1 document with its views")
	return cmd
}

func runTestCoverage(cmd *cobra.Command, o testCoverageOptions, filter string) error {
	const verb = "forge test coverage"
	by := testreport.ByPackage
	switch o.by {
	case "package":
	case "file":
		by = testreport.ByFile
	default:
		return fmt.Errorf("--by %q: use package or file", o.by)
	}
	if o.sel.commit != "" && o.sel.run != "" {
		return errors.New("pass --commit or --run, not both")
	}
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	root, err := testRecordRoot(ctx, verb)
	if err != nil {
		return err
	}
	all, _, err := testlocal.List(root)
	if err != nil {
		return err
	}
	fail := func(msg string, suggested ...string) error {
		if o.asJSON {
			doc := testCoverageDoc{Head: testreport.Head{Schema: schemaTestCoverageView, ExitCode: exitUndetermined, Error: msg},
				Path: filter, Lane: o.lane, By: o.by, Rollup: []testreport.Rollup{}, Suggested: suggested}
			if err := writeJSONDocument(out, doc); err != nil {
				return err
			}
		}
		return verdictErr(exitUndetermined, "UNDETERMINED: "+msg)
	}
	runs, what, err := selectRuns(ctx, root, o.sel, all)
	if err != nil {
		if errors.Is(err, errNoRuns) {
			return fail("no test runs are recorded under "+testreport.RunsRoot(root), "task test")
		}
		return fail(err.Error(), "reliant forge test runs")
	}
	a, err := testlocal.Assemble(ctx, testlocal.Options{
		ProjectRoot: root, Runs: runs, Base: o.diff, NoDiff: o.diff == "", Lane: o.lane, Git: testGit,
	})
	if err != nil {
		return fail(err.Error(), "reliant forge test runs")
	}
	if a.Coverage == nil {
		return fail("the runs carry no coverage", "pass -coverprofile=$RUN/<lane>/cover.out to the lane's go test")
	}
	cov := narrowCoverage(a.Coverage, filter)
	if filter != "" && len(cov.Files) == 0 {
		return fail(fmt.Sprintf("no covered file under %s", filter), "reliant forge test coverage")
	}
	doc := testCoverageDoc{Head: testreport.Head{Schema: schemaTestCoverageView, OK: true},
		Coverage: cov, Path: filter, Lane: o.lane, Summary: cov.Summary(), By: o.by, Rollup: cov.Rollup(by)}
	if doc.Rollup == nil {
		doc.Rollup = []testreport.Rollup{}
	}
	if o.diff != "" && a.Changed != nil {
		d := cov.Diff(a.Changed)
		doc.Diff, doc.BaseSHA = &d, a.BaseSHA
	}
	if worst := worstRollup(doc.Rollup); worst != "" && by == testreport.ByPackage {
		doc.Suggested = append(doc.Suggested, "reliant forge test coverage "+worst+" --by file")
	}
	if o.diff == "" {
		doc.Suggested = append(doc.Suggested, "reliant forge test coverage --diff origin/main")
	}
	if o.asJSON {
		return writeJSONDocument(out, doc)
	}
	renderTestCoverage(out, doc, a, what, o.lane)
	return nil
}

// narrowCoverage keeps the files under a repo-relative path (the import path
// for a file with none). The result shares blocks with c.
func narrowCoverage(c *testreport.Coverage, filter string) *testreport.Coverage {
	filter = strings.Trim(path.Clean(strings.ReplaceAll(filter, `\`, "/")), "/")
	if filter == "" || filter == "." {
		return c
	}
	n := *c
	n.Files = nil
	for _, f := range c.Files {
		p := f.DisplayPath()
		if p == filter || strings.HasPrefix(p, filter+"/") || (f.Package != "" && (f.Package == filter || strings.HasSuffix(f.Package, "/"+filter))) {
			n.Files = append(n.Files, f)
		}
	}
	return &n
}

func worstRollup(rs []testreport.Rollup) string {
	worst, pct := "", 101.0
	for _, r := range rs {
		if r.Combined.Statements > 0 && r.Combined.Pct < pct {
			worst, pct = r.Path, r.Combined.Pct
		}
	}
	return worst
}

func renderTestCoverage(w io.Writer, doc testCoverageDoc, a *testlocal.Assembly, what, lane string) {
	s := doc.Summary
	head := "forge test coverage"
	if doc.Path != "" {
		head += " — " + doc.Path
	}
	if a.Commit != "" {
		head += " @ " + testShortID(a.Commit)
		if a.Dirty {
			head += " (dirty tree)"
		}
	}
	fmt.Fprintln(w, head)
	fmt.Fprintf(w, "Runs: %s", what)
	if lane != "" {
		fmt.Fprintf(w, ", lane %s", lane)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Combined: %.1f%% of %d statements", s.Combined.Pct, s.Combined.Statements)
	for _, l := range sortedTallyLanes(s.Lanes) {
		fmt.Fprintf(w, " · %s %.1f%%", l, s.Lanes[testreport.Lane(l)].Pct)
	}
	if s.OnlyE2E.Covered > 0 {
		fmt.Fprintf(w, " · only e2e %.1f%%", s.OnlyE2E.Pct)
	}
	fmt.Fprintln(w)
	if s.CombinedLabel != "" {
		fmt.Fprintf(w, "  combined is %s\n", s.CombinedLabel)
	}
	if s.Generated.Statements > 0 {
		fmt.Fprintf(w, "  generated code, not counted: %d statements\n", s.Generated.Statements)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %7s  %10s  %s\n", "cover", "statements", doc.By)
	for _, r := range doc.Rollup {
		fmt.Fprintf(w, "  %6.1f%%  %10d  %s\n", r.Combined.Pct, r.Combined.Statements, r.Path)
	}
	if d := doc.Diff; d != nil {
		fmt.Fprintln(w)
		if d.Lines == 0 {
			fmt.Fprintf(w, "Diff vs %s: no changed executable lines\n", testShortID(doc.BaseSHA))
		} else {
			fmt.Fprintf(w, "Diff vs %s: %d of %d changed lines covered (%.1f%%)\n", testShortID(doc.BaseSHA), d.Covered, d.Lines, d.Pct)
			for _, f := range d.Files {
				if len(f.Uncovered) == 0 {
					continue
				}
				fmt.Fprintf(w, "  %s: %d/%d, not covered: %s\n", f.Path, f.Covered, f.Lines, formatRanges(f.Uncovered))
			}
		}
		if d.Truncated {
			fmt.Fprintln(w, "  (diff truncated: too large to check every line)")
		}
	}
	for _, n := range a.Notes {
		fmt.Fprintf(w, "Note: %s\n", n)
	}
	for _, sg := range doc.Suggested {
		fmt.Fprintf(w, "  next: %s\n", sg)
	}
}

func sortedTallyLanes(m map[testreport.Lane]testreport.Tally) []string {
	f := make(map[testreport.Lane]float64, len(m))
	for k, v := range m {
		f[k] = v.Pct
	}
	return sortedLanes(f)
}

func formatRanges(rs []testreport.LineRange) string {
	parts := make([]string, 0, len(rs))
	for i, r := range rs {
		if i == 12 {
			parts = append(parts, fmt.Sprintf("… (%d more)", len(rs)-12))
			break
		}
		if r.Start == r.End {
			parts = append(parts, fmt.Sprint(r.Start))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", r.Start, r.End))
		}
	}
	return strings.Join(parts, ", ")
}
