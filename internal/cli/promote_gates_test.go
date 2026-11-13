package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// The inline form, field by field. This is the escape hatch for a check that
// is not a forge verb, so its parsing rules are a user-facing contract.
func TestResolvePromoteGates_InlineForm(t *testing.T) {
	t.Parallel()
	gates, err := resolvePromoteGates([]string{
		"name=e2e,status=passed,url=https://ci.example/run/9",
		"name=qa-signoff,status=skipped",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(gates) != 2 {
		t.Fatalf("got %d gates, want 2", len(gates))
	}
	if gates[0].Name != "e2e" || gates[0].Status != release.GateStatusPassed ||
		gates[0].URL != "https://ci.example/run/9" {
		t.Errorf("gate[0] = %+v", gates[0])
	}
	if gates[1].Status != release.GateStatusSkipped {
		t.Errorf("gate[1] status = %q, want skipped", gates[1].Status)
	}
}

// A summary is prose and prose contains commas, so `summary=` runs to the end
// of the spec. Splitting it on commas would reject the one field most likely
// to contain the separator.
func TestResolvePromoteGates_SummaryMayContainCommas(t *testing.T) {
	t.Parallel()
	gates, err := resolvePromoteGates([]string{"name=test,status=passed,summary=412 passed, 0 failed, 3 skipped"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "412 passed, 0 failed, 3 skipped"; gates[0].Summary != want {
		t.Fatalf("summary = %q, want %q", gates[0].Summary, want)
	}
}

// A status outside the closed set is refused CLIENT-SIDE, naming the four
// legal values — the typo fails before the RPC rather than arriving as an
// InvalidArgument the caller has to decode.
func TestResolvePromoteGates_StatusOutsideTheSetIsRefusedWithTheSet(t *testing.T) {
	t.Parallel()
	_, err := resolvePromoteGates([]string{"name=test,status=pass"})
	if !errors.Is(err, release.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	for _, want := range []string{"passed", "failed", "skipped", "error"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %q: %q", want, err)
		}
	}
}

// Everything the inline form refuses, and why each would otherwise record
// evidence nobody can interpret.
func TestResolvePromoteGates_Refusals(t *testing.T) {
	t.Parallel()
	cases := map[string]struct{ spec, wantMsg string }{
		"no name":       {"status=passed", "name is required"},
		"no status":     {"name=test", "status is required"},
		"unknown field": {"name=test,status=passed,state=green", "unknown field"},
		"not key=value": {"name=test,status=passed,bogus", "is not key=value"},
		"empty":         {"   ", "empty value"},
		"blank name":    {"name=  ,status=passed", "name is required"},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			_, err := resolvePromoteGates([]string{tc.spec})
			if err == nil {
				t.Fatalf("--gate %q must be refused", tc.spec)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantMsg)
			}
		})
	}
}

// A repeated name is refused: one promotion records one result per check, and
// a frozen entry has no later row to supersede an earlier one.
func TestResolvePromoteGates_DuplicateNameIsRefused(t *testing.T) {
	t.Parallel()
	_, err := resolvePromoteGates([]string{"name=test,status=passed", "name=test,status=failed"})
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("a duplicate gate name must be refused, got %v", err)
	}
}

// The file form, through gateFromDocument: a `--gate-json` document and a
// plain forge `--json` document are both accepted with no glue script.
func TestResolvePromoteGates_FileForm(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	gatePath := filepath.Join(dir, "lint.json")
	if err := writeGateDocument(gatePath, release.Gate{
		Name: "lint", Status: release.GateStatusPassed, Summary: "0 errors",
	}); err != nil {
		t.Fatal(err)
	}

	// A forge --json document that is not gate-shaped at all.
	waitPath := filepath.Join(dir, "wait.json")
	waitDoc, _ := json.Marshal(map[string]any{
		"ok": true, "exit_code": 0, "phase": "succeeded", "reason": "every workload converged",
	})
	if err := os.WriteFile(waitPath, waitDoc, 0o644); err != nil {
		t.Fatal(err)
	}

	gates, err := resolvePromoteGates([]string{gatePath, waitPath})
	if err != nil {
		t.Fatal(err)
	}
	if len(gates) != 2 {
		t.Fatalf("got %d gates, want 2", len(gates))
	}
	if gates[0].Name != "lint" || gates[0].Summary != "0 errors" {
		t.Errorf("gate[0] = %+v", gates[0])
	}
	if gates[1].Name != "wait" || gates[1].Status != release.GateStatusPassed {
		t.Errorf("gate[1] = %+v, want the wait document read as a passing wait gate", gates[1])
	}
}

