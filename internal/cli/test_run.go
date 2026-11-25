package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/testcapture"
	"github.com/reliant-labs/forge/internal/testlocal"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// schemaTestRunResult names `forge test run --json`.
const schemaTestRunResult = "forge.test.run-result/v1"

// The lanes `forge test run` runs when none is named: the project's test
// suite, and its integration lane when the Taskfile has one.
const (
	testRunDefaultLane     = "test"
	testRunIntegrationLane = "test:integration"
)

// testRunResultDoc is `forge test run --json`.
type testRunResultDoc struct {
	testreport.Head
	// Run is the run's id and Dir its directory; empty when nothing was
	// recorded (FORGE_TESTS=off, or the run could not be created).
	Run string `json:"run,omitempty"`
	Dir string `json:"dir,omitempty"`
	// Recording says whether the lanes recorded into a run at all.
	Recording bool `json:"recording"`
	// Tasks are the Taskfile lanes run, in order, each with its own exit
	// code: the verdict is theirs.
	Tasks []testRunTask `json:"tasks"`
	// Captured are the lanes the tasks recorded into the run (a task that
	// does not wrap its command in `forge test capture` records none).
	Captured  []testRunLaneEntry `json:"captured"`
	Notes     []string           `json:"notes,omitempty"`
	Suggested []string           `json:"suggested,omitempty"`
}

// testRunTask is one `task <lane>` the run ran.
type testRunTask struct {
	Task string `json:"task"`
	// ExitCode is the task's own exit status (-1 when it could not start).
	ExitCode   int   `json:"exit_code"`
	DurationMS int64 `json:"duration_ms"`
	// Error says why the task could not start.
	Error string `json:"error,omitempty"`
	// Canceled: the run was interrupted before this task finished. A task
	// never started has exit_code -1 and duration 0; one that was running
	// has whatever exit status the interruption left it.
	Canceled bool `json:"canceled,omitempty"`
}

// The seams `forge test run` reaches the Taskfile through: the task binary
// lists the tasks (includes resolved, exactly as `task` itself sees them)
// and runs one. Tests swap them.
var (
	testRunListTasks = listTaskfileTasks
	testRunTaskExec  = execTaskLane
	// testRunProvenance reads the run's git facts; nil is begin's own.
	testRunProvenance func(ctx context.Context, dir string) (release.Provenance, error)
	// testRunNow is the clock lane durations and the run's id are read from.
	testRunNow = time.Now
	// testRunSignals interrupt the run. forge stays up on them to mark what
	// did not finish and write the document (the running `task` gets the
	// terminal's Ctrl-C too); a test empties it and cancels the context.
	testRunSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}
)

