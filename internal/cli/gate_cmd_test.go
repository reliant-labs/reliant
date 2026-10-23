package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/release"
)

// gateFixture is a promoted env with a gate store bound to the same fake
// control plane, so a record goes to the place the promotion lives.
func gateFixture(t *testing.T) (*fakeDeployService, gateRecordOptions, release.Promotion) {
	t.Helper()
	fake, store := hostedPromoteFixture(t, "v1")
	gstore := newGateTestStore(t, fake)
	useGateBackend(t, gateBackend{hosted: true, store: gstore, bindings: store})
	useGateBackend(t, gateBackend{hosted: true, store: gstore, bindings: store})
	current, ok, err := store.Current(context.Background(), "prod")
	if err != nil || !ok {
		t.Fatalf("fixture env is not promoted: %v", err)
	}
	return fake, gateRecordOptions{run: runOptions{None: true}}, current
}

// useGateBackend installs the backend seam for one test, so the command runs
// its real path against an httptest control plane. Restored on cleanup
// because the seam is package-level.
func useGateBackend(t *testing.T, backend gateBackend) {
	t.Helper()
	previous := resolveGateBackend
	resolveGateBackend = func(context.Context, string) (gateBackend, error) { return backend, nil }
	t.Cleanup(func() { resolveGateBackend = previous })
}

// useFailingGateBackend installs a backend whose resolution itself fails.
func useFailingGateBackend(t *testing.T, backend gateBackend, err error) {
	t.Helper()
	previous := resolveGateBackend
	resolveGateBackend = func(context.Context, string) (gateBackend, error) { return backend, err }
	t.Cleanup(func() { resolveGateBackend = previous })
}

// newGateTestStore binds a gateStore to the fake, the same way
// newHostedTestStore binds the ledger.
func newGateTestStore(t *testing.T, fake *fakeDeployService) *gateStore {
	t.Helper()
	_, srv := newHostedTestStore(t, fake)
	ep := cloud.Endpoint{Env: "prod", URL: srv.URL, TokenEnv: "T"}
	return &gateStore{client: cloud.NewClient(ep, cloud.Credential{Token: "rlat_test"}), endpoint: srv.URL}
}

func runRecord(t *testing.T, opts gateRecordOptions) (string, error) {
	t.Helper()
	var out strings.Builder
	err := runGateRecord(context.Background(), "prod", opts, &out)
	return out.String(), err
}

// THE HEADLINE: a gate reaches the promotion's child record, and the server's
// attribution comes back.
func TestGateRecord_AppendsToTheCurrentPromotion(t *testing.T) {
	fake, opts, current := gateFixture(t)
	opts.name, opts.status, opts.summary = "smoke", "passed", "4 routes probed"

	out, err := runRecord(t, opts)
	if err != nil {
		t.Fatalf("gate record: %v", err)
	}
	if n := fake.callCount(procRecordGate); n != 1 {
		t.Fatalf("RecordGate called %d time(s), want 1", n)
	}
	if !strings.Contains(out, "smoke") || !strings.Contains(out, "passed") {
		t.Errorf("output should name the gate and its verdict:\n%s", out)
	}
	if !strings.Contains(out, current.ID) {
		t.Errorf("output should name the promotion %q:\n%s", current.ID, out)
	}

	gates, err := mustGates(t, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(gates) != 1 || gates[0].Name != "smoke" {
		t.Fatalf("gates = %+v", gates)
	}
	// ATTRIBUTION IS THE SERVER'S. forge never sends it, and reads it back.
	if gates[0].RecordedBy != "token:test-token" {
		t.Errorf("recorded_by = %q, want the server's principal", gates[0].RecordedBy)
	}
	body := lastBody(t, fake, procRecordGate)
	sent, _ := body["gate"].(map[string]any)
	for _, forbidden := range []string{"recordedBy", "recordedAt"} {
		if _, set := sent[forbidden]; set {
			t.Errorf("forge must not send %s: %v", forbidden, sent)
		}
	}
}

func lastBody(t *testing.T, f *fakeDeployService, proc string) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.bodies) - 1; i >= 0; i-- {
		if f.bodies[i].Path == "/"+proc {
			return f.bodies[i].Body
		}
	}
	t.Fatalf("no %s call was made", proc)
	return nil
}

