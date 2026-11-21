package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/exitcode"
	"github.com/reliant-labs/forge/internal/schemagen"
	"github.com/reliant-labs/forge/internal/testcapture"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// These tests answer the Taskfile through fakes of `task --list-all` and
// `task <lane>`, and read a fixed clock: nothing execs task, git or go.

// trRunEpoch is the fixed clock's start; each reading advances it a second.
var trRunEpoch = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

// trTaskLanes fakes the Taskfile: tasks lists its lanes, and running a lane
// records a capture of that name into $FORGE_TEST_RUN (when record is set)
// and exits with its code. It returns the lanes run, with the run dir each saw.
func trTaskLanes(t *testing.T, tasks []string, codes map[string]int, record map[string]string) *[]string {
	t.Helper()
	root := t.TempDir()
	trWrite(t, filepath.Join(root, "forge.yaml"), "name: shop\n")
	t.Chdir(root)
	var ran []string
	prevList, prevExec, prevProv, prevNow, prevSig := testRunListTasks, testRunTaskExec, testRunProvenance, testRunNow, testRunSignals
	clock := trRunEpoch
	testRunNow = func() time.Time {
		clock = clock.Add(time.Second)
		return clock
	}
	testRunSignals = nil
	testRunListTasks = func(context.Context, string) (map[string]bool, error) {
		out := map[string]bool{}
		for _, n := range tasks {
			out[n] = true
		}
		return out, nil
	}
	testRunTaskExec = func(_ context.Context, _ string, env []string, stdout, _ io.Writer, lane string) (int, error) {
		run := ""
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, testcapture.EnvRun+"="); ok {
				run = v
			}
		}
		ran = append(ran, lane+"@"+run)
		fmt.Fprintf(stdout, "lane %s output\n", lane)
		if capture := record[lane]; capture != "" && run != "" {
			now := trRunEpoch
			trJSON(t, filepath.Join(run, capture, testreport.LaneManifestFile), testreport.LaneManifest{
				Schema: testreport.LaneManifestSchema, Lane: capture, ExitCode: codes[lane], StartedAt: now, FinishedAt: now,
				Format: testreport.LaneFormatGoJSON, Results: testreport.LaneResultsParsed,
			})
		}
		if codes[lane] < 0 {
			return -1, errors.New("cannot start")
		}
		return codes[lane], nil
	}
	testRunProvenance = func(context.Context, string) (release.Provenance, error) {
		return release.Provenance{Commit: trCommit}, nil
	}
	t.Cleanup(func() {
		testRunListTasks, testRunTaskExec, testRunProvenance, testRunNow, testRunSignals = prevList, prevExec, prevProv, prevNow, prevSig
	})
	return &ran
}

func trRunJSON(t *testing.T, args ...string) (testRunResultDoc, int, string) {
	t.Helper()
	out, code := trRun(t, newTestRunCmd(), append([]string{"--json"}, args...)...)
	var doc testRunResultDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not one JSON document (the lanes' output must go to stderr): %v\n%s", err, out)
	}
	if doc.Schema != schemaTestRunResult || doc.ExitCode != code || doc.OK != (code == 0) {
		t.Fatalf("head %+v disagrees with exit %d", doc.Head, code)
	}
	return doc, code, out
}

