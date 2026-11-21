package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cigate"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cli/lint"
	"github.com/reliant-labs/forge/internal/testcapture"
	"github.com/reliant-labs/forge/internal/testflakes"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// testRoot is the directory the flake and quarantine commands work in: the
// forge project around the working directory, else the working directory
// itself — they work in any Go repository, forge project or not.
func testRoot() string {
	if root, err := cmdutil.FindProjectRoot(); err == nil {
		return root
	}
	dir, _ := cmdutil.ResolutionRoot()
	return dir
}

func testBegin(root string) testcapture.BeginOptions {
	dir, _ := cmdutil.ResolutionRoot()
	return testcapture.BeginOptions{Dir: dir, ProjectRoot: root, ForgeVersion: buildinfo.Version()}
}

// newTestFlakesCmd is `forge test flakes`: the flake states of the local
// record, and its two producers, gate and confirm.
func newTestFlakesCmd() *cobra.Command {
	var (
		states        []string
		issues, dry   bool
		repo          string
		jsonOut       bool
		includeHealth bool
	)
	cmd := &cobra.Command{
		Use:   "flakes [--state suspected|flaky|quarantined|removed|fixed] [--issues] [--json]",
		Short: "Flaky tests in the local test record, with the quarantine command for each",
		Long: `Computes each test's flake state from the local test record
(.forge/tests/runs/: the project's own lanes, ` + "`forge test flakes gate`" + ` and
` + "`forge test flakes confirm`" + ` runs) and prints it with the command that
quarantines it.

A confirmed flake event is a failure the same tree also passed: a retry pass
within a run (go test -count=N), a confirm rerun that passed, or another run
of the same tree. Failures matching a host signature (network, rate limits,
disk, ports, docker, timeouts under load) never score and are reported as
host health; three or more failures of one run that share a package, a
harness or a signature are one infra event.

States: suspected (one confirmed event: the state the delete-on-first-flake
policy acts on), flaky (events on two or more commits in 30 days),
quarantined (an entry under ci/quarantine/; an expired one stays applied and
is reported overdue), removed, fixed. The flake rate is the Wilson 95% lower
bound with its n: a ranking, never a trigger.

--issues files one flake:suspected issue per suspected or flaky test with gh
(the test's body, its failure signature and its runs), comments on an
existing one, and closes an open one whose test left those states.

--json prints a forge.test.flakes/v1 document.`,
		Example: `  forge test flakes
  forge test flakes --state suspected --json
  forge test flakes --issues --dry-run`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// TODO(tests.flakes.issues): forge.yaml's tests.flakes.issues
			// (design 1.8, task F5a) turns --issues on for a project; until
			// that block lands only the flag does.
			return runTestFlakes(cmd.Context(), cmd.OutOrStdout(), testFlakesArgs{
				root: testRoot(), states: states, issues: issues, dryRun: dry, repo: repo, json: jsonOut, all: includeHealth,
			})
		},
	}
	f := cmd.Flags()
	f.StringSliceVar(&states, "state", nil, "Only tests in these states (suspected, flaky, quarantined, removed, fixed, healthy); default every state but healthy")
	f.BoolVar(&includeHealth, "all", false, "Include healthy tests that have flake history")
	f.BoolVar(&issues, "issues", false, "File, update and close flake:suspected issues with gh")
	f.BoolVar(&dry, "dry-run", false, "With --issues: say what would be filed, commented and closed, and do none of it")
	f.StringVar(&repo, "repo", "", "With --issues: the owner/name to file in (default $GITHUB_REPOSITORY, else the current repository)")
	f.BoolVar(&jsonOut, "json", false, "Print a "+testreport.SchemaFlakes+" document")
	cmd.AddCommand(newTestFlakesGateCmd(), newTestFlakesConfirmCmd())
	return cmd
}

type testFlakesArgs struct {
	root         string
	states       []string
	issues       bool
	dryRun       bool
	repo         string
	json         bool
	all          bool
	gh           testflakes.GH
	now          time.Time
	issuesStdout io.Writer
}

var allFlakeStates = []testreport.State{
	testreport.StateHealthy, testreport.StateSuspected, testreport.StateFlaky,
	testreport.StateFixed, testreport.StateQuarantined, testreport.StateRemoved,
}