// RECORDING A `failed` GATE EXITS 0. The recording succeeded; the check's own
// step already failed the job. Anything else is an incentive not to record.
func TestGateRecord_AFailedGateExitsZero(t *testing.T) {
	_, opts, _ := gateFixture(t)
	opts.name, opts.status, opts.summary = "test", "failed", "3 failures"

	if _, err := runRecord(t, opts); err != nil {
		t.Fatalf("recording a failed gate must exit 0, got %v", err)
	}
}

// Idempotent on (promotion, name, run id): a re-run of one CI step returns
// the existing row rather than duplicating it, and says so.
func TestGateRecord_IsIdempotentOnPromotionNameAndRun(t *testing.T) {
	fake, opts, current := gateFixture(t)
	opts.name, opts.status = "test", "passed"
	opts.run = runOptions{ID: "github:acme/app/42/1"}

	if _, err := runRecord(t, opts); err != nil {
		t.Fatal(err)
	}
	out, err := runRecord(t, opts)
	if err != nil {
		t.Fatalf("a repeat must succeed, got %v", err)
	}
	if !strings.Contains(out, "already recorded") {
		t.Errorf("a repeat should report itself as already recorded:\n%s", out)
	}
	gates, _ := mustGates(t, current.ID)
	if len(gates) != 1 {
		t.Fatalf("got %d gates, want 1 — the repeat must not duplicate", len(gates))
	}
	if n := fake.callCount(procRecordGate); n != 2 {
		t.Errorf("both attempts should reach the server (it owns idempotency), got %d calls", n)
	}
}

// A DIFFERENT ATTEMPT RECORDS AFRESH. This is why the run id carries
// GITHUB_RUN_ATTEMPT: without it a re-run would collide with the first
// attempt and be handed the OLD row while believing it recorded the new one.
func TestGateRecord_ASecondAttemptRecordsItsOwnRow(t *testing.T) {
	_, opts, current := gateFixture(t)
	opts.name, opts.status = "test", "failed"
	opts.run = runOptions{ID: "github:acme/app/42/1"}
	if _, err := runRecord(t, opts); err != nil {
		t.Fatal(err)
	}

	// Attempt 2 of the same run: same name, same pipeline, new attempt.
	opts.status = "passed"
	opts.run = runOptions{ID: "github:acme/app/42/2"}
	if _, err := runRecord(t, opts); err != nil {
		t.Fatal(err)
	}

	gates, _ := mustGates(t, current.ID)
	if len(gates) != 2 {
		t.Fatalf("got %d gates, want 2 — a re-run's evidence must not collide with the first attempt's", len(gates))
	}
	// Both are kept: the history of a flaky check is itself the evidence.
	if gates[0].Status != release.GateStatusFailed || gates[1].Status != release.GateStatusPassed {
		t.Errorf("both attempts must be kept in order: %+v", gates)
	}
}

// --from reads any forge --json document, with no glue script. This is the
// property that makes recording cheap enough to actually happen.
func TestGateRecord_FromAForgeJSONDocument(t *testing.T) {
	_, opts, current := gateFixture(t)
	path := filepath.Join(t.TempDir(), "wait.json")
	doc, _ := json.Marshal(map[string]any{
		"ok": true, "exit_code": 0, "phase": "succeeded", "reason": "every workload converged",
	})
	if err := os.WriteFile(path, doc, 0o644); err != nil {
		t.Fatal(err)
	}
	opts.from = path

	if _, err := runRecord(t, opts); err != nil {
		t.Fatal(err)
	}
	gates, _ := mustGates(t, current.ID)
	if len(gates) != 1 || gates[0].Name != "wait" {
		t.Fatalf("gates = %+v, want one gate named wait", gates)
	}
	if gates[0].Status != release.GateStatusPassed {
		t.Errorf("status = %q, want passed", gates[0].Status)
	}
	if !strings.Contains(gates[0].Summary, "every workload converged") {
		t.Errorf("summary = %q, want the document's reason", gates[0].Summary)
	}
}