// The default lanes: test, then test:integration when the Taskfile has it,
// sharing one run.
func TestTestRunDefaultLanesShareOneRun(t *testing.T) {
	t.Setenv(testcapture.EnvRun, "")
	t.Setenv(testcapture.EnvTests, "")
	ran := trTaskLanes(t, []string{"test", "test:integration", "lint"}, nil, map[string]string{"test": "unit", "test:integration": "integration"})
	doc, code, _ := trRunJSON(t)
	if code != exitOK {
		t.Fatalf("exit %d: %+v", code, doc)
	}
	if len(*ran) != 2 || !strings.HasPrefix((*ran)[0], "test@") || !strings.HasPrefix((*ran)[1], "test:integration@") {
		t.Fatalf("ran %v", *ran)
	}
	run := strings.SplitN((*ran)[0], "@", 2)[1]
	if run == "" || (*ran)[1] != "test:integration@"+run || doc.Dir != run || !doc.Recording || doc.Run == "" {
		t.Fatalf("the lanes did not share one run: %v, doc %+v", *ran, doc)
	}
	if len(doc.Captured) != 2 || doc.Captured[0].Lane != "integration" || doc.Captured[1].Lane != "unit" {
		t.Fatalf("captured %+v", doc.Captured)
	}
	if len(doc.Suggested) == 0 || !strings.Contains(doc.Suggested[0], "forge test report "+doc.Run) {
		t.Fatalf("suggested %v", doc.Suggested)
	}

	// No test:integration: test alone.
	ran = trTaskLanes(t, []string{"test"}, nil, nil)
	if _, code, _ := trRunJSON(t); code != exitOK || len(*ran) != 1 {
		t.Fatalf("exit %d, ran %v", code, *ran)
	}
}

func TestTestRunExitCodes(t *testing.T) {
	t.Setenv(testcapture.EnvRun, "")
	t.Setenv(testcapture.EnvTests, "")

	// A failing lane fails the run, and the lanes after it still run.
	ran := trTaskLanes(t, []string{"test", "test:integration"}, map[string]int{"test": 3}, nil)
	doc, code, _ := trRunJSON(t)
	if code != exitWrong || len(*ran) != 2 || doc.Tasks[0].ExitCode != 3 || doc.Tasks[1].ExitCode != 0 || !strings.Contains(doc.Error, "test (exit 3)") {
		t.Fatalf("exit %d, ran %v, doc %+v", code, *ran, doc)
	}
	if len(doc.Notes) == 0 || !strings.Contains(strings.Join(doc.Notes, " "), "forge test capture") {
		t.Fatalf("no note that nothing was captured: %v", doc.Notes)
	}

	// A lane that cannot start is a failed lane.
	trTaskLanes(t, []string{"test"}, map[string]int{"test": -1}, nil)
	if doc, code, _ := trRunJSON(t); code != exitWrong || doc.Tasks[0].Error == "" {
		t.Fatalf("exit %d: %+v", code, doc)
	}

	// A lane the Taskfile does not have refuses the run before anything runs.
	ran = trTaskLanes(t, []string{"test", "test:unit"}, nil, nil)
	doc, code, _ = trRunJSON(t, "test", "test:e2e")
	if code != exitRefused || len(*ran) != 0 || !strings.Contains(doc.Error, "no lane test:e2e") || !strings.Contains(doc.Notes[0], "test, test:unit") {
		t.Fatalf("exit %d, ran %v, doc %+v", code, *ran, doc)
	}
	// So does a Taskfile with no `test` lane, when none was named.
	trTaskLanes(t, []string{"build"}, nil, nil)
	if doc, code, _ := trRunJSON(t); code != exitRefused || !strings.Contains(doc.Error, "the default") {
		t.Fatalf("exit %d: %+v", code, doc)
	}
	// And a project whose Taskfile cannot be listed (no task binary).
	trTaskLanes(t, []string{"test"}, nil, nil)
	testRunListTasks = func(context.Context, string) (map[string]bool, error) {
		return nil, errors.New("`task` is not installed")
	}
	if doc, code, _ := trRunJSON(t); code != exitRefused || !strings.Contains(doc.Error, "not installed") {
		t.Fatalf("exit %d: %+v", code, doc)
	}
}

