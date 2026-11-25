package testcapture

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testreport"
)

// The lane commands these tests capture are this test binary, re-executed
// with helperEnv naming what to do: no shell, no go, no git.
const helperEnv = "FORGE_TESTCAPTURE_HELPER"

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		os.Exit(helperMain(mode))
	}
	os.Exit(m.Run())
}

// helperStream is what the "stream" helper prints: a go test -json stream with
// a secret in one event's output, and a line that is not an event.
var helperStream = []string{
	`{"Action":"start","Package":"example.com/p"}`,
	`{"Action":"run","Package":"example.com/p","Test":"TestA"}`,
	`{"Action":"output","Package":"example.com/p","Test":"TestA","Output":"=== RUN   TestA\n"}`,
	`{"Action":"output","Package":"example.com/p","Test":"TestA","Output":"    a_test.go:3: dsn postgres://app:hunter22@db/x\n"}`,
	`{"Action":"output","Package":"example.com/p","Test":"TestA","Output":"--- FAIL: TestA (0.00s)\n"}`,
	`{"Action":"fail","Package":"example.com/p","Test":"TestA"}`,
	`not an event: password=hunter22`,
	`{"Action":"output","Package":"example.com/p","Output":"FAIL\texample.com/p\t0.01s\n"}`,
	`{"Action":"fail","Package":"example.com/p"}`,
}

func helperMain(mode string) int {
	name, arg, _ := strings.Cut(mode, ":")
	switch name {
	case "exit":
		code, _ := strconv.Atoi(arg)
		return code
	case "stream":
		for _, l := range helperStream {
			fmt.Println(l)
		}
		// The lane's own command writes its coverage into the lane dir.
		_ = os.WriteFile(filepath.Join(os.Getenv(EnvLaneDir), testreport.LaneCoverProfile), []byte("mode: atomic\n"), 0o644)
		return 1
	case "env":
		fmt.Printf("%s|%s|%s\n", os.Getenv(EnvRun), os.Getenv(EnvLane), os.Getenv(EnvLaneDir))
		return 0
	case "stdin":
		_, _ = io.Copy(os.Stdout, os.Stdin)
		return 0
	case "junit":
		_ = os.WriteFile(arg, []byte("<testsuites>\n<testsuite name=\"x\"><testcase name=\"t\"><failure>token=abcdefgh</failure></testcase></testsuite>\n</testsuites>\n"), 0o644)
		return 0
	case "wait-term":
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGTERM)
		fmt.Println("ready")
		select {
		case <-sigs:
			return 42
		case <-time.After(time.Minute):
			return 2
		}
	}
	return 99
}

type harness struct {
	t      *testing.T
	root   string
	run    string
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, root: t.TempDir()}
	res := Begin(context.Background(), fakeBegin(h.root, nil))
	if !res.Recording {
		t.Fatalf("begin: %+v", res)
	}
	h.run = res.Dir
	return h
}

func (h *harness) opts(lane, mode string, env map[string]string) CaptureOptions {
	return CaptureOptions{
		RunDir:  h.run,
		Lane:    lane,
		Command: []string{os.Args[0], "-test.run=^$"},
		Environ: append(os.Environ(), helperEnv+"="+mode),
		Getenv:  envOf(env),
		Stdin:   strings.NewReader(""),
		Stdout:  &h.stdout,
		Stderr:  &h.stderr,
		Begin:   fakeBegin(h.root, env),
	}
}

