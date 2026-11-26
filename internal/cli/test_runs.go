package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/testlocal"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// schemaTestRuns names `forge test runs --json`.
const schemaTestRuns = "forge.test.runs/v1"

// testRunsDoc is `forge test runs --json`.
type testRunsDoc struct {
	testreport.Head
	Runs []testRunRow `json:"runs"`
	// Problems name run directories that could not be read.
	Problems  []string `json:"problems,omitempty"`
	Suggested []string `json:"suggested,omitempty"`
}

// testRunRow is one run in the list: its run.json facts and each lane's
// exit code. Results are not parsed here; `forge test report <id>` does.
type testRunRow struct {
	ID        string             `json:"id"`
	Dir       string             `json:"dir"`
	CreatedAt time.Time          `json:"created_at"`
	Commit    string             `json:"commit,omitempty"`
	Tree      string             `json:"tree,omitempty"`
	Dirty     bool               `json:"dirty"`
	Branch    string             `json:"branch,omitempty"`
	Source    string             `json:"source"`
	URL       string             `json:"url,omitempty"`
	Lanes     []testRunLaneEntry `json:"lanes"`
}

type testRunLaneEntry struct {
	Lane     string `json:"lane"`
	ExitCode int    `json:"exit_code"`
	Format   string `json:"format"`
	Results  string `json:"results"`
	Coverage bool   `json:"coverage"`
}

// newTestRunsCmd is `forge test runs`: the local runs, newest first.
func newTestRunsCmd() *cobra.Command {
	var (
		commit, branch string
		limit          int
		asJSON         bool
	)
	cmd := &cobra.Command{
		Use:   "runs",
		Short: "List the recorded test runs, newest first (runs nothing)",
		Long: `Lists the test runs recorded under .forge/tests/runs/, newest first: each run's
commit, whether its tree was dirty, its branch, where it ran, and each lane's
exit code. ` + "`forge test report <id>`" + ` summarises one.`,
		Example: `  forge test runs
  forge test runs --commit HEAD
  forge test runs --branch main --json`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if commit != "" && branch != "" {
				return errors.New("pass --commit or --branch, not both")
			}
			ctx := cmd.Context()
			root, err := testRecordRoot(ctx, "forge test runs")
			if err != nil {
				return err
			}
			all, problems, err := testlocal.List(root)
			if err != nil {
				return err
			}
			if commit != "" {
				sha := commit
				if full, err := testlocal.ResolveRev(ctx, testGit, root, commit); err == nil && full != "" {
					sha = full
				}
				all = filterRuns(all, func(r *testlocal.Run) bool { return testlocal.MatchCommit(r, sha) })
			}
			if branch != "" {
				all = filterRuns(all, func(r *testlocal.Run) bool { return r.Branch == branch })
			}
			total := len(all)
			if limit > 0 && len(all) > limit {
				all = all[:limit]
			}
			doc := testRunsDoc{Head: testreport.Head{Schema: schemaTestRuns, OK: true}, Runs: []testRunRow{}, Problems: problems}
			for _, r := range all {
				doc.Runs = append(doc.Runs, runRow(r))
			}
			if len(doc.Runs) > 0 {
				doc.Suggested = []string{"reliant forge test report " + doc.Runs[0].ID}
			} else {
				doc.Suggested = []string{"task test"}
			}
			if asJSON {
				return writeJSONDocument(cmd.OutOrStdout(), doc)
			}
			renderTestRuns(cmd.OutOrStdout(), doc, total)
			return nil
		},
	}
	cmd.Flags().StringVar(&commit, "commit", "", "Only runs of this commit (clean or dirty)")
	cmd.Flags().StringVar(&branch, "branch", "", "Only runs on this branch")
	cmd.Flags().IntVar(&limit, "limit", 20, "Show at most this many runs (0: all)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print a forge.test.runs/v1 document")
	return cmd
}

func filterRuns(runs []*testlocal.Run, keep func(*testlocal.Run) bool) []*testlocal.Run {
	var out []*testlocal.Run
	for _, r := range runs {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}

func runRow(r *testlocal.Run) testRunRow {
	row := testRunRow{
		ID: r.ID, Dir: r.Dir, CreatedAt: r.CreatedAt, Commit: r.Commit, Tree: r.TreeHash,
		Dirty: r.Dirty, Branch: r.Branch, Source: r.Source(), URL: r.URL(), Lanes: []testRunLaneEntry{},
	}
	for i := range r.Lanes {
		l := &r.Lanes[i]
		row.Lanes = append(row.Lanes, testRunLaneEntry{
			Lane: l.Lane, ExitCode: l.ExitCode, Format: string(l.Format), Results: string(l.Results),
			Coverage: len(l.CoverageInputs()) > 0,
		})
	}
	return row
}

func renderTestRuns(w io.Writer, doc testRunsDoc, total int) {
	if len(doc.Runs) == 0 {
		fmt.Fprintln(w, "No test runs recorded here yet. `task test` records one.")
		return
	}
	for _, r := range doc.Runs {
		commit := testShortID(r.Commit)
		if commit == "" {
			commit = "(no commit)"
		}
		if r.Dirty {
			commit += "+dirty"
		}
		var lanes []string
		for _, l := range r.Lanes {
			mark := "✓"
			if l.ExitCode != 0 {
				mark = fmt.Sprintf("✗%d", l.ExitCode)
			}
			cov := ""
			if l.Coverage {
				cov = "+cov"
			}
			lanes = append(lanes, fmt.Sprintf("%s %s%s", l.Lane, mark, cov))
		}
		if len(lanes) == 0 {
			lanes = []string{"(no lanes captured)"}
		}
		branch := r.Branch
		if branch == "" {
			branch = "-"
		}
		fmt.Fprintf(w, "%s  %s  %-19s %-12s %-5s  %s\n", r.ID, r.CreatedAt.Local().Format("2006-01-02 15:04"), commit, branch, r.Source, strings.Join(lanes, ", "))
	}
	if total > len(doc.Runs) {
		fmt.Fprintf(w, "(%d of %d runs; --limit 0 shows all)\n", len(doc.Runs), total)
	}
	for _, p := range doc.Problems {
		fmt.Fprintf(w, "Note: %s\n", p)
	}
}
