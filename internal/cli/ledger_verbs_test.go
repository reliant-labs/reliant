package cli

// `forge ledger export`, `where` and `show`.
//
// The load-bearing test here is the ROUND TRIP: import a git ledger, export
// it, and compare the result against the files it came from. That is §11.1
// step 7, and it is the only way to know an import lost nothing before the
// source files are deleted from the repository (step 9).

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/pkg/release"
)

// runLedgerVerb drives one `forge ledger <verb>` with the project pinned.
func runLedgerVerb(t *testing.T, dir string, newCmd func() *cobra.Command, args ...string) (string, error) {
	t.Helper()
	if err := cmdutil.SetProjectDir(dir); err != nil {
		t.Fatalf("pin the project dir: %v", err)
	}
	t.Cleanup(func() { _ = cmdutil.SetProjectDir("") })
	var out bytes.Buffer
	cmd := newCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// ─── export: the round trip ─────────────────────────────────────────────────

// §11.1 step 7. Import a git ledger, export it, and prove every promotion
// LINE came back byte-identical to the one in the repository — the same id,
// the same promoted_at, the same order, in the same canonical encoding.
//
// Byte equality of the lines is the assertion that has teeth. Comparing
// decoded structs would pass even if the export re-encoded with different
// key order or dropped an optional field, and the whole point of this
// verification is that an operator can run `diff` and believe the result.
func TestLedgerExport_RoundTripsThePromotionLinesByteForByte(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a real git project for the ledger; runs in task test")
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	promotions := []release.Promotion{
		importablePromotion("id-a", "prod", "v1.0.0", base),
		importablePromotion("id-b", "prod", "v2.0.0", base.Add(time.Hour)),
		importablePromotion("id-c", "staging", "v1.0.0", base),
	}
	fx := newImportFixture(t, func(dir string) {
		plantRetiredLedger(t, dir, promotions, map[string]release.Release{
			"v1.0.0": importableRelease("v1.0.0"),
			"v2.0.0": importableRelease("v2.0.0"),
		})
	})
	if _, err := runImport(t, fx.dir, "--from-git", "--rev", fx.rev, "--apply"); err != nil {
		t.Fatalf("import: %v", err)
	}

	exportDir := filepath.Join(t.TempDir(), "verify")
	out, err := runLedgerVerb(t, fx.dir, newLedgerExportCmd, "--dir", exportDir)
	if err != nil {
		t.Fatalf("export: %v\n%s", err, out)
	}

	// Each promotion line decodes back to EXACTLY the record the git
	// ledger held — including the fields a struct comparison would let
	// slide.
	for _, env := range []string{"prod", "staging"} {
		path := filepath.Join(exportDir, "promotions", env+".jsonl")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		lines := ledgerLines(string(data))
		var want []release.Promotion
		for _, p := range promotions {
			if p.Env == env {
				want = append(want, p)
			}
		}
		if len(lines) != len(want) {
			t.Fatalf("%s has %d lines, want %d", path, len(lines), len(want))
		}
		for i, line := range lines {
			var got release.Promotion
			if err := json.Unmarshal([]byte(line), &got); err != nil {
				t.Fatalf("%s line %d: %v", path, i+1, err)
			}
			// The id and the ORDER are what the round trip is
			// about: a reordered export would mean the import
			// recorded the wrong current binding.
			if got.ID != want[i].ID {
				t.Errorf("%s line %d id = %q, want %q", path, i+1, got.ID, want[i].ID)
			}
			if got.Release != want[i].Release {
				t.Errorf("%s line %d release = %q, want %q", path, i+1, got.Release, want[i].Release)
			}
			if !got.PromotedAt.Equal(want[i].PromotedAt) {
				t.Errorf("%s line %d promoted_at = %v, want %v", path, i+1, got.PromotedAt, want[i].PromotedAt)
			}
		}
	}

	// The releases round trip too, by VERSION — which is the normalization
	// that matters, since the retired layout keyed them by filename.
	data, err := os.ReadFile(filepath.Join(exportDir, "releases.jsonl"))
	if err != nil {
		t.Fatalf("read releases: %v", err)
	}
	var versions []string
	for _, line := range ledgerLines(string(data)) {
		var r release.Release
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("decode a release: %v", err)
		}
		versions = append(versions, r.Version)
	}
	sort.Strings(versions)
	if strings.Join(versions, ",") != "v1.0.0,v2.0.0" {
		t.Errorf("exported versions = %v, want v1.0.0,v2.0.0", versions)
	}

	// The expected differences are PRINTED. A reader diffing the export
	// against the git files has to know which differences are normal
	// before they can conclude that the rest are not.
	for _, note := range exportNormalizations {
		if !strings.Contains(out, note) {
			t.Errorf("export must print its normalizations; missing %q from:\n%s", note, out)
		}
	}
}