func TestCaptureRecordsAGoTestStream(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	res := Capture(context.Background(), h.opts("unit", "stream", nil))
	if res.ExitCode != 1 || res.StartErr != nil {
		t.Fatalf("Capture = %+v, want the command's exit status 1", res)
	}
	laneDir := filepath.Join(h.run, "unit")
	if res.LaneDir != laneDir {
		t.Fatalf("lane dir %q, want %q", res.LaneDir, laneDir)
	}

	// The terminal gets go test's text, unscrubbed: it is the human's own.
	wantOut := "--- FAIL: TestA (0.00s)\n    a_test.go:3: dsn postgres://app:hunter22@db/x\nnot an event: password=hunter22\nFAIL\texample.com/p\t0.01s\n"
	if h.stdout.String() != wantOut {
		t.Fatalf("terminal:\n%q\nwant:\n%q", h.stdout.String(), wantOut)
	}

	// The stored stream is every line, scrubbed, the events kept as events.
	stored, err := os.ReadFile(filepath.Join(laneDir, testreport.LaneResultsFile))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(stored), "\n"), "\n")
	if len(lines) != len(helperStream) || strings.Contains(string(stored), "hunter22") {
		t.Fatalf("results.json:\n%s", stored)
	}
	if lines[3] != `{"Action":"output","Package":"example.com/p","Test":"TestA","Output":"    a_test.go:3: dsn postgres://[redacted]@db/x\n"}` ||
		lines[6] != `not an event: password=[redacted]` || lines[0] != helperStream[0] {
		t.Fatalf("scrubbed lines:\n%s\n%s", lines[3], lines[6])
	}

	m, err := testreport.ReadLaneManifest(h.run, "unit")
	if err != nil {
		t.Fatal(err)
	}
	if m.ExitCode != 1 || m.Format != testreport.LaneFormatGoJSON || m.Results != testreport.LaneResultsParsed ||
		m.ResultsFile != testreport.LaneResultsFile || m.Stream == nil || *m.Stream != (testreport.StreamStats{Lines: 9, Events: 8, Scrubbed: 2}) ||
		len(m.Coverage) != 1 || m.Coverage[0] != (testreport.LaneCoverageInput{Path: "cover.out", Format: testreport.CoverageGoProfile}) ||
		m.StartedAt.IsZero() || m.FinishedAt.Before(m.StartedAt) || len(m.Warnings) != 0 || m.Command[0] != os.Args[0] {
		t.Fatalf("capture.json = %+v (stream %+v)", m, m.Stream)
	}
	run, err := testreport.ReadRunManifest(h.run)
	if err != nil || len(run.Lanes) != 1 || run.Lanes[0] != "unit" {
		t.Fatalf("run.json lanes = %v, %v", run.Lanes, err)
	}
}

func TestCaptureExportsTheRunToItsCommand(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	if res := Capture(context.Background(), h.opts("env-lane", "env", nil)); res.ExitCode != 0 {
		t.Fatalf("Capture = %+v\n%s", res, h.stderr.String())
	}
	want := h.run + "|env-lane|" + filepath.Join(h.run, "env-lane") + "\n"
	if h.stdout.String() != want {
		t.Fatalf("the command saw %q, want %q", h.stdout.String(), want)
	}
	// Not a go test -json stream: stored as-is and said so.
	m, err := testreport.ReadLaneManifest(h.run, "env-lane")
	if err != nil || m.Results != testreport.LaneResultsUnparsed {
		t.Fatalf("capture.json = %+v, %v", m, err)
	}
}

func TestCapturePassesStdinThrough(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	o := h.opts("unit", "stdin", nil)
	o.Stdin = strings.NewReader("from the caller\n")
	if res := Capture(context.Background(), o); res.ExitCode != 0 || h.stdout.String() != "from the caller\n" {
		t.Fatalf("Capture = %+v, stdout %q", res, h.stdout.String())
	}
}

