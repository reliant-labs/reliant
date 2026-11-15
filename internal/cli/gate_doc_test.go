package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// The golden tests §3.3 asks for: gateFromDocument over a REAL document from
// each producing verb.
//
// The documents below are built by MARSHALLING THE PRODUCERS' OWN REPORT
// STRUCTS, not by hand-writing JSON that looks about right. That is the whole
// value of the test: a hand-written fixture pins what the test author believed
// the shape was, so a producer that renames a field keeps passing while the
// parser silently stops recognising its documents in the field. Going through
// the real struct means such a rename breaks this test at compile time.
func TestGateFromDocument_GoldenPerVerb(t *testing.T) {
	t.Parallel()

	// `forge env smoke --json` — the real report type from smoke.go.
	smokeDoc := smokeJSONReport{Env: "prod", Routes: []smokeJSONRoute{
		{Result: "PASS", Reason: smokeReasonReached, Host: "api.example.com", Path: "/"},
		{Result: "FAIL", Reason: smokeReasonTLS, Host: "web.example.com", Path: "/"},
	}}
	smokeDoc.Summary.Pass, smokeDoc.Summary.Fail, smokeDoc.Summary.OK = 1, 1, false

	// `forge env status --json` — the real envStatusDocument.
	bound := true
	envVerify := envStatusDocument{
		Env: "prod", Bound: bound, Release: "v1.4.0",
		Images: []imageVerification{{}, {}}, OK: true,
	}

	// `forge release verify --json`.
	relVerify := releaseVerifyReport{
		Release: "v1.4.0", Artifacts: []artifactVerification{{}, {}, {}},
		OK: false, Diagnostic: "1 artifact missing from its registry",
	}

	// `forge lint --gate-json` and `forge ci verify-test-run --gate-json`
	// write gate-shaped documents; those are covered by
	// TestGateFromDocument_GateShapeIsReadAsItself and by each producer's
	// own test. Here we cover the ENVELOPE path for lint's `--json`
	// document, which a pipeline may hand to `gate record` directly.
	lintJSON := map[string]any{
		"findings": []any{
			map[string]any{"severity": "error", "rule": "forge-config-deps", "message": "scalar Deps field"},
			map[string]any{"severity": "warning", "rule": "forge-tests", "message": "no test"},
		},
		"summary":   map[string]any{"errors": 1, "warnings": 1, "infos": 0, "total": 2},
		"ok":        false,
		"exit_code": exitWrong,
	}

	// `forge env status --wait --json` (§3.2, F3's document). Exit 5 (timed out,
	// still progressing) is `error`, NOT `failed` — see
	// TestGateFromDocument_ExitCodesThatAreNotFailures.
	waitJSON := map[string]any{
		"ok": false, "exit_code": exitTimedOut,
		"env": "prod", "phase": "progressing",
		"reason": "api has not reached the pinned digest",
	}

	cases := []struct {
		name        string
		doc         any
		wantName    string
		wantStatus  release.GateStatus
		wantSummary string // substring
	}{
		{"smoke", smokeDoc, "smoke", release.GateStatusFailed, "1 passed, 0 warned, 1 failed"},
		{"env verify", envVerify, "verify", release.GateStatusPassed, "release v1.4.0 over 2 declared image"},
		{"release verify", relVerify, "release-verify", release.GateStatusFailed, "1 artifact missing"},
		{"lint", lintJSON, "lint", release.GateStatusFailed, "1 error(s), 1 warning(s)"},
		{"wait", waitJSON, "wait", release.GateStatusError, "rollout progressing: api has not reached"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data, err := json.Marshal(tc.doc)
			if err != nil {
				t.Fatal(err)
			}
			gate, err := gateFromDocument(data, "")
			if err != nil {
				t.Fatalf("gateFromDocument(%s): %v\n  document: %s", tc.name, err, data)
			}
			if gate.Name != tc.wantName {
				t.Errorf("name = %q, want %q", gate.Name, tc.wantName)
			}
			if gate.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", gate.Status, tc.wantStatus)
			}
			if !strings.Contains(gate.Summary, tc.wantSummary) {
				t.Errorf("summary = %q, want it to contain %q", gate.Summary, tc.wantSummary)
			}
			// Every derived gate must be recordable.
			if err := gate.Validate(); err != nil {
				t.Errorf("derived gate does not validate: %v", err)
			}
		})
	}
}