// --status BESIDE --from is refused: the document carries the check's own
// verdict, and overriding it records an opinion as evidence.
func TestGateRecord_StatusCannotOverrideADocument(t *testing.T) {
	_, opts, _ := gateFixture(t)
	path := filepath.Join(t.TempDir(), "smoke.json")
	if err := writeGateDocument(path, release.Gate{Name: "smoke", Status: release.GateStatusFailed}); err != nil {
		t.Fatal(err)
	}
	opts.from, opts.status = path, "passed"

	_, err := runRecord(t, opts)
	if err == nil || !strings.Contains(err.Error(), "carries the check's own verdict") {
		t.Fatalf("--status beside --from must be refused, got %v", err)
	}
	if exitCodeForError(err) != exitWrong {
		t.Errorf("exit = %d, want %d", exitCodeForError(err), exitWrong)
	}
}

// --url and --summary DO compose with --from: they are metadata about the
// check, not the check's answer. CI knows the run URL; the document knows the
// counts.
func TestGateRecord_UrlAndSummaryComposeWithADocument(t *testing.T) {
	_, opts, current := gateFixture(t)
	path := filepath.Join(t.TempDir(), "lint.json")
	if err := writeGateDocument(path, release.Gate{
		Name: "lint", Status: release.GateStatusPassed, Summary: "0 errors",
	}); err != nil {
		t.Fatal(err)
	}
	opts.from, opts.url = path, "https://ci.example/run/99"

	if _, err := runRecord(t, opts); err != nil {
		t.Fatal(err)
	}
	gates, _ := mustGates(t, current.ID)
	if gates[0].URL != "https://ci.example/run/99" {
		t.Errorf("url = %q, want the flag's", gates[0].URL)
	}
	if gates[0].Summary != "0 errors" {
		t.Errorf("summary = %q, want the document's", gates[0].Summary)
	}
}

// Exit 1: an invalid gate. The status set is closed on forge's side too, so
// the typo fails before the RPC.
func TestGateRecord_InvalidGateExitsOne(t *testing.T) {
	fake, opts, _ := gateFixture(t)
	opts.name, opts.status = "test", "green"

	_, err := runRecord(t, opts)
	if err == nil {
		t.Fatal("a status outside the closed set must be refused")
	}
	if got := exitCodeForError(err); got != exitWrong {
		t.Errorf("exit = %d, want %d (we looked, and the input was wrong)", got, exitWrong)
	}
	if n := fake.callCount(procRecordGate); n != 0 {
		t.Errorf("a malformed gate must not reach the server, got %d call(s)", n)
	}
}

// Stating no gate at all is exit 1, with a message naming both forms.
func TestGateRecord_NoGateStatedExitsOne(t *testing.T) {
	_, opts, _ := gateFixture(t)
	_, err := runRecord(t, opts)
	if err == nil || !strings.Contains(err.Error(), "--from") {
		t.Fatalf("want a refusal naming --from, got %v", err)
	}
	if got := exitCodeForError(err); got != exitWrong {
		t.Errorf("exit = %d, want %d", got, exitWrong)
	}
}

// Exit 3: --release asserts which promotion is current, and a moved
// environment is a CONFLICT — never a silent record against the wrong
// promotion, which would read as a vouched-for release.
func TestGateRecord_ReleaseMismatchExitsConflict(t *testing.T) {
	fake, opts, _ := gateFixture(t)
	opts.name, opts.status = "smoke", "passed"
	opts.releaseVer = "v2" // the fixture promoted v1

	_, err := runRecord(t, opts)
	if err == nil {
		t.Fatal("a --release that is not current must be refused")
	}
	if got := exitCodeForError(err); got != exitConflict {
		t.Fatalf("exit = %d, want %d (conflict)", got, exitConflict)
	}
	if !strings.Contains(err.Error(), "moved on") {
		t.Errorf("the message should say the env moved: %q", err)
	}
	if n := fake.callCount(procRecordGate); n != 0 {
		t.Errorf("nothing may be recorded on a conflict, got %d call(s)", n)
	}
}