// An interruption during the first lane is not a failed run: every
// requested lane is listed, the cut-short and never-started ones canceled,
// and the run exits 2 (undetermined), not 1.
func TestTestRunInterruptedListsEveryLane(t *testing.T) {
	t.Setenv(testcapture.EnvRun, "")
	t.Setenv(testcapture.EnvTests, "")
	ran := trTaskLanes(t, []string{"test", "test:integration"}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inner := testRunTaskExec
	testRunTaskExec = func(c context.Context, root string, env []string, stdout, stderr io.Writer, lane string) (int, error) {
		_, _ = inner(c, root, env, stdout, stderr, lane)
		cancel() // the interrupt lands while the first lane runs
		return -1, errors.New("signal: interrupt")
	}
	cmd := newTestRunCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"--json"})
	code := exitcode.Of(cmd.ExecuteContext(ctx))
	var doc testRunResultDoc
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if code != exitUndetermined || doc.ExitCode != exitUndetermined || doc.OK {
		t.Fatalf("exit %d, doc %+v", code, doc.Head)
	}
	if len(*ran) != 1 || len(doc.Tasks) != 2 {
		t.Fatalf("ran %v, tasks %+v", *ran, doc.Tasks)
	}
	first, second := doc.Tasks[0], doc.Tasks[1]
	if first.Task != "test" || !first.Canceled || first.DurationMS != 1000 || second.Task != "test:integration" || !second.Canceled || second.ExitCode != -1 || second.DurationMS != 0 {
		t.Fatalf("tasks %+v", doc.Tasks)
	}
	if !strings.HasPrefix(doc.Error, "UNDETERMINED: interrupted") || strings.Contains(doc.Error, "FAIL") {
		t.Fatalf("error %q", doc.Error)
	}
}

// Text output: the lanes' own output, then the summary and the verdict.
func TestTestRunText(t *testing.T) {
	t.Setenv(testcapture.EnvRun, "")
	t.Setenv(testcapture.EnvTests, "")
	trTaskLanes(t, []string{"test"}, nil, map[string]string{"test": "unit"})
	out, code := trRun(t, newTestRunCmd())
	if code != exitOK || !strings.Contains(out, "lane test output") || !strings.Contains(out, "Recorded lanes: unit (exit 0)") || !strings.HasSuffix(out, "PASS: every lane exited 0\n") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// The verb runs only the project's lanes, and says so first.
func TestTestRunHelpSaysItNeverRunsGoTest(t *testing.T) {
	// Not parallel: newTestRootCmd resets package state (the recorded route).
	cmd := newTestRunCmd()
	first, _, _ := strings.Cut(cmd.Long, "\n")
	for name, text := range map[string]string{"Short": cmd.Short, "the first line of Long": first} {
		if !strings.Contains(text, "project's own Taskfile lanes") || !strings.Contains(text, "never runs `go test` itself") {
			t.Errorf("%s = %q", name, text)
		}
	}
	if !strings.Contains(cmd.Long, "`test`") || !strings.Contains(cmd.Long, "`test:integration`") {
		t.Error("the help does not name the default lanes")
	}
	found, _, err := newTestRootCmd(t).Find([]string{"test", "run"})
	if err != nil || found.Name() != "run" {
		t.Errorf("forge test run is not registered: %v", err)
	}
}

func TestTestRunMatchesItsPublishedSchema(t *testing.T) {
	t.Setenv(testcapture.EnvRun, "")
	t.Setenv(testcapture.EnvTests, "")
	trTaskLanes(t, []string{"test"}, map[string]int{"test": 1}, map[string]string{"test": "unit"})
	_, _, out := trRunJSON(t)
	var schema bytes.Buffer
	if err := runProjectSchema(&schema, "test-run-result"); err != nil {
		t.Fatal(err)
	}
	compiled, err := schemagen.Compile(schema.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	v, err := schemagen.DecodeJSON([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if errs := compiled.Validate(v); len(errs) > 0 {
		t.Errorf("does not validate: %v\n%s", errs, out)
	}
}

func TestParseTaskList(t *testing.T) {
	t.Parallel()
	got, err := parseTaskList([]byte(`{"tasks":[{"name":"test","task":"test","aliases":["t"]},{"name":"sub:x","task":"sub:x","aliases":[]}]}`))
	if err != nil || !got["test"] || !got["t"] || !got["sub:x"] || len(got) != 3 {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := parseTaskList([]byte("task: not json")); err == nil {
		t.Fatal("garbage parsed")
	}
}