// The exit status is the command's whatever happens to the record.
func TestCaptureExitStatusIsTheCommands(t *testing.T) {
	t.Parallel()
	for _, code := range []int{0, 1, 3, 125} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			if res := Capture(context.Background(), h.opts("unit", "exit:"+strconv.Itoa(code), nil)); res.ExitCode != code {
				t.Fatalf("recorded: exit %d, want %d", res.ExitCode, code)
			}

			// An unwritable run: the command still runs, with its status.
			bad := newHarness(t)
			o := bad.opts("unit", "exit:"+strconv.Itoa(code), nil)
			o.RunDir = filepath.Join(bad.run, testreport.RunManifestFile, "nested")
			if res := Capture(context.Background(), o); res.ExitCode != code || res.LaneDir != "" {
				t.Fatalf("unrecordable: %+v, want exit %d with nothing recorded", res, code)
			}
			if !strings.Contains(bad.stderr.String(), "warning") {
				t.Fatalf("no warning for the run it could not record:\n%s", bad.stderr.String())
			}

			// An invalid lane name is the same: a warning, never a verdict.
			inv := newHarness(t)
			if res := Capture(context.Background(), inv.opts("../escape", "exit:"+strconv.Itoa(code), nil)); res.ExitCode != code || res.LaneDir != "" {
				t.Fatalf("invalid lane: %+v, want exit %d", res, code)
			}

			// FORGE_TESTS=off: the command alone.
			off := newHarness(t)
			if res := Capture(context.Background(), off.opts("unit", "exit:"+strconv.Itoa(code), map[string]string{EnvTests: "off"})); res.ExitCode != code {
				t.Fatalf("off: exit %d, want %d", res.ExitCode, code)
			}
			if _, err := os.Stat(filepath.Join(off.run, "unit", testreport.LaneManifestFile)); !os.IsNotExist(err) {
				t.Fatalf("FORGE_TESTS=off recorded the lane: %v", err)
			}
		})
	}
}

func TestCaptureCommandNotFoundIs127(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	o := h.opts("unit", "", nil)
	o.Command = []string{"forge-no-such-command-xyz"}
	res := Capture(context.Background(), o)
	if res.ExitCode != 127 || res.StartErr == nil {
		t.Fatalf("Capture = %+v, want 127", res)
	}
	m, err := testreport.ReadLaneManifest(h.run, "unit")
	if err != nil || m.ExitCode != 127 || m.Error == "" {
		t.Fatalf("capture.json = %+v, %v", m, err)
	}
}

func TestCaptureCopiesAResultsFile(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	report := filepath.Join(t.TempDir(), "report.xml")
	o := h.opts("web", "junit:"+report, nil)
	o.Results = report
	if res := Capture(context.Background(), o); res.ExitCode != 0 {
		t.Fatalf("Capture = %+v", res)
	}
	m, err := testreport.ReadLaneManifest(h.run, "web")
	if err != nil || m.Format != testreport.LaneFormatJUnit || m.Results != testreport.LaneResultsParsed || m.ResultsFile != "junit.xml" || m.Stream != nil {
		t.Fatalf("capture.json = %+v, %v", m, err)
	}
	stored, err := os.ReadFile(filepath.Join(h.run, "web", "junit.xml"))
	if err != nil || !strings.Contains(string(stored), "token=[redacted]") {
		t.Fatalf("junit.xml = %q, %v: want the copy, scrubbed", stored, err)
	}

	// A report the command never wrote is recorded missing, not a failure.
	h2 := newHarness(t)
	o2 := h2.opts("web", "exit:0", nil)
	o2.Results = filepath.Join(t.TempDir(), "never.xml")
	if res := Capture(context.Background(), o2); res.ExitCode != 0 {
		t.Fatalf("Capture = %+v", res)
	}
	if m, err := testreport.ReadLaneManifest(h2.run, "web"); err != nil || m.Results != testreport.LaneResultsMissing || m.ResultsFile != "" {
		t.Fatalf("capture.json = %+v, %v", m, err)
	}
}

// Capturing a lane again in one run replaces it: the earlier capture's
// coverage is not the new command's.
func TestCaptureReplacesAnEarlierCaptureOfTheLane(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	Capture(context.Background(), h.opts("unit", "stream", nil))
	if res := Capture(context.Background(), h.opts("unit", "exit:0", nil)); res.ExitCode != 0 {
		t.Fatalf("Capture = %+v", res)
	}
	m, err := testreport.ReadLaneManifest(h.run, "unit")
	if err != nil || len(m.Coverage) != 0 || m.ExitCode != 0 {
		t.Fatalf("capture.json = %+v, %v", m, err)
	}
	if run, _ := testreport.ReadRunManifest(h.run); len(run.Lanes) != 1 {
		t.Fatalf("run.json lanes = %v, want unit once", run.Lanes)
	}
}