// --release that MATCHES records normally, and reports the release.
func TestGateRecord_MatchingReleaseRecords(t *testing.T) {
	_, opts, _ := gateFixture(t)
	opts.name, opts.status, opts.releaseVer = "smoke", "passed", "v1"

	out, err := runRecord(t, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "release v1") {
		t.Errorf("output should name the release:\n%s", out)
	}
}

// Exit 2: auth refused is "could not look", NOT a failed check. Folding it
// into failure would blame the release for a credential problem.
func TestGateRecord_AuthRefusedExitsUndetermined(t *testing.T) {
	_, opts, _ := gateFixture(t)
	opts.name, opts.status = "test", "passed"
	_, bindings := hostedPromoteFixture(t, "v1")
	useGateBackend(t, gateBackend{hosted: true, bindings: bindings,
		store: &gateStore{client: refusingCaller{code: cloud.CodePermissionDenied}, endpoint: "https://cp.example"}})

	_, err := runRecord(t, opts)
	if err == nil {
		t.Fatal("a refused credential must be an error")
	}
	if got := exitCodeForError(err); got != exitUndetermined {
		t.Fatalf("exit = %d, want %d (could not look)", got, exitUndetermined)
	}
	if !strings.Contains(err.Error(), "deploy:write") {
		t.Errorf("the hint should name the scope recording needs: %q", err)
	}
}

// A control plane that does not serve the procedure yet is also exit 2 — the
// evidence was not recorded and forge cannot tell whether it would have been.
func TestGateRecord_UnimplementedExitsUndetermined(t *testing.T) {
	_, opts, _ := gateFixture(t)
	opts.name, opts.status = "test", "passed"
	_, bindings := hostedPromoteFixture(t, "v1")
	useGateBackend(t, gateBackend{hosted: true, bindings: bindings,
		store: &gateStore{client: refusingCaller{code: cloud.CodeUnimplemented}, endpoint: "https://cp.example"}})

	_, err := runRecord(t, opts)
	if got := exitCodeForError(err); got != exitUndetermined {
		t.Fatalf("exit = %d, want %d", got, exitUndetermined)
	}
}

// refusingCaller fails every call with one Connect code, so the exit-code
// mapping is tested without a server that has to be talked into failing.
type refusingCaller struct{ code string }

func (r refusingCaller) Call(_ context.Context, procedure string, _, _ any) error {
	return &cloud.Error{
		Code: r.code, Message: "refused by the test", Procedure: procedure,
		Endpoint: "https://cp.example",
	}
}

// An env that has never been promoted has no promotion to attach evidence to.
func TestGateRecord_UnpromotedEnvIsRefused(t *testing.T) {
	fake := newFakeDeployService(map[string]string{"prod": "env-prod-uuid"})
	store, _ := newHostedTestStore(t, fake)
	useGateBackend(t, gateBackend{hosted: true, store: newGateTestStore(t, fake), bindings: store})
	opts := gateRecordOptions{name: "smoke", status: "passed", run: runOptions{None: true}}
	_, err := runRecord(t, opts)
	if err == nil || !strings.Contains(err.Error(), "never been promoted") {
		t.Fatalf("want a refusal naming the unpromoted env, got %v", err)
	}
}

// A FILE-BACKED env cannot hold post-promote evidence: its entries are lines
// already written. Said plainly, with the promote-time alternative.
func TestGateRecord_FileLedgerSaysWhyAndWhatToDoInstead(t *testing.T) {
	dir := t.TempDir()
	ledger := testLedger(t, dir)
	if _, err := ledger.Bindings.Append(context.Background(),
		release.Promotion{Env: "prod", Release: "v1", Kind: release.KindPromote}, appendGuard{}); err != nil {
		t.Fatal(err)
	}
	useFailingGateBackend(t, gateBackend{hosted: false, bindings: ledger.Bindings},
		errGateNeedsHostedLedger("prod", ledger.Bindings.Location()))
	opts := gateRecordOptions{name: "smoke", status: "passed", run: runOptions{None: true}}
	_, err := runRecord(t, opts)
	if err == nil {
		t.Fatal("a file ledger must refuse post-promote evidence")
	}
	for _, want := range []string{"cannot hold post-promote evidence", "--gate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message should mention %q: %q", want, err)
		}
	}
}