func runTestFlakes(ctx context.Context, out io.Writer, a testFlakesArgs) error {
	want, err := parseFlakeStates(a.states)
	if err != nil {
		return exitCodeError{code: exitWrong, msg: err.Error()}
	}
	if a.all && len(want) == 0 {
		want = allFlakeStates
	}
	now := a.now
	if now.IsZero() {
		now = time.Now()
	}
	// Issues need every state, healthy included, to see a test leave one.
	doc, warnings := testflakes.ComputeFlakes(testflakes.FlakesOptions{Root: a.root, Now: now, States: allFlakeStates})
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "forge test flakes: warning: %s\n", w)
	}
	if a.issues {
		iout := a.issuesStdout
		if iout == nil {
			iout = os.Stderr
			if !a.json {
				iout = out
			}
		}
		res := testflakes.SyncIssues(ctx, testflakes.IssueOptions{Root: a.root, Repo: a.repo, GH: a.gh, Stdout: iout, DryRun: a.dryRun}, doc)
		for i := range doc.Tests {
			if url := res.URLs[doc.Tests[i].ID.String()]; url != "" {
				doc.Tests[i].Issue = url
			}
		}
	}
	doc.Tests = filterFlakeStates(doc.Tests, want)
	doc.Suggested = testflakes.Suggestions(doc.Tests)
	if a.json {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(doc)
	}
	renderFlakes(out, doc)
	return nil
}

func parseFlakeStates(in []string) ([]testreport.State, error) {
	var out []testreport.State
	for _, s := range in {
		st := testreport.State(strings.ToLower(strings.TrimSpace(s)))
		ok := false
		for _, known := range allFlakeStates {
			ok = ok || st == known
		}
		if !ok {
			return nil, fmt.Errorf("--state %q: want suspected, flaky, quarantined, removed, fixed or healthy", s)
		}
		out = append(out, st)
	}
	return out, nil
}

// filterFlakeStates keeps the tests in want; empty want keeps every state but
// healthy.
func filterFlakeStates(tests []testreport.FlakyTest, want []testreport.State) []testreport.FlakyTest {
	keep := map[testreport.State]bool{}
	for _, s := range want {
		keep[s] = true
	}
	out := []testreport.FlakyTest{}
	for _, t := range tests {
		if (len(keep) == 0 && t.State != testreport.StateHealthy) || keep[t.State] {
			out = append(out, t)
		}
	}
	return out
}

func renderFlakes(w io.Writer, doc *testreport.Flakes) {
	if len(doc.Tests) == 0 {
		fmt.Fprintln(w, "No flaky tests in the local record (.forge/tests/runs/).")
	}
	for _, t := range doc.Tests {
		line := fmt.Sprintf("%-11s %s", strings.ToUpper(string(t.State)), t.ID)
		if t.N > 0 {
			line += fmt.Sprintf("  flake rate %.3f (n=%d, %d commit(s) in 30d)", t.Score, t.N, t.Events30d)
		}
		fmt.Fprintln(w, line)
		if q := t.Quarantine; q != nil {
			note := "  quarantined: " + q.Path
			if q.ExpiresAt != nil {
				note += " (expires " + q.ExpiresAt.UTC().Format(time.RFC3339) + ")"
			}
			if q.Overdue {
				note += " — OVERDUE: still applied; delete or rewrite the test"
			}
			fmt.Fprintln(w, note)
		}
		for _, e := range t.Events {
			fmt.Fprintf(w, "  %s  %s  run %s  lane %s  commit %s\n", e.At.UTC().Format("2006-01-02 15:04Z"), e.Signal, e.Run, e.Lane, flakeShortSHA(e.Commit))
		}
		if t.Issue != "" {
			fmt.Fprintf(w, "  issue: %s\n", t.Issue)
		}
	}
	if len(doc.Host) > 0 {
		fmt.Fprintln(w, "\nHost health (failures the host caused; never scored):")
		for _, h := range doc.Host {
			fmt.Fprintf(w, "  %-16s %d failure(s) in %d run(s)\n", h.Signature, h.Events, len(h.Runs))
		}
	}
	if len(doc.Infra) > 0 {
		fmt.Fprintln(w, "\nInfra events (3+ tests failing together; never scored per test):")
		for _, e := range doc.Infra {
			fmt.Fprintf(w, "  %s  %s  %d test(s), %d occurrence(s)\n", e.Key, e.Reason, len(e.Members), len(e.Occurrences))
		}
	}
	if len(doc.Suggested) > 0 {
		fmt.Fprintln(w, "\nNext:")
		for _, s := range doc.Suggested {
			fmt.Fprintln(w, "  "+s)
		}
	}
}

func flakeShortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	if s == "" {
		return "-"
	}
	return s
}