// Without --run, FORGE_TEST_RUN is the run; with neither, capture begins one.
func TestCaptureFindsOrBeginsItsRun(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	o := h.opts("unit", "exit:0", map[string]string{EnvRun: h.run})
	o.RunDir = ""
	if res := Capture(context.Background(), o); res.LaneDir != filepath.Join(h.run, "unit") {
		t.Fatalf("with %s: lane dir %q", EnvRun, res.LaneDir)
	}

	fresh := newHarness(t)
	o = fresh.opts("unit", "exit:0", nil)
	o.RunDir = ""
	res := Capture(context.Background(), o)
	if res.LaneDir == "" || filepath.Dir(filepath.Dir(res.LaneDir)) != testreport.RunsRoot(fresh.root) || filepath.Dir(res.LaneDir) == fresh.run {
		t.Fatalf("with neither: lane dir %q, want a new run under %s", res.LaneDir, testreport.RunsRoot(fresh.root))
	}
}

func TestExecuteForwardsSignals(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("signals are not delivered to a child process on Windows")
	}
	// The helper prints "ready" once it listens for SIGTERM; the signal is
	// sent on seeing that line, so nothing here waits on a clock.
	sigs := make(chan os.Signal, 1)
	onReady := &readyWriter{fire: func() { sigs <- syscall.SIGTERM }}
	code, err := executeForwarding(context.Background(), []string{os.Args[0], "-test.run=^$"},
		append(os.Environ(), helperEnv+"=wait-term"), strings.NewReader(""), onReady, io.Discard, sigs)
	if err != nil || code != 42 {
		t.Fatalf("executeForwarding = %d, %v; want the command's own 42 after the forwarded SIGTERM", code, err)
	}

	// A command the forwarded signal kills exits 128+n.
	killed := make(chan os.Signal, 1)
	killed <- syscall.SIGKILL
	code, _ = executeForwarding(context.Background(), []string{os.Args[0], "-test.run=^$"},
		append(os.Environ(), helperEnv+"=wait-term"), strings.NewReader(""), io.Discard, io.Discard, killed)
	if code != 128+int(syscall.SIGKILL) {
		t.Fatalf("killed: exit %d, want %d", code, 128+int(syscall.SIGKILL))
	}
}

// Lanes of one run can finish at once: each append to run.json holds the
// run's lock, so none is lost.
func TestRecordLaneAppendsUnderTheLock(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	lanes := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	errs := make(chan error, len(lanes))
	for _, lane := range lanes {
		go func() {
			if err := os.MkdirAll(filepath.Join(h.run, lane), 0o755); err != nil {
				errs <- err
				return
			}
			errs <- RecordLane(h.run, testreport.LaneManifest{Lane: lane, Format: testreport.LaneFormatJUnit, Results: testreport.LaneResultsParsed})
		}()
	}
	for range lanes {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	run, err := testreport.ReadRunManifest(h.run)
	if err != nil || len(run.Lanes) != len(lanes) {
		t.Fatalf("run.json lanes = %v, %v; want all %d", run.Lanes, err, len(lanes))
	}
	m, err := testreport.ReadLaneManifest(h.run, "c")
	if err != nil || m.Schema != testreport.LaneManifestSchema || m.Coverage == nil {
		t.Fatalf("capture.json = %+v, %v", m, err)
	}
	if err := RecordLane(h.run, testreport.LaneManifest{Lane: "../x"}); err == nil {
		t.Fatal("RecordLane accepted a lane that is not a path segment")
	}
}

func TestResultsName(t *testing.T) {
	t.Parallel()
	for f, want := range map[testreport.LaneFormat]string{
		testreport.LaneFormatGoJSON: "results.json", testreport.LaneFormatJUnit: "junit.xml",
		testreport.LaneFormatVitest: "vitest.json", testreport.LaneFormatUnknown: "results.tap",
	} {
		if got := ResultsName(f, "out/report.TAP"); got != want {
			t.Errorf("ResultsName(%s) = %q, want %q", f, got, want)
		}
	}
}

// readyWriter calls fire once, when the command's output first holds "ready".
type readyWriter struct {
	buf   bytes.Buffer
	fire  func()
	fired bool
}

func (w *readyWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	if !w.fired && strings.Contains(w.buf.String(), "ready") {
		w.fired = true
		w.fire()
	}
	return len(p), nil
}
