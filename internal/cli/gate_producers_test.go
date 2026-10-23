package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cli/lint"
	"github.com/reliant-labs/forge/internal/testreport"
	"github.com/reliant-labs/forge/pkg/release"
)

// readGate reads a written gate document back through the parser that will
// consume it in production. Asserting on the PARSED gate rather than on the
// file's bytes is the point: it pins the round trip the pipeline actually
// performs (`--gate-json` then `gate record --from`), so a producer that
// writes a document the parser cannot read fails here.
func readGate(t *testing.T, path string) release.Gate {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read gate document: %v", err)
	}
	gate, err := gateFromDocument(data, "")
	if err != nil {
		t.Fatalf("the written document is not readable as a gate: %v\n%s", err, data)
	}
	return gate
}

// ─── ci verify-test-run ─────────────────────────────────────────────────────

// The four test-run states map onto the closed set, and the counts ride
// along. UNDETERMINED → `error` is the one that matters most: "could not
// obtain the facts" must not be recorded as a verdict about the release.
func TestWriteTestRunGate_StatesMapOntoTheClosedSet(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		analysis   testreport.Analysis
		wantStatus release.GateStatus
		wantInSum  string
	}{
		{
			name: "a clean run passes",
			analysis: testreport.Analysis{Totals: testreport.Totals{
				Packages: 12, Tests: 412, Passed: 409, Skipped: 3,
			}},
			wantStatus: release.GateStatusPassed,
			wantInSum:  "409 passed, 0 failed, 3 skipped of 412 test(s)",
		},
		{
			name: "failing packages fail",
			analysis: testreport.Analysis{Totals: testreport.Totals{
				Packages: 12, Tests: 412, Passed: 400, Failed: 12,
				SuiteFailed: true, FailedPackages: 2,
			}},
			wantStatus: release.GateStatusFailed,
			wantInSum:  "2 package(s) failed",
		},
		{
			name: "an unreadable stream is an ERROR, not a failure",
			analysis: testreport.Analysis{
				Undetermined: []testreport.Unknown{{Reason: "no events"}},
			},
			wantStatus: release.GateStatusError,
			wantInSum:  "UNDETERMINED",
		},
		{
			name:       "a stream with no tests is SKIPPED, not a pass",
			analysis:   testreport.Analysis{},
			wantStatus: release.GateStatusSkipped,
			wantInSum:  "proved nothing",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "test.json")
			if err := writeTestRunGate(path, tc.analysis, time.Now().Add(-time.Minute)); err != nil {
				t.Fatal(err)
			}
			gate := readGate(t, path)
			if gate.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", gate.Status, tc.wantStatus)
			}
			if gate.Name != "test" {
				t.Errorf("name = %q, want test", gate.Name)
			}
			if !strings.Contains(gate.Summary, tc.wantInSum) {
				t.Errorf("summary = %q, want it to contain %q", gate.Summary, tc.wantInSum)
			}
		})
	}
}

// A skip-finding run (packages that skipped so much their pass proves
// nothing) is `failed` and says which rule fired — that is the finding the
// command exists to surface, and a gate reading just "failed" would send a
// reviewer back to re-run it.
func TestWriteTestRunGate_MassSkipSaysWhy(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "test.json")
	analysis := testreport.Analysis{
		Totals:   testreport.Totals{Packages: 3, Tests: 133, Passed: 9, Skipped: 124},
		Findings: []testreport.Finding{{Package: "internal/threads", Kind: testreport.KindFailed}},
	}
	if err := writeTestRunGate(path, analysis, time.Now()); err != nil {
		t.Fatal(err)
	}
	gate := readGate(t, path)
	if gate.Status != release.GateStatusFailed {
		t.Fatalf("status = %q, want failed", gate.Status)
	}
	if !strings.Contains(gate.Summary, "proves nothing") {
		t.Errorf("summary = %q, want it to name the rule that fired", gate.Summary)
	}
}

// The check's WINDOW is recorded, which is what makes a gate a run stage with
// a duration.
func TestWriteTestRunGate_RecordsTheCheckWindow(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "test.json")
	started := time.Now().Add(-90 * time.Second)
	if err := writeTestRunGate(path, testreport.Analysis{
		Totals: testreport.Totals{Packages: 1, Tests: 1, Passed: 1},
	}, started); err != nil {
		t.Fatal(err)
	}
	gate := readGate(t, path)
	if gate.StartedAt == nil || gate.FinishedAt == nil {
		t.Fatalf("the window must be recorded: %+v", gate)
	}
	if gate.FinishedAt.Before(*gate.StartedAt) {
		t.Error("finished before started")
	}
}

// ─── build ──────────────────────────────────────────────────────────────────