func newTestFlakesGateCmd() *cobra.Command {
	var (
		base   string
		full   bool
		cpu    string
		graded bool
	)
	cmd := &cobra.Command{
		Use:   "gate [--base <rev>] [--full] [--cpu 1,2,4] [--graded] [-- <go test command>]",
		Short: "Run every test the change adds or edits three times (the flake gate)",
		Long: `Finds the top-level Test and Fuzz functions the diff from --base to HEAD
adds or edits (a +func line, or a hunk inside the function) and runs them
three times, in one invocation of the project's own ` + "`go test`" + `:

  go test -json -short -count=3 -timeout 10m [-race] -run '^(TestA|TestB)$' ./pkg...

--full adds -race. --cpu 1,2,4 runs each test once per GOMAXPROCS value
instead of -count=3, which Go's test cache keeps. A command after -- replaces
` + "`go test`" + ` (a project wrapper such as scripts/ci-go-test.sh); the gate adds
nothing to it beyond those flags, the -run pattern and the packages. Test
files under the e2e or integration build tag, and files outside the module's
own packages, are skipped.

The run is recorded as lane flake-gate of a run under .forge/tests/runs/
($FORGE_TEST_RUN joins an existing one), so its retry passes feed
` + "`forge test flakes`" + `.

--base defaults to $CI_BASE, else the merge-base of HEAD with
origin/<default branch>.

Exit status: 0 every changed test passed every run; 1 a changed test failed
at least once; 2 no changed test could be identified (none in the diff, or
the diff could not be read). It reports; it blocks nothing by itself.
--graded exits as forge ci gate's flake-check step does: 1 only when
lint.rules grades flake-check error (for customers it is a warning), and 0
when there were no changed tests.`,
		Example: `  forge test flakes gate --base origin/main
  forge test flakes gate --base HEAD^1 --full -- scripts/ci-go-test.sh
  forge test flakes gate --cpu 1,2,4`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root := testRoot()
			b := base
			if b == "" {
				b = defaultFlakeBase(cmd.Context(), root)
			}
			res := testflakes.Gate(cmd.Context(), testflakes.GateOptions{
				Root: root, Base: b, Full: full, CPU: cpu, Command: args,
				Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr(), Begin: testBegin(root),
			})
			return flakeGateVerdict(cmd.OutOrStdout(), res, graded, root)
		},
	}
	cmd.Flags().SetInterspersed(false)
	cmd.Flags().StringVar(&base, "base", "", "Revision the change is measured from (default $CI_BASE, else the merge-base with origin/<default branch>)")
	cmd.Flags().BoolVar(&full, "full", false, "Add -race (the main-branch and release tier)")
	cmd.Flags().StringVar(&cpu, "cpu", "", "Run each test once per GOMAXPROCS value (e.g. 1,2,4) instead of -count=3")
	cmd.Flags().BoolVar(&graded, "graded", false, "Exit as the flake-check gate step: fail only when lint.rules grades flake-check error")
	return cmd
}