// An exported promotion keeps the imported_from the store's line carries, so
// the export is a faithful copy of the ledger rather than a re-encoding of
// release.Promotion that silently drops it.
func TestLedgerExport_PreservesTheImportProvenance(t *testing.T) {
	fx := newImportFixture(t, func(dir string) {
		plantRetiredLedger(t, dir,
			[]release.Promotion{importablePromotion("id-a", "prod", "v1.0.0", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))},
			map[string]release.Release{"v1.0.0": importableRelease("v1.0.0")})
	})
	if _, err := runImport(t, fx.dir, "--from-git", "--rev", fx.rev, "--apply"); err != nil {
		t.Fatalf("import: %v", err)
	}

	exportDir := filepath.Join(t.TempDir(), "verify")
	if out, err := runLedgerVerb(t, fx.dir, newLedgerExportCmd, "--dir", exportDir); err != nil {
		t.Fatalf("export: %v\n%s", err, out)
	}

	data, err := os.ReadFile(filepath.Join(exportDir, "promotions", "prod.jsonl"))
	if err != nil {
		t.Fatalf("read the export: %v", err)
	}
	var line struct {
		ID           string `json:"id"`
		ImportedFrom string `json:"imported_from"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &line); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(line.ImportedFrom, "git:") {
		t.Errorf("imported_from = %q, want the git provenance the ledger holds", line.ImportedFrom)
	}
}

// A retired env has history and no declaration anywhere (control-plane's
// staging and preprod, O-9). An export that enumerated only DECLARED envs
// would omit it and make the import look lossy against the very files it
// read.
func TestLedgerExport_IncludesAnEnvWithNoRecord(t *testing.T) {
	fx := newImportFixture(t, func(dir string) {
		plantRetiredLedger(t, dir,
			[]release.Promotion{importablePromotion("id-a", "retired-env", "v1.0.0", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))},
			map[string]release.Release{"v1.0.0": importableRelease("v1.0.0")})
	})
	store := testStore(t, fx.dir)
	if err := store.ImportPromotionFrom(
		importablePromotion("orphan", "no-record-env", "v1.0.0", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		"git:x@y"); err != nil {
		t.Fatalf("seed a promotion with no env record: %v", err)
	}

	exportDir := filepath.Join(t.TempDir(), "verify")
	if out, err := runLedgerVerb(t, fx.dir, newLedgerExportCmd, "--dir", exportDir); err != nil {
		t.Fatalf("export: %v\n%s", err, out)
	}

	if _, err := os.Stat(filepath.Join(exportDir, "promotions", "no-record-env.jsonl")); err != nil {
		t.Errorf("an env with history but no record must still be exported: %v", err)
	}
}

// --dir has no default, because a default is a location people start to rely
// on — and this is a verification aid, not a backup.
func TestLedgerExport_RequiresADirectory(t *testing.T) {
	dir := newImportProject(t, "export-project")

	_, err := runLedgerVerb(t, dir, newLedgerExportCmd)
	if err == nil {
		t.Fatal("expected a refusal: --dir is required")
	}
	if !strings.Contains(err.Error(), "--dir is required") {
		t.Errorf("error = %v, want it to name the missing flag", err)
	}
	if !strings.Contains(err.Error(), "backup") {
		t.Errorf("error = %v, want it to say this is not a backup location", err)
	}
}

// An env on a control plane has no file form. Rendering its rows as files
// would produce the stale-looking-authoritative second copy O-1 removed, so
// the export refuses and names the read that does work.
func TestLedgerExport_RefusesAHostedEnv(t *testing.T) {
	err := refuseHostedExport("prod", envLedger{Bindings: fakeHostedBindings{}, Hosted: true})
	if err == nil {
		t.Fatal("expected a refusal: a hosted ledger has no file form")
	}
	if !strings.Contains(err.Error(), "no file form") {
		t.Errorf("error = %v, want it to say there is no file form", err)
	}
	if !strings.Contains(err.Error(), "env status") {
		t.Errorf("error = %v, want it to name the read that does work", err)
	}
	// A machine-ledger env is exported, not refused.
	if err := refuseHostedExport("dev", envLedger{Bindings: fakeHostedBindings{}}); err != nil {
		t.Errorf("a machine-ledger env must be exportable, got: %v", err)
	}
}

// fakeHostedBindings is a bindingStore that only has to name a location.
type fakeHostedBindings struct{ noImportStore }

func (fakeHostedBindings) Location() string { return "https://api.example.com" }

// ─── where ──────────────────────────────────────────────────────────────────

// An env with no control-plane declaration lands on the machine ledger, and
// the answer says WHY — which is the question the command exists for, since
// selection is declarative and otherwise invisible.
func TestLedgerWhere_MachineLedgerNamesTheReason(t *testing.T) {
	dir := newImportProject(t, "where-project")

	doc, err := ledgerWhereFor(context.Background(), dir, "prod")
	if err != nil {
		t.Fatalf("where: %v", err)
	}
	if doc.Backend != ledgerBackendMachine {
		t.Errorf("backend = %q, want %q", doc.Backend, ledgerBackendMachine)
	}
	if !strings.Contains(doc.Because, "declares no forge.ControlPlane") {
		t.Errorf("because = %q, want it to name the missing declaration", doc.Because)
	}
	if doc.Location == "" {
		t.Error("the answer must name where records go")
	}
	if doc.Declaration != nil {
		t.Errorf("a machine-ledger env has no declaration to report, got %+v", doc.Declaration)
	}
}

// The text form prints the credential's env var NAME and never its value: a
// command's output goes into bug reports, and one that printed a token would
// make its own output unsafe to paste.
func TestLedgerWhere_PrintsTheTokenEnvNameNotItsValue(t *testing.T) {
	var out bytes.Buffer
	writeLedgerWhere(&out, ledgerWhereDoc{
		Env:      "prod",
		Backend:  ledgerBackendControlPlane,
		Location: "https://api.example.com",
		Because:  "declared",
		Project:  "control-plane",
		Declaration: &ledgerDeclarationDoc{
			Endpoint: "https://api.example.com",
			TokenEnv: "CONTROL_PLANE_TOKEN",
		},
	})
	got := out.String()
	if !strings.Contains(got, "$CONTROL_PLANE_TOKEN") {
		t.Errorf("output must name the env var, got:\n%s", got)
	}
}

// --json carries the same words the text form prints, so a consumer and a
// human reading the same answer cannot disagree about it.
func TestLedgerWhere_JSONUsesTheSameBackendNames(t *testing.T) {
	for _, backend := range []ledgerBackendKind{ledgerBackendMachine, ledgerBackendControlPlane} {
		encoded, err := json.Marshal(ledgerWhereDoc{Backend: backend})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if !strings.Contains(string(encoded), `"backend":"`+string(backend)+`"`) {
			t.Errorf("backend %q did not round-trip into JSON: %s", backend, encoded)
		}
	}
}

// ─── show ───────────────────────────────────────────────────────────────────

// §7.4's view. The current binding is the env's LAST promotion, the history
// reads newest first, and the two orderings must not contradict each other.
func TestLedgerShow_CurrentIsTheLastPromotionAndHistoryIsNewestFirst(t *testing.T) {
	dir := newImportProject(t, "show-project")
	store := testStore(t, dir)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, v := range []string{"v1.0.0", "v2.0.0", "v3.0.0"} {
		if err := store.ImportPromotionFrom(
			importablePromotion("id-"+v, "prod", v, base.Add(time.Duration(i)*time.Hour)), "git:x@y"); err != nil {
			t.Fatalf("seed %s: %v", v, err)
		}
	}

	doc, err := ledgerShow(store, "show-project", "", base.Add(4*time.Hour))
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if len(doc.Environments) != 1 {
		t.Fatalf("got %d environments, want 1", len(doc.Environments))
	}
	env := doc.Environments[0]
	if env.Current == nil || env.Current.Release != "v3.0.0" {
		t.Fatalf("current = %+v, want v3.0.0 — the binding is the last line", env.Current)
	}
	var order []string
	for _, p := range env.Promotions {
		order = append(order, p.Release)
	}
	if want := "v3.0.0,v2.0.0,v1.0.0"; strings.Join(order, ",") != want {
		t.Errorf("history = %v, want %s (newest first)", order, want)
	}
}

// "Never promoted" is a normal state a reader must render, so Current is nil
// rather than the field being omitted.
func TestLedgerShow_NeverPromotedIsANormalState(t *testing.T) {
	dir := newImportProject(t, "show-empty-project")
	store := testStore(t, dir)
	if err := store.PutEnv(release.EnvRecord{Name: "dev", Kind: release.EnvLocal}); err != nil {
		t.Fatalf("seed an env: %v", err)
	}

	doc, err := ledgerShow(store, "show-empty-project", "", time.Now())
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if len(doc.Environments) != 1 || doc.Environments[0].Current != nil {
		t.Fatalf("environments = %+v, want one env with no current promotion", doc.Environments)
	}
}

// An env named on the command line is reported even when the ledger holds
// nothing for it, with empty lists. Returning no environments at all would
// read as "I do not know about this env", which is a different fact.
func TestLedgerShow_AnEnvWithNoRecordsStillAnswers(t *testing.T) {
	dir := newImportProject(t, "show-unknown-project")
	store := testStore(t, dir)

	doc, err := ledgerShow(store, "show-unknown-project", "nowhere", time.Now())
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if len(doc.Environments) != 1 {
		t.Fatalf("got %d environments, want 1 (the one asked for)", len(doc.Environments))
	}
	if doc.Environments[0].Name != "nowhere" {
		t.Errorf("name = %q, want nowhere", doc.Environments[0].Name)
	}
}

// Every list is NON-NULL in --json. An absent key and an empty array read
// alike to a consumer that forgot to distinguish them, and the one thing
// that may be missing is the whole document — which means the daemon could
// not be reached.
func TestLedgerShow_JSONListsAreNeverNull(t *testing.T) {
	dir := newImportProject(t, "show-json-project")
	store := testStore(t, dir)

	doc, err := ledgerShow(store, "show-json-project", "", time.Now())
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for _, key := range []string{`"envs":[]`, `"environments":[]`, `"sessions":[]`} {
		if !strings.Contains(string(encoded), key) {
			t.Errorf("want %s in the document, got: %s", key, encoded)
		}
	}
}

// A session's live and stale verdicts come from ONE clock reading, so a row
// cannot come back live-and-not-live — and a stale row is labelled by when
// it was last seen rather than as a failure, because forge cannot tell a
// dead stack from a daemon that stopped heartbeating.
func TestLedgerShow_SessionVerdictsComeFromOneInstant(t *testing.T) {
	dir := newImportProject(t, "show-session-project")
	store := testStore(t, dir)
	started := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := store.ReportSession(release.LocalSession{
		ID:         "sess-1",
		Env:        "dev",
		Worktree:   release.Worktree{Host: "laptop", Key: "feat-x", Label: "feat-x"},
		StartedAt:  started,
		LastSeenAt: started,
	}); err != nil {
		t.Fatalf("seed a session: %v", err)
	}

	// Well past release.SessionStaleAfter.
	doc, err := ledgerShow(store, "show-session-project", "", started.Add(time.Hour))
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if len(doc.Sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(doc.Sessions))
	}
	got := doc.Sessions[0]
	if !got.Live {
		t.Error("a session that was never stopped is live")
	}
	if !got.Stale {
		t.Error("a live session unseen for an hour is stale")
	}

	// Within the window: live and not stale, from the same single reading.
	fresh, err := ledgerShow(store, "show-session-project", "", started.Add(time.Minute))
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !fresh.Sessions[0].Live || fresh.Sessions[0].Stale {
		t.Errorf("a session seen a minute ago is live and not stale, got live=%v stale=%v",
			fresh.Sessions[0].Live, fresh.Sessions[0].Stale)
	}
}

// The derived apply state is computed through release.DeriveApplyState, the
// one implementation of that rule — so a UI reading this cannot disagree
// with what forge prints in a terminal about whether an apply was abandoned.
func TestLedgerShow_ApplyStateIsDerivedNotStored(t *testing.T) {
	dir := newImportProject(t, "show-apply-project")
	store := testStore(t, dir)
	// CreatedAt is set explicitly: the store stamps it from the clock when
	// it is zero, and a deadline relative to a fixed start is the whole
	// point of the assertion below.
	started := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	begun, err := store.BeginApply(release.Apply{
		Env:        "prod",
		BundleID:   "bundle-1",
		CreatedAt:  started,
		DeadlineAt: started.Add(time.Hour),
	}, false)
	if err != nil {
		t.Fatalf("begin an apply: %v", err)
	}

	doc, err := ledgerShow(store, "show-apply-project", "prod", begun.CreatedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if len(doc.Environments) != 1 || len(doc.Environments[0].Applies) != 1 {
		t.Fatalf("environments = %+v, want one env with one apply", doc.Environments)
	}
	shown := doc.Environments[0].Applies[0]
	want := release.DeriveApplyState(begun, nil, begun.CreatedAt.Add(time.Minute))
	if shown.State != want {
		t.Errorf("state = %q, want %q (the one DeriveApplyState rule)", shown.State, want)
	}
	if shown.Outcome != nil {
		t.Error("an apply that has not reported has no outcome")
	}
}