func TestWriteBuildGate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name              string
		succeeded, failed int
		wantStatus        release.GateStatus
		wantInSum         string
	}{
		{"every target built", 4, 0, release.GateStatusPassed, "4 target(s) built, 0 failed"},
		{"a failed target fails", 3, 1, release.GateStatusFailed, "1 failed"},
		// A build that built NOTHING must not attest to artifacts that
		// do not exist — the same rule the other two producers follow.
		{"nothing matched is skipped", 0, 0, release.GateStatusSkipped, "nothing was built"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "build.json")
			if err := writeBuildGate(path, tc.succeeded, tc.failed, time.Now()); err != nil {
				t.Fatal(err)
			}
			gate := readGate(t, path)
			if gate.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", gate.Status, tc.wantStatus)
			}
			if gate.Name != "build" {
				t.Errorf("name = %q, want build", gate.Name)
			}
			if !strings.Contains(gate.Summary, tc.wantInSum) {
				t.Errorf("summary = %q, want %q", gate.Summary, tc.wantInSum)
			}
		})
	}
}

// ─── lint ───────────────────────────────────────────────────────────────────

// The lint → gate boundary: lint reports its verdict in its own three
// strings, and this package maps them onto the closed set. The mapping goes
// through release.ParseGateStatus, so a producer reporting something outside
// the set fails HERE rather than later at `gate record`.
func TestWriteGateFromRequest_MapsLintsVerdicts(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"passed", "failed", "skipped", "error"} {
		path := filepath.Join(t.TempDir(), "lint.json")
		err := writeGateFromRequest(lint.GateRequest{
			Path: path, Name: "lint", Status: status,
			Summary:   "3 gating linter(s) passed",
			Details:   map[string]any{"lanes_ran": 3},
			StartedAt: time.Now().Add(-time.Second), FinishedAt: time.Now(),
		})
		if err != nil {
			t.Fatalf("status %q: %v", status, err)
		}
		gate := readGate(t, path)
		if string(gate.Status) != status {
			t.Errorf("status = %q, want %q", gate.Status, status)
		}
		if gate.Details["lanes_ran"] != float64(3) {
			t.Errorf("details did not survive the round trip: %v", gate.Details)
		}
	}
}

// A verdict outside the closed set is refused AT THE PRODUCER, and no file is
// written — so a pipeline never carries a document that cannot be recorded.
func TestWriteGateFromRequest_RefusesAVerdictOutsideTheSet(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "lint.json")
	err := writeGateFromRequest(lint.GateRequest{Path: path, Name: "lint", Status: "green"})
	if err == nil {
		t.Fatal("a verdict outside the closed set must be refused")
	}
	if _, serr := os.Stat(path); serr == nil {
		t.Error("no document may be written for a refused verdict")
	}
}

// THE RUN IDENTITY is stamped from the CI environment, so a producer joins
// its gate to the pipeline attempt without every verb needing a --run-id —
// and it includes the ATTEMPT, so a re-run's evidence does not collide with
// the first attempt's under the server's (promotion, name, run id) key.
func TestWriteGateWithRun_StampsTheCIRunIncludingTheAttempt(t *testing.T) {
	previous := osGetenv
	t.Cleanup(func() { osGetenv = previous })
	osGetenv = func(key string) string {
		return map[string]string{
			"GITHUB_REPOSITORY":  "acme/app",
			"GITHUB_RUN_ID":      "42",
			"GITHUB_RUN_ATTEMPT": "3",
			"GITHUB_SERVER_URL":  "https://github.com",
		}[key]
	}

	path := filepath.Join(t.TempDir(), "gate.json")
	if err := writeGateWithRun(path, release.Gate{
		Name: "lint", Status: release.GateStatusPassed,
	}); err != nil {
		t.Fatal(err)
	}
	gate := readGate(t, path)
	if want := "github:acme/app/42/3"; gate.RunID != want {
		t.Fatalf("run id = %q, want %q (the attempt is part of the idempotency key)", gate.RunID, want)
	}
	if gate.URL == "" {
		t.Error("the run URL should be stamped too, so a reader can reach the report")
	}
}

// Outside CI there is no run, and that is not an error: a human at a terminal
// produces a gate that belongs to no run.
func TestWriteGateWithRun_NoCIEnvironmentIsFine(t *testing.T) {
	previous := osGetenv
	t.Cleanup(func() { osGetenv = previous })
	osGetenv = func(string) string { return "" }

	path := filepath.Join(t.TempDir(), "gate.json")
	if err := writeGateWithRun(path, release.Gate{
		Name: "build", Status: release.GateStatusPassed,
	}); err != nil {
		t.Fatal(err)
	}
	if gate := readGate(t, path); gate.RunID != "" {
		t.Errorf("run id = %q, want empty outside CI", gate.RunID)
	}
}

// The writer is installed at init, so --gate-json works from every entry
// point that builds the command tree — not only the one that happened to
// wire it.
func TestLintGateWriterIsWiredAtInit(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "lint.json")
	if err := lint.WriteGateForTest(lint.GateRequest{
		Path: path, Name: "lint", Status: "passed", Summary: "ok",
	}); err != nil {
		t.Fatalf("the gate writer is not wired: %v", err)
	}
	if readGate(t, path).Name != "lint" {
		t.Error("the document was not written through the installed writer")
	}
}