// defaultFlakeBase is $CI_BASE, else the merge-base of HEAD with the remote's
// default branch (origin/HEAD, falling back to origin/main).
func defaultFlakeBase(ctx context.Context, root string) string {
	if b := strings.TrimSpace(os.Getenv(cigate.BaseEnv)); b != "" {
		return b
	}
	ref := "origin/main"
	if head, err := testflakes.ExecGit(ctx, root, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil && head != "" {
		ref = strings.TrimPrefix(head, "refs/remotes/")
	}
	if mb, err := testflakes.ExecGit(ctx, root, "merge-base", "HEAD", ref); err == nil && mb != "" {
		return mb
	}
	return ref
}

// flakeGateVerdict is the gate's exit status: its own, or under --graded the
// flake-check step's.
func flakeGateVerdict(w io.Writer, res testflakes.GateResult, graded bool, root string) error {
	if !graded {
		if res.ExitCode == 0 {
			fmt.Fprintln(w, res.Message)
			return nil
		}
		return exitCodeError{code: res.ExitCode, msg: res.Message, verdict: true}
	}
	switch res.ExitCode {
	case testflakes.GatePassed, testflakes.GateUndetermined:
		fmt.Fprintln(w, res.Message)
		return nil
	}
	cfg, _ := loadProjectConfigFrom(filepath.Join(root, "forge.yaml"))
	if lint.FlakeCheckGates(cfg) {
		return exitCodeError{code: exitWrong, msg: res.Message + " (flake-check: error in lint.rules)", verdict: true}
	}
	fmt.Fprintln(w, "warning: "+res.Message)
	fmt.Fprintln(w, "flake-check is a warning for this project; `lint.rules: {flake-check: error}` in forge.yaml makes it block.")
	return nil
}

func newTestFlakesConfirmCmd() *cobra.Command {
	var (
		run, repo, logs, sha, runURL, from string
		maxReruns                          int
		list, jsonOut                      bool
	)
	cmd := &cobra.Command{
		Use:   "confirm --run <ci-run-id> | --logs <dir> | --from <results.json> [-- <go test command>]",
		Short: "Rerun a red run's failing tests once to tell flakes from breaks (always exits 0)",
		Long: `Reads the failing Go tests of a red run and reruns each once, alone, under
-race, at the run's commit (run it from the module root of a checkout of that
commit), then classifies every failure:

  confirmed     failed, then passed on the rerun: a flake
  real          failed again (or did not run): a break, not a flake
  raced         the output reports a data race: a bug, never retried
  host          the output matches a host signature: host health, never retried
  correlated    one of 3+ failures sharing a package, harness or signature:
                one infra event, never retried
  inconclusive  the rerun could not tell (skipped, its package failed, over
                the --max budget, not in this module)
  unattributed  no test owns the failure (a build failure, TestMain)

The source is a GitHub Actions run (--run, read with ` + "`gh run view --log-failed`" + `
and its gate-logs / test-short-* artifacts; Windows and macOS jobs are
ignored), a local gate's log directory (--logs: its *.log files and go test
-json *.json streams), or a results file (--from: go test -json, JUnit or
Vitest; a lane of .forge/tests/runs/ keeps its run's commit and tree).

At most --max (15) distinct tests are rerun. The reruns are recorded as lane
confirm of a new run, with every verdict in confirm/confirm.json, so
` + "`forge test flakes`" + ` sees a confirmed flake. It always exits 0 and never changes
the verdict of the run it read.`,
		Example: `  forge test flakes confirm --run 1234567890
  forge test flakes confirm --logs .git/ci-gate/runs/test.abc123 --sha "$(git rev-parse HEAD)"
  forge test flakes confirm --from .forge/tests/runs/01JD…/unit/results.json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			n := 0
			for _, s := range []string{run, logs, from} {
				if s != "" {
					n++
				}
			}
			if n != 1 {
				return exitCodeError{code: exitWrong, msg: "forge test flakes confirm: name exactly one source: --run <ci-run-id>, --logs <dir> or --from <results file>"}
			}
			root := testRoot()
			stdout := cmd.OutOrStdout()
			if jsonOut {
				stdout = cmd.ErrOrStderr()
			}
			doc := testflakes.RunConfirm(cmd.Context(), testflakes.ConfirmOptions{
				Root: root, RunID: run, Repo: repo, LogsDir: logs, SHA: sha, RunURL: runURL, From: from,
				Max: maxReruns, List: list, Command: args,
				Stdout: stdout, Stderr: cmd.ErrOrStderr(), Begin: testBegin(root),
			})
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(doc)
			}
			c := doc.Counts
			fmt.Fprintf(stdout, "confirm: %d failing — %d confirmed, %d real, %d raced, %d host, %d correlated, %d inconclusive, %d unattributed\n",
				c.Failing, c.Confirmed, c.Real, c.Raced, c.Host, c.Correlated, c.Inconclusive, c.Unattributed)
			if doc.Run != "" {
				fmt.Fprintf(stdout, "recorded: %s\n", doc.Run)
			}
			for _, s := range doc.Suggested {
				fmt.Fprintf(stdout, "  next: %s\n", s)
			}
			return nil
		},
	}
	cmd.Flags().SetInterspersed(false)
	f := cmd.Flags()
	f.StringVar(&run, "run", "", "GitHub Actions run id to read (gh run view --log-failed)")
	f.StringVar(&repo, "repo", "", "With --run: owner/name (default $GITHUB_REPOSITORY, else the current repository)")
	f.StringVar(&logs, "logs", "", "A local gate's log directory (*.log, and go test -json *.json)")
	f.StringVar(&sha, "sha", "", "With --logs: the commit the logs are of (default HEAD)")
	f.StringVar(&runURL, "run-url", "", "With --logs: a link to the run, for the record")
	f.StringVar(&from, "from", "", "A results file: go test -json, JUnit XML or Vitest JSON")
	f.IntVar(&maxReruns, "max", testflakes.DefaultMaxReruns, "Most distinct tests to rerun")
	f.BoolVar(&list, "list", false, "Print the failing tests found and stop: nothing is rerun or recorded")
	f.BoolVar(&jsonOut, "json", false, "Print a "+testflakes.SchemaConfirm+" document on stdout (progress goes to stderr)")
	return cmd
}