// newTestRunCmd is `forge test run`: the project's own Taskfile lanes, run as
// one recorded run. It is not the `forge test` that was removed (ci_test_run.go
// says why that one was): it runs no suite of its own, only `task <lane>`.
func newTestRunCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "run [<lane>...]",
		Short: "Run the project's own Taskfile lanes (`task <lane>`) as one recorded run; never runs `go test` itself",
		Long: `Runs the project's own Taskfile lanes (` + "`task <lane>`" + `) as one recorded run; it never runs ` + "`go test`" + ` itself.

The Taskfile decides how each lane runs. forge allocates one run with
` + "`forge test begin`" + ` and runs every lane with FORGE_TEST_RUN set to it, so the
lanes' own ` + "`forge test capture`" + ` steps all record into that one run. A lane that
does not capture still runs; it just records nothing.

Lanes are Taskfile task names. With none named, it runs ` + "`test`" + `, then
` + "`test:integration`" + ` when the Taskfile has it. Every lane runs even after one fails,
so a failing unit lane does not hide the integration lane's result.

Exit codes:
  0  every lane exited 0
  1  a lane failed (each lane's own exit code is in --json)
  2  interrupted before every lane finished: the run proves nothing either
     way (--json lists every requested lane; the ones cut short or never
     started are canceled)
  4  refused, nothing run: the Taskfile has no such lane, or ` + "`task`" + ` is not
     installed

Then ` + "`forge test report <run>`" + ` summarises what the lanes recorded.`,
		Example: `  forge test run
  forge test run test:integration
  forge test run test test:e2e --json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTestRun(cmd, args, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print a "+schemaTestRunResult+" document (the lanes' own output goes to stderr)")
	return cmd
}

func runTestRun(cmd *cobra.Command, lanes []string, asJSON bool) error {
	ctx := cmd.Context()
	if len(testRunSignals) > 0 {
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(ctx, testRunSignals...)
		defer stop()
	}
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	laneOut := out
	if asJSON {
		laneOut = errOut
	}
	doc := testRunResultDoc{Head: testreport.Head{Schema: schemaTestRunResult}, Tasks: []testRunTask{}, Captured: []testRunLaneEntry{}}
	finish := func(code int, msg string) error {
		doc.ExitCode, doc.OK = code, code == exitOK
		if code != exitOK {
			doc.Error = msg
		}
		if asJSON {
			if err := writeJSONDocument(out, doc); err != nil {
				return err
			}
		} else {
			renderTestRun(out, doc)
		}
		return verdictErr(code, msg)
	}

	root, err := testRecordRoot(ctx, "forge test run")
	if err != nil {
		return err
	}
	available, err := testRunListTasks(ctx, root)
	if err != nil {
		doc.Suggested = []string{"task --list-all"}
		return finish(exitRefused, "REFUSED: cannot read the project's Taskfile lanes: "+err.Error())
	}
	explicit := len(lanes) > 0
	if !explicit {
		lanes = []string{testRunDefaultLane}
		if available[testRunIntegrationLane] {
			lanes = append(lanes, testRunIntegrationLane)
		}
	}
	var missing []string
	for _, l := range lanes {
		if !available[l] {
			missing = append(missing, l)
		}
	}
	if len(missing) > 0 {
		doc.Notes = []string{"lanes in this Taskfile: " + strings.Join(testLaneCandidates(available), ", ")}
		what := "the Taskfile has no lane " + strings.Join(missing, ", ")
		if !explicit {
			what += " (the default; name the lanes to run)"
		}
		return finish(exitRefused, "REFUSED: "+what+"; forge test run runs only the project's own Taskfile lanes")
	}

	begun := testcapture.Begin(ctx, testcapture.BeginOptions{Dir: root, ProjectRoot: root, ForgeVersion: buildinfo.Version(), Provenance: testRunProvenance, Now: testRunNow})
	for _, w := range begun.Warnings {
		fmt.Fprintf(errOut, "forge test run: warning: %s\n", w)
	}
	env := os.Environ()
	if begun.Recording {
		doc.Recording, doc.Dir = true, begun.Dir
		env = append(env, testcapture.EnvRun+"="+begun.Dir)
	}

	tasks, failed, canceled := runTestRunLanes(ctx, root, env, laneOut, errOut, lanes)
	doc.Tasks = tasks

	if begun.Recording {
		if r, err := testlocal.Open(begun.Dir); err != nil {
			doc.Notes = append(doc.Notes, "the run could not be read back: "+err.Error())
		} else {
			doc.Run = r.ID
			doc.Captured = runRow(r).Lanes
			if len(doc.Captured) == 0 {
				doc.Notes = append(doc.Notes, "no lane recorded into the run: the tasks do not wrap their commands in `forge test capture`")
			} else {
				doc.Suggested = append(doc.Suggested, "reliant forge test report "+r.ID)
			}
		}
	} else {
		doc.Notes = append(doc.Notes, "nothing was recorded (FORGE_TESTS=off, or the run could not be created)")
	}

	switch {
	case len(canceled) > 0:
		msg := "UNDETERMINED: interrupted (" + ctx.Err().Error() + "): " + strings.Join(canceled, ", ") + " did not finish"
		if len(failed) > 0 {
			msg += "; failed before the interruption: " + strings.Join(failed, ", ")
		}
		return finish(exitUndetermined, msg)
	case len(failed) > 0:
		return finish(exitWrong, "FAIL: "+strings.Join(failed, ", "))
	}
	return finish(exitOK, "")
}

// runTestRunLanes runs each lane in order. It returns a row for every lane
// asked for, the lanes that failed (with their exit codes), and the lanes the
// interruption cut short or kept from starting.
func runTestRunLanes(ctx context.Context, root string, env []string, laneOut, errOut io.Writer, lanes []string) (tasks []testRunTask, failed, canceled []string) {
	tasks, failed, canceled = []testRunTask{}, []string{}, []string{}
	for _, lane := range lanes {
		if err := ctx.Err(); err != nil {
			// Every requested lane gets a row: one never started says so.
			tasks = append(tasks, testRunTask{Task: lane, ExitCode: -1, Error: "not started: the run was interrupted", Canceled: true})
			canceled = append(canceled, lane)
			continue
		}
		fmt.Fprintf(errOut, "forge test run: task %s\n", lane)
		started := testRunNow()
		code, err := testRunTaskExec(ctx, root, env, laneOut, errOut, lane)
		t := testRunTask{Task: lane, ExitCode: code, DurationMS: testRunNow().Sub(started).Milliseconds()}
		if err != nil {
			t.Error = err.Error()
		}
		switch {
		case code != 0 && ctx.Err() != nil:
			// Cut short by the interruption: not a verdict on the lane.
			t.Canceled = true
			canceled = append(canceled, lane)
		case code != 0:
			failed = append(failed, fmt.Sprintf("%s (exit %d)", lane, code))
		}
		tasks = append(tasks, t)
	}
	return tasks, failed, canceled
}

// testLaneCandidates are the Taskfile's tasks worth naming in a refusal: the
// test ones first, the rest only when there are none.
func testLaneCandidates(available map[string]bool) []string {
	var test, all []string
	for name := range available {
		all = append(all, name)
		if name == "test" || strings.HasPrefix(name, "test:") || strings.HasPrefix(name, "test-") {
			test = append(test, name)
		}
	}
	if len(test) == 0 {
		test = all
	}
	sort.Strings(test)
	if len(test) == 0 {
		return []string{"(none)"}
	}
	return test
}

func renderTestRun(w io.Writer, doc testRunResultDoc) {
	head := "forge test run"
	if doc.Run != "" {
		head += fmt.Sprintf(": run %s (%s)", doc.Run, doc.Dir)
	}
	fmt.Fprintln(w, head)
	for _, t := range doc.Tasks {
		line := fmt.Sprintf("  %-20s exit %d  %.1fs", t.Task, t.ExitCode, float64(t.DurationMS)/1000)
		if t.Canceled {
			line += "  canceled"
		}
		if t.Error != "" {
			line += "  " + t.Error
		}
		fmt.Fprintln(w, line)
	}
	if len(doc.Captured) > 0 {
		names := make([]string, 0, len(doc.Captured))
		for _, l := range doc.Captured {
			names = append(names, fmt.Sprintf("%s (exit %d)", l.Lane, l.ExitCode))
		}
		fmt.Fprintf(w, "Recorded lanes: %s\n", strings.Join(names, ", "))
	}
	for _, n := range doc.Notes {
		fmt.Fprintf(w, "Note: %s\n", n)
	}
	for _, s := range doc.Suggested {
		fmt.Fprintf(w, "  next: %s\n", s)
	}
	switch {
	case doc.OK:
		fmt.Fprintln(w, "PASS: every lane exited 0")
	default:
		fmt.Fprintln(w, doc.Error)
	}
}

// listTaskfileTasks asks `task` itself which tasks the project's Taskfile
// declares, so includes, aliases and namespaces mean what task says they
// mean. Listing evaluates no variables and runs no command.
func listTaskfileTasks(ctx context.Context, dir string) (map[string]bool, error) {
	if _, err := exec.LookPath("task"); err != nil {
		return nil, errors.New("`task` is not installed (https://taskfile.dev/installation), and forge runs the project's lanes only through it")
	}
	list := func(args ...string) ([]byte, error) {
		c := exec.CommandContext(ctx, "task", args...)
		c.Dir = dir
		var stderr strings.Builder
		c.Stderr = &stderr
		b, err := c.Output()
		if err != nil {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return nil, fmt.Errorf("%w: %s", err, msg)
			}
		}
		return b, err
	}
	b, err := list("--list-all", "--json", "--no-status")
	if err != nil {
		// A task that predates --no-status.
		if b, err = list("--list-all", "--json"); err != nil {
			return nil, fmt.Errorf("task --list-all: %w", err)
		}
	}
	return parseTaskList(b)
}

// parseTaskList reads `task --list-all --json`.
func parseTaskList(b []byte) (map[string]bool, error) {
	var doc struct {
		Tasks []struct {
			Name    string   `json:"name"`
			Task    string   `json:"task"`
			Aliases []string `json:"aliases"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("task --list-all --json: %w", err)
	}
	out := make(map[string]bool, len(doc.Tasks))
	for _, t := range doc.Tasks {
		for _, n := range append([]string{t.Name, t.Task}, t.Aliases...) {
			if n != "" {
				out[n] = true
			}
		}
	}
	return out, nil
}

// execTaskLane runs `task <lane>` in dir and returns its exit status. err is
// set only when the task could not be started (code -1).
func execTaskLane(ctx context.Context, dir string, env []string, stdout, stderr io.Writer, lane string) (int, error) {
	c := exec.CommandContext(ctx, "task", lane)
	c.Dir, c.Env, c.Stdout, c.Stderr, c.Stdin = dir, env, stdout, stderr, os.Stdin
	err := c.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), nil
	}
	return -1, err
}