// An unreadable file refuses the whole promote. The alternative — skipping it
// with a warning — promotes with the evidence silently missing.
func TestResolvePromoteGates_MissingFileIsRefused(t *testing.T) {
	t.Parallel()
	_, err := resolvePromoteGates([]string{filepath.Join(t.TempDir(), "absent.json")})
	if err == nil || !strings.Contains(err.Error(), "read gate document") {
		t.Fatalf("a missing gate file must be refused, got %v", err)
	}
}

// No --gate means no gates, and no error: the flag is optional.
func TestResolvePromoteGates_EmptyIsNotAnError(t *testing.T) {
	t.Parallel()
	gates, err := resolvePromoteGates(nil)
	if err != nil || gates != nil {
		t.Fatalf("resolvePromoteGates(nil) = %v, %v", gates, err)
	}
}

// ─── End to end, through the promote wire ───────────────────────────────────

// THE HEADLINE: a --gate reaches the promotion on the wire, in full. This is
// what makes the flag evidence rather than a no-op that validated something
// and dropped it — F2 wired resolvePromoteGates' output through
// promoteWrite.Gates → Promotion.Gates → the hosted Append's strict
// gatesToWire, and this asserts the whole chain.
func TestPromote_GatesReachTheWire(t *testing.T) {
	fake, store := hostedPromoteFixture(t, "v1")

	_, err := runHostedPromote(t, store, "v2", promoteOptions{
		Gates: []string{
			"name=lint,status=passed,summary=0 errors",
			"name=e2e,status=failed,url=https://ci.example/run/9",
		},
	})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}

	body := fake.lastPromoteBody(t)
	raw, ok := body["gates"].([]any)
	if !ok {
		t.Fatalf("no gates on the promote body: %v", body)
	}
	if len(raw) != 2 {
		t.Fatalf("got %d gates on the wire, want 2: %v", len(raw), raw)
	}
	first, _ := raw[0].(map[string]any)
	if first["name"] != "lint" || first["status"] != "passed" || first["summary"] != "0 errors" {
		t.Errorf("gate[0] on the wire = %v", first)
	}
	second, _ := raw[1].(map[string]any)
	if second["name"] != "e2e" || second["status"] != "failed" ||
		second["url"] != "https://ci.example/run/9" {
		t.Errorf("gate[1] on the wire = %v", second)
	}
	// recordedBy / recordedAt are the SERVER's to set. A client that sent
	// them would be claiming who vouched for its own check.
	for i, g := range raw {
		m, _ := g.(map[string]any)
		for _, forbidden := range []string{"recordedBy", "recordedAt"} {
			if _, sent := m[forbidden]; sent {
				t.Errorf("gate[%d] must not send %s — the server sets it from the principal: %v", i, forbidden, m)
			}
		}
	}
}

// RECORDING A FAILED GATE STILL PROMOTES. Evidence, not enforcement: the
// promote succeeds and the entry records that the promoter knew. A client
// that refused here would be a rule that only binds whoever passes the flag.
func TestPromote_AFailedGateDoesNotBlockThePromote(t *testing.T) {
	fake, store := hostedPromoteFixture(t, "v1")
	before := fake.callCount(procPromote)

	if _, err := runHostedPromote(t, store, "v2", promoteOptions{
		Gates: []string{"name=test,status=failed,summary=3 failures"},
	}); err != nil {
		t.Fatalf("a failed gate must not block the promote: %v", err)
	}
	if n := fake.callCount(procPromote); n != before+1 {
		t.Fatalf("Promote called %d time(s), want exactly one", n-before)
	}
	gates, _ := fake.lastPromoteBody(t)["gates"].([]any)
	first, _ := gates[0].(map[string]any)
	if first["status"] != "failed" {
		t.Errorf("the failing verdict must be recorded verbatim: %v", first)
	}
}

// A BAD GATE REFUSES BEFORE ANY WRITE. The pointer must not move and then
// fail on the evidence — that would leave the env promoted with its gates
// missing and nothing to say so.
func TestPromote_AMalformedGateRefusesBeforeAnyWrite(t *testing.T) {
	fake, store := hostedPromoteFixture(t, "v1")
	before := fake.callCount(procPromote)

	_, err := runHostedPromote(t, store, "v2", promoteOptions{Gates: []string{"name=test,status=green"}})
	if err == nil {
		t.Fatal("a status outside the closed set must refuse the promote")
	}
	if n := fake.callCount(procPromote); n != before {
		t.Fatalf("refused AFTER writing: Promote called %d time(s)", n-before)
	}
	// And the env still runs what it ran.
	current, _, cerr := store.Current(context.Background(), "prod")
	if cerr != nil {
		t.Fatal(cerr)
	}
	if current.Release != "v1" {
		t.Fatalf("the env moved to %q despite the refusal", current.Release)
	}
}