// ─── gate list ──────────────────────────────────────────────────────────────

// PROMOTE-TIME GATES COME FIRST, then the recorded ones. That order carries
// "was this known before the environment moved", which sorting the union by
// time would destroy.
func TestGateList_PromoteTimeGatesComeBeforeRecordedOnes(t *testing.T) {
	fake, store := hostedPromoteFixture(t)
	gstore := newGateTestStore(t, fake)
	useGateBackend(t, gateBackend{hosted: true, store: gstore, bindings: store})

	// Promote with pre-promote evidence.
	if _, err := runHostedPromote(t, store, "v1", promoteOptions{
		Gates: []string{"name=lint,status=passed", "name=test,status=passed"},
	}); err != nil {
		t.Fatal(err)
	}
	current, _, err := store.Current(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}

	// Then record what was learned after.
	recordOpts := gateRecordOptions{
		run: runOptions{None: true}, name: "smoke", status: "failed",
	}
	if _, err := runRecord(t, recordOpts); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := runGateList(context.Background(), "prod",
		gateListOptions{}, &out); err != nil {
		t.Fatal(err)
	}

	gates, err := gstore.listGates(context.Background(), current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(gates) != 3 {
		t.Fatalf("got %d gates, want 3 (2 promote-time + 1 recorded): %+v", len(gates), gates)
	}
	names := []string{gates[0].Name, gates[1].Name, gates[2].Name}
	if names[0] != "lint" || names[1] != "test" || names[2] != "smoke" {
		t.Errorf("order = %v, want the promote-time pair before the recorded one", names)
	}
	// Only the recorded one carries attribution — the promote-time gates
	// were frozen into the entry.
	if gates[2].RecordedBy == "" {
		t.Error("the recorded gate should carry the server's attribution")
	}
	if !strings.Contains(out.String(), "smoke") {
		t.Errorf("the table should list every gate:\n%s", out.String())
	}
}

// --json carries the §3.A envelope, stamped from the same error the text path
// returns, and never `null` for the gate list.
func TestGateList_JSONEnvelope(t *testing.T) {
	fake, store := hostedPromoteFixture(t, "v1")
	gstore := newGateTestStore(t, fake)
	useGateBackend(t, gateBackend{hosted: true, store: gstore, bindings: store})

	out := captureStdout(t, func() {
		if err := runGateList(context.Background(), "prod",
			gateListOptions{jsonOut: true}, os.Stdout); err != nil {
			t.Errorf("gate list: %v", err)
		}
	})
	var doc struct {
		OK       bool            `json:"ok"`
		ExitCode int             `json:"exit_code"`
		Env      string          `json:"env"`
		Gates    json.RawMessage `json:"gates"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, out)
	}
	if !doc.OK || doc.ExitCode != exitOK || doc.Env != "prod" {
		t.Errorf("envelope = %+v", doc)
	}
	if string(doc.Gates) != "[]" {
		t.Errorf("gates = %s, want [] rather than null", doc.Gates)
	}
}

// The --json document of a FAILED record reports the same code the process
// exits with. A document claiming ok:true over a non-zero exit is the bug
// the shared envelope exists to prevent.
func TestGateRecord_JSONEnvelopeAgreesWithTheExitCode(t *testing.T) {
	_, opts, _ := gateFixture(t)
	opts.name, opts.status, opts.jsonOut = "smoke", "passed", true
	opts.releaseVer = "v2" // conflict

	var err error
	out := captureStdout(t, func() {
		err = runGateRecord(context.Background(), "prod", opts, os.Stdout)
	})
	if err == nil {
		t.Fatal("want the conflict error")
	}
	var doc struct {
		OK       bool   `json:"ok"`
		ExitCode int    `json:"exit_code"`
		Error    string `json:"error"`
	}
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", jerr, out)
	}
	if doc.OK {
		t.Error("ok must be false")
	}
	if doc.ExitCode != exitConflict || doc.ExitCode != exitCodeForError(err) {
		t.Errorf("exit_code = %d, want %d and equal to the process's", doc.ExitCode, exitConflict)
	}
	if doc.Error == "" {
		t.Error("the document must carry the reason")
	}
}

// The command surface §3.3 names.
func TestGateCmd_DeclaresItsFlags(t *testing.T) {
	t.Parallel()
	cmd := newGateCmd()
	sub := map[string][]string{
		"record": {"promotion", "release", "from", "name", "status", "url", "summary", "json", "run-id", "run-url", "no-run"},
		"list":   {"promotion", "json"},
	}
	for name, flags := range sub {
		var found bool
		for _, c := range cmd.Commands() {
			if c.Name() != name {
				continue
			}
			found = true
			for _, f := range flags {
				if c.Flags().Lookup(f) == nil {
					t.Errorf("--%s is not declared on `forge gate %s`", f, name)
				}
			}
		}
		if !found {
			t.Errorf("`forge gate %s` is not declared", name)
		}
	}
}

// mustGates reads a promotion's gates through the installed backend seam, so
// an assertion sees exactly what the command wrote.
func mustGates(t *testing.T, promotionID string) ([]release.Gate, error) {
	t.Helper()
	backend, err := resolveGateBackend(context.Background(), "prod")
	if err != nil {
		return nil, err
	}
	return backend.store.listGates(context.Background(), promotionID)
}

// AN EXPLICIT --name WINS over a document's derived name.
//
// The case is real and the consequence is silent: two `env wait` documents for
// two environments both derive the name "wait", and the server's idempotency
// key is (promotion, name, run id) — so under one run id the second record
// would hand back the FIRST one's row rather than recording. The caller who
// typed --name is distinguishing them, and that must be honoured.
//
// (Inside gateFromDocument the name is only a hint, because there a document
// that names itself beats a default. Typed on the command line, it is not a
// default.)
func TestGateRecord_ExplicitNameWinsOverTheDocumentsOwn(t *testing.T) {
	_, opts, current := gateFixture(t)
	path := filepath.Join(t.TempDir(), "wait.json")
	doc, _ := json.Marshal(map[string]any{
		"ok": true, "exit_code": 0, "phase": "succeeded", "reason": "converged",
	})
	if err := os.WriteFile(path, doc, 0o644); err != nil {
		t.Fatal(err)
	}
	opts.from, opts.name = path, "wait-prod"

	if _, err := runRecord(t, opts); err != nil {
		t.Fatal(err)
	}
	gates, err := mustGates(t, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(gates) != 1 || gates[0].Name != "wait-prod" {
		t.Fatalf("gates = %+v, want the explicit --name to win over the derived \"wait\"", gates)
	}
	// The document's verdict and summary still come from the document.
	if gates[0].Status != release.GateStatusPassed || !strings.Contains(gates[0].Summary, "converged") {
		t.Errorf("the document must still supply the verdict and summary: %+v", gates[0])
	}
}

// Two waits for two environments, recorded under ONE run id, both land —
// because their names differ. This is the collision the rule above prevents.
func TestGateRecord_TwoNamedWaitsUnderOneRunBothLand(t *testing.T) {
	_, opts, current := gateFixture(t)
	dir := t.TempDir()
	write := func(name, reason string) string {
		p := filepath.Join(dir, name)
		doc, _ := json.Marshal(map[string]any{
			"ok": true, "exit_code": 0, "phase": "succeeded", "reason": reason,
		})
		if err := os.WriteFile(p, doc, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	opts.run = runOptions{ID: "github:acme/app/42/1"}

	opts.from, opts.name = write("staging.json", "staging converged"), "wait-staging"
	if _, err := runRecord(t, opts); err != nil {
		t.Fatal(err)
	}
	opts.from, opts.name = write("prod.json", "prod converged"), "wait-prod"
	if _, err := runRecord(t, opts); err != nil {
		t.Fatal(err)
	}

	gates, err := mustGates(t, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(gates) != 2 {
		t.Fatalf("got %d gate(s), want 2 — distinct names must not collide on one run id: %+v", len(gates), gates)
	}
}