// A document that already IS a gate is read as itself, through the STRICT
// status path — not re-derived from an envelope it does not have.
func TestGateFromDocument_GateShapeIsReadAsItself(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	finished := started.Add(90 * time.Second)
	want := release.Gate{
		Name: "test", Status: release.GateStatusPassed,
		URL: "https://ci.example/run/7", Summary: "412 passed, 0 failed, 3 skipped",
		StartedAt: &started, FinishedAt: &finished, RunID: "github:acme/app/7/1",
		Details: map[string]any{"passed": float64(412)},
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := gateFromDocument(data, "ignored-hint")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "test" || got.Status != release.GateStatusPassed {
		t.Fatalf("gate = %+v", got)
	}
	if got.Summary != want.Summary || got.URL != want.URL || got.RunID != want.RunID {
		t.Errorf("gate lost fields on the round trip: %+v", got)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(started) {
		t.Errorf("started_at = %v, want %v", got.StartedAt, started)
	}
}

// Two `summary` shapes must never be mistaken for one another. Release
// verify's {verified,failed,unverifiable,unreachable} decodes into the smoke
// probe as all-zeroes, which would read as "a smoke that probed nothing" —
// recording a SKIPPED gate over a release verify that actually found a
// missing artifact. The smoke probe therefore requires smoke's own keys.
func TestGateFromDocument_ReleaseVerifySummaryIsNotASmokeSummary(t *testing.T) {
	t.Parallel()
	data, _ := json.Marshal(releaseVerifyReport{
		Release: "v1.4.0", Artifacts: []artifactVerification{{}},
		OK: false, Diagnostic: "1 artifact missing from its registry",
	})
	gate, err := gateFromDocument(data, "")
	if err != nil {
		t.Fatal(err)
	}
	if gate.Name != "release-verify" {
		t.Fatalf("name = %q, want release-verify — a release verify must not read as a smoke", gate.Name)
	}
	if gate.Status != release.GateStatusFailed {
		t.Errorf("status = %q, want failed", gate.Status)
	}
}

// Conversely, a smoke that probed nothing emits `"routes": null`, and that is
// the run whose verdict matters most. It must still be recognised.
func TestGateFromDocument_SmokeWithNullRoutesIsStillASmoke(t *testing.T) {
	t.Parallel()
	doc := []byte(`{"env":"prod","routes":null,"summary":{"pass":0,"warn":0,"fail":0,"ok":true}}`)
	gate, err := gateFromDocument(doc, "")
	if err != nil {
		t.Fatalf("a checked-nothing smoke document must be recordable: %v", err)
	}
	if gate.Name != "smoke" || gate.Status != release.GateStatusSkipped {
		t.Fatalf("gate = %+v, want a skipped smoke", gate)
	}
}

// A gate document whose status is outside the closed set is REFUSED on the
// write path — the typo fails before anything is recorded, which is the
// reason ParseGateStatus and GateStatusFromStored are two functions.
func TestGateFromDocument_StatusOutsideTheSetIsRefused(t *testing.T) {
	t.Parallel()
	_, err := gateFromDocument([]byte(`{"name":"lint","status":"green"}`), "")
	if !errors.Is(err, release.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if !strings.Contains(err.Error(), "passed") {
		t.Errorf("the refusal must name the closed set, got %q", err)
	}
}

// EVERY NON-ZERO CODE EXCEPT 1 IS SOMETHING OTHER THAN `failed`, and this is
// the table's own stated rule rather than a refinement of it (exitcodes.go:
// "5 and 6 are deliberately NOT 1").
//
// Why this matters more than it looks: F6's composite runs
// `gate record … --from wait.json` under `if: always()`, and the ledger is
// APPEND-ONLY. A mapping that folded these into `failed` would append a
// permanent, unretractable "the release failed" gate to every slow rollout and
// every overtaken promote — releases that were fine.
func TestGateFromDocument_ExitCodesThatAreNotFailures(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		exitCode int
		want     release.GateStatus
		why      string
	}{
		"1 wrong is the ONLY failure": {
			exitWrong, release.GateStatusFailed,
			"we looked at the release and it is wrong",
		},
		"2 undetermined": {
			exitUndetermined, release.GateStatusError,
			"could not look — unreachable, auth refused, unobservable",
		},
		"3 conflict": {
			exitConflict, release.GateStatusError,
			"someone else moved the env; the check never judged the release",
		},
		"4 refused": {
			exitRefused, release.GateStatusError,
			"the write was declined; nothing was judged",
		},
		"5 timed out": {
			exitTimedOut, release.GateStatusError,
			"still progressing — we never saw it finish",
		},
		"6 superseded": {
			exitSuperseded, release.GateStatusSkipped,
			"the subject is gone; the check did not apply (C8 agrees)",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			doc, _ := json.Marshal(map[string]any{
				"ok": false, "exit_code": tc.exitCode,
				"phase": "progressing", "reason": "whatever the verb said",
			})
			gate, err := gateFromDocument(doc, "")
			if err != nil {
				t.Fatal(err)
			}
			if gate.Status != tc.want {
				t.Fatalf("exit %d recorded as %q, want %q — %s",
					tc.exitCode, gate.Status, tc.want, tc.why)
			}
		})
	}
}

// An exit code this build does not know is `error`: unclassifiable is not
// `failed` (which asserts something about the release) and not `passed`.
func TestGateFromDocument_UnknownExitCodeIsError(t *testing.T) {
	t.Parallel()
	gate, err := gateFromDocument([]byte(`{"ok":false,"exit_code":99,"phase":"odd"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if gate.Status != release.GateStatusError {
		t.Fatalf("status = %q, want error", gate.Status)
	}
}

// A verb that exits 0 while reporting ok:false is not laundered into a pass
// by the exit-code switch: `ok` still decides.
func TestGateFromDocument_ZeroExitDefersToOK(t *testing.T) {
	t.Parallel()
	gate, err := gateFromDocument([]byte(`{"ok":false,"exit_code":0,"phase":"x"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if gate.Status != release.GateStatusFailed {
		t.Fatalf("status = %q, want failed — ok:false must not be overridden by a zero exit", gate.Status)
	}
}

// A smoke that probed NOTHING is `skipped`, never `passed`. This is the
// green-while-blind case: the document says ok:true and exits 0, and
// recording it as a pass would attest to a probe that never ran.
func TestGateFromDocument_SmokeThatCheckedNothingIsSkipped(t *testing.T) {
	t.Parallel()
	doc := smokeJSONReport{Env: "prod", Routes: []smokeJSONRoute{}}
	doc.Summary.OK = true
	data, _ := json.Marshal(doc)

	gate, err := gateFromDocument(data, "")
	if err != nil {
		t.Fatal(err)
	}
	if gate.Status != release.GateStatusSkipped {
		t.Fatalf("status = %q, want %q — a smoke with no probes must never read as a pass",
			gate.Status, release.GateStatusSkipped)
	}
	if !strings.Contains(gate.Summary, "nothing was checked") {
		t.Errorf("summary should say nothing was checked, got %q", gate.Summary)
	}
}

// An env that has never been promoted has declared nothing to be wrong
// about — and nothing to vouch for. `forge env status` exits 0; the gate is
// skipped, not passed.
func TestGateFromDocument_UnboundEnvVerifyIsSkipped(t *testing.T) {
	t.Parallel()
	data, _ := json.Marshal(envStatusDocument{Env: "prod", Bound: false, Images: []imageVerification{}, OK: true})
	gate, err := gateFromDocument(data, "")
	if err != nil {
		t.Fatal(err)
	}
	if gate.Status != release.GateStatusSkipped {
		t.Fatalf("status = %q, want skipped", gate.Status)
	}
}

// A document that states no verdict yields NO gate — not a pass, and not an
// error gate. Guessing would produce evidence about nothing.
func TestGateFromDocument_NoVerdictYieldsNoGate(t *testing.T) {
	t.Parallel()
	for _, doc := range []string{
		`{}`,
		`{"some":"unrelated json"}`,
		`{"name":"lint"}`,              // a name but no status
		`{"status":"passed"}`,          // a status but no name, and no envelope
		`{"findings":[],"summary":{}}`, // lint-ish, but no ok/exit_code
	} {
		if _, err := gateFromDocument([]byte(doc), ""); err == nil {
			t.Errorf("gateFromDocument(%s) must refuse, got a gate", doc)
		}
	}
}

// An envelope forge recognises but cannot attribute to a verb is still
// trustworthy — the envelope's contract is universal — so --name makes it
// recordable. Without a name there is nothing to call the check.
func TestGateFromDocument_UnrecognisedShapeNeedsAName(t *testing.T) {
	t.Parallel()
	doc := []byte(`{"ok":true,"exit_code":0}`)

	if _, err := gateFromDocument(doc, ""); err != errNoGateInDocument {
		t.Fatalf("an unnamed, unrecognised document must refuse, got %v", err)
	}
	gate, err := gateFromDocument(doc, "qa-signoff")
	if err != nil {
		t.Fatal(err)
	}
	if gate.Name != "qa-signoff" || gate.Status != release.GateStatusPassed {
		t.Fatalf("gate = %+v, want the hinted name and a pass", gate)
	}
}

// A document that names ITSELF wins over --name: the producer knows what it
// ran better than the person recording it.
func TestGateFromDocument_DocumentNameBeatsTheHint(t *testing.T) {
	t.Parallel()
	data, _ := json.Marshal(map[string]any{
		"ok": true, "exit_code": 0,
		"totals": map[string]any{"tests": 10, "passed": 10, "failed": 0, "skipped": 0},
	})
	gate, err := gateFromDocument(data, "my-name-for-it")
	if err != nil {
		t.Fatal(err)
	}
	if gate.Name != "test" {
		t.Fatalf("name = %q, want the document's own %q", gate.Name, "test")
	}
	if !strings.Contains(gate.Summary, "10 passed") {
		t.Errorf("summary = %q", gate.Summary)
	}
}

// gateFromDocumentFile names the file-level failures as themselves: a missing
// file and an uninterpretable document are different problems.
func TestGateFromDocumentFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	if _, err := gateFromDocumentFile(filepath.Join(dir, "absent.json"), ""); err == nil ||
		!strings.Contains(err.Error(), "read gate document") {
		t.Errorf("a missing file must say so, got %v", err)
	}

	junk := filepath.Join(dir, "junk.json")
	if err := os.WriteFile(junk, []byte(`{"unrelated":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := gateFromDocumentFile(junk, "")
	if err == nil || !strings.Contains(err.Error(), "states no verdict") {
		t.Errorf("an uninterpretable document must say what was expected, got %v", err)
	}
	// The message must tell the reader how to PRODUCE one.
	if err != nil && !strings.Contains(err.Error(), "--gate-json") {
		t.Errorf("the fix hint should name --gate-json, got %q", err)
	}

	good := filepath.Join(dir, "gate.json")
	if err := writeGateDocument(good, release.Gate{Name: "lint", Status: release.GateStatusPassed}); err != nil {
		t.Fatal(err)
	}
	gate, err := gateFromDocumentFile(good, "")
	if err != nil {
		t.Fatal(err)
	}
	if gate.Name != "lint" || gate.Status != release.GateStatusPassed {
		t.Fatalf("round trip lost the gate: %+v", gate)
	}
}

// writeGateDocument refuses an unrecordable gate AT THE PRODUCER, so the
// failure happens in the job that ran the check rather than later, in a
// different job, with the check's output gone.
func TestWriteGateDocument_RefusesAnUnrecordableGate(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "gate.json")
	if err := writeGateDocument(path, release.Gate{Status: release.GateStatusPassed}); err == nil {
		t.Fatal("a gate with no name must be refused")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("no file may be written for a refused gate")
	}
}

// statusForFindings: zero checks is `skipped`, never a pass.
func TestStatusForFindings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		gating, checked int
		want            release.GateStatus
	}{
		{0, 12, release.GateStatusPassed},
		{3, 12, release.GateStatusFailed},
		{0, 0, release.GateStatusSkipped},
		{1, 0, release.GateStatusFailed}, // a finding is a finding
	}
	for _, tc := range cases {
		if got := statusForFindings(tc.gating, tc.checked); got != tc.want {
			t.Errorf("statusForFindings(%d, %d) = %q, want %q", tc.gating, tc.checked, got, tc.want)
		}
	}
}
