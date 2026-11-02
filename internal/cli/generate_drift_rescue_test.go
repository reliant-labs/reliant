package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/obsdash"
)

func TestOwnedOverviewMigrationSettlesRealPendingDrift(t *testing.T) {
	for _, bothEdited := range []bool{false, true} {
		t.Run(map[bool]string{false: "migration", true: "independent-edits"}[bothEdited], func(t *testing.T) {
			checksums.ResetPerRunState()
			t.Cleanup(checksums.ResetPerRunState)
			dir := t.TempDir()
			cs := &checksums.FileChecksums{}
			legacy := obsdash.DashboardsDir + "/shop-overview_gen.json"
			owned := obsdash.OverridePath("shop", obsdash.Dashboard{ID: "overview"})
			if _, err := checksums.WriteGeneratedFile(dir, legacy, []byte(`{"name":"pristine"}`), cs, true); err != nil {
				t.Fatal(err)
			}
			edited := []byte(`{"name":"edited legacy"}`)
			if err := os.WriteFile(filepath.Join(dir, legacy), edited, 0644); err != nil {
				t.Fatal(err)
			}
			if bothEdited {
				if err := os.WriteFile(filepath.Join(dir, owned), []byte(`{"name":"owned override"}`), 0644); err != nil {
					t.Fatal(err)
				}
			}
			// A previous generation created the certified file. Start the new
			// invocation with clean target/written sets, exactly as production.
			checksums.ResetSkipWrite()
			checksums.ResetPerRunState()
			drift := checksums.ScanTier1Drift(dir, cs)
			if len(drift) != 1 || drift[0].Path != legacy {
				t.Fatalf("tracked legacy edit was not detected: %v", drift)
			}
			ctx := &pipelineContext{AbsPath: dir, Checksums: cs}
			quarantineDriftForRescue(ctx, drift)
			if _, err := obsdash.Write(dir, obsdash.Input{Project: "shop"}, cs); err != nil {
				t.Fatal(err)
			}
			if checksums.IsTier1Target(legacy) {
				t.Fatal("legacy ownership must not be a current generated target")
			}
			if err := stepRetireObsoleteDisowns(ctx); err != nil {
				t.Fatal(err)
			}
			if cs.IsDisowned(legacy) {
				t.Fatal("obsolete legacy disown did not retire before adjudication")
			}
			if err := stepRehashTracked(ctx); err != nil {
				t.Fatal(err)
			}
			if err := stepAdjudicateTier1Drift(ctx); err != nil {
				t.Fatalf("complete ownership lifecycle left pending Tier-1 drift: %v", err)
			}
			got, err := os.ReadFile(filepath.Join(dir, owned))
			if err != nil {
				t.Fatal(err)
			}
			if !bothEdited && string(got) != string(edited) {
				t.Fatalf("edit lost: %s", got)
			}
			if bothEdited {
				if string(got) != `{"name":"owned override"}` {
					t.Fatal("owned edit overwritten")
				}
				if old, err := os.ReadFile(filepath.Join(dir, legacy)); err != nil || string(old) != string(edited) {
					t.Fatal("independent legacy edit lost")
				}
			}
			if len(checksums.ScanTier1Drift(dir, cs)) != 0 {
				t.Fatal("subsequent generate still considers owned legacy edited Tier-1")
			}
		})
	}
}

// checkTier1DriftAndAdjudicate runs the stomp guard and its fresh-render
// check back to back, as a run whose emitters rendered none of the drifted
// paths would: what the guard quarantined is then reported as before.
func checkTier1DriftAndAdjudicate(t *testing.T, ctx *pipelineContext) error {
	t.Helper()
	t.Cleanup(checksums.ResetPerRunState)
	if err := stepCheckTier1Drift(ctx); err != nil {
		return err
	}
	return stepAdjudicateTier1Drift(ctx)
}

// emitterStep stands in for an emitter: it writes files through forge's
// Tier-1 writer, exactly as the observability dashboards step does.
func emitterStep(files map[string]string) GenStep {
	return GenStep{
		Name: "observability dashboards",
		Gate: always,
		Run: func(ctx *pipelineContext) error {
			for rel, body := range files {
				if _, err := checksums.WriteGeneratedFile(ctx.AbsPath, rel, []byte(body), ctx.Checksums, true); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// rescuePlan is the stomp guard, an emitter, and the fresh-render check.
func rescuePlan(t *testing.T, emitted map[string]string) []GenStep {
	return []GenStep{
		productionStep(t, "load checksums"),
		productionStep(t, "check Tier-1 file-stomp guard"),
		emitterStep(emitted),
		productionStep(t, adjudicateDriftStep),
	}
}

func readHashesLedger(t *testing.T, dir string) map[string]string {
	t.Helper()
	cs, err := checksums.Load(dir)
	if err != nil {
		t.Fatalf("load certificates: %v", err)
	}
	return cs.Unstampable
}

// writeHashesLedger writes one certificate file per entry, as Save does.
func writeHashesLedger(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, hash := range files {
		writeTreeFileT(t, filepath.Join(dir, filepath.FromSlash(checksums.HashCertPath(rel))), hash+"\n")
	}
}

// TestGenerate_KilledRunLedgerIsRescued is the finding. A generate killed
// during validation had rewritten three dashboards (JSON, so recorded in
// .forge/hashes.json rather than a marker) but not saved the ledger. The
// next generate refused all three as hand-edited Tier-1 files. They are
// byte-for-byte what that next run renders, so it re-records them and
// succeeds, and the run after it sees no drift at all.
func TestGenerate_KilledRunLedgerIsRescued(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the full generate pipeline; runs in task test")
	}
	dir := t.TempDir()
	v1 := map[string]string{
		"observability/dashboards/api_gen.json":     `{"title":"api","v":1}` + "\n",
		"observability/dashboards/workers_gen.json": `{"title":"workers","v":1}` + "\n",
		"observability/dashboards/db_gen.json":      `{"title":"db","v":1}` + "\n",
	}
	v2 := map[string]string{}
	ledger := map[string]string{}
	for rel, body := range v1 {
		ledger[rel] = checksums.BodyHash([]byte(body)) // the completed run's record
		v2[rel] = strings.Replace(body, `"v":1`, `"v":2`, 1)
		writeTreeFileT(t, filepath.Join(dir, rel), v2[rel]) // the killed run's rewrite
	}
	writeHashesLedger(t, dir, ledger)

	withPipelineSteps(t, rescuePlan(t, v2))
	out, err := runPipelineCapturingOutput(t, dir)
	if err != nil {
		t.Fatalf("forge's own outputs under a stale ledger must not block generate: %v\n%s", err, out)
	}
	if strings.Contains(out, "hand-edited") {
		t.Errorf("no file here was hand-edited:\n%s", out)
	}
	got := readHashesLedger(t, dir)
	for rel, body := range v2 {
		if on, _ := os.ReadFile(filepath.Join(dir, rel)); string(on) != body {
			t.Errorf("%s changed: %q", rel, on)
		}
		if got[rel] != checksums.BodyHash([]byte(body)) {
			t.Errorf("%s: ledger still records the pre-kill render", rel)
		}
	}

	out, err = runPipelineCapturingOutput(t, dir)
	if err != nil || strings.Contains(out, "differ from forge's record") {
		t.Fatalf("the next run must see no drift: %v\n%s", err, out)
	}
}

// TestGenerate_MergedStampedFileIsRescued covers the self-certifying half:
// a merge left a Go file's body as this run renders it under the marker of
// another vintage. It is restamped, not refused.
func TestGenerate_MergedStampedFileIsRescued(t *testing.T) {
	dir := t.TempDir()
	const rel = "internal/fixture/fixture_gen.go"
	rendered := "// Code generated by forge. DO NOT EDIT.\npackage fixture\n\nconst Version = 2\n"
	otherSide := checksums.BodyHash([]byte("// Code generated by forge. DO NOT EDIT.\npackage fixture\n\nconst Version = 1\n"))
	merged, ok := checksums.StampWithValue(rel, []byte(rendered), otherSide)
	if !ok {
		t.Fatal("a .go file must be stampable")
	}
	writeTreeFileT(t, filepath.Join(dir, rel), string(merged))
	if checksums.Verify(merged) != checksums.Modified {
		t.Fatal("precondition: the merged file must read as drift")
	}

	withPipelineSteps(t, rescuePlan(t, map[string]string{rel: rendered}))
	out, err := runPipelineCapturingOutput(t, dir)
	if err != nil {
		t.Fatalf("a merged file equal to this run's render must not block generate: %v\n%s", err, out)
	}
	on, _ := os.ReadFile(filepath.Join(dir, rel))
	if checksums.Verify(on) != checksums.Pristine {
		t.Errorf("the rescued file must be re-certified:\n%s", on)
	}
}

// TestGenerate_HandEditIsStillRefused bounds the rescue: bytes this run
// would not write are a hand-edit, reported as the guard always has, and
// the run's other writes roll back.
func TestGenerate_HandEditIsStillRefused(t *testing.T) {
	dir := t.TempDir()
	const rel = "observability/dashboards/api_gen.json"
	const fresh = "observability/dashboards/new_gen.json"
	rendered := `{"title":"api","v":2}` + "\n"
	ledger := map[string]string{rel: checksums.BodyHash([]byte(`{"title":"api","v":1}` + "\n"))}
	edited := `{"title":"api, tuned by hand","v":1}` + "\n"
	writeTreeFileT(t, filepath.Join(dir, rel), edited)
	writeHashesLedger(t, dir, ledger)

	withPipelineSteps(t, rescuePlan(t, map[string]string{rel: rendered, fresh: "{}\n"}))
	out, err := runPipelineCapturingOutput(t, dir)
	if err == nil {
		t.Fatalf("a hand-edit must still stop generate:\n%s", out)
	}
	for _, want := range []string{rel, "--force", "forge project disown"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %q: %v", want, err)
		}
	}
	if on, _ := os.ReadFile(filepath.Join(dir, rel)); string(on) != edited {
		t.Errorf("the hand-edit was overwritten: %q", on)
	}
	if _, statErr := os.Stat(filepath.Join(dir, fresh)); !os.IsNotExist(statErr) {
		t.Errorf("a refused run must roll back its other writes; %s exists", fresh)
	}
	if got := readHashesLedger(t, dir); got[rel] != ledger[rel] {
		t.Errorf("a refused run must leave the ledger as it was")
	}
}

// TestStepPresetsKeepDriftAdjudication: quarantined drift that is never
// adjudicated would pass silently, so every plan that keeps the guard
// keeps its check — each preset lists it, and the driver appends it to a
// plan that does not.
func TestStepPresetsKeepDriftAdjudication(t *testing.T) {
	for name, allow := range stepPresetAllowlist {
		if allow["check Tier-1 file-stomp guard"] && !allow[adjudicateDriftStep] {
			t.Errorf("step preset %q keeps the stomp guard but not %q", name, adjudicateDriftStep)
		}
	}
	guardOnly := []GenStep{productionStep(t, "check Tier-1 file-stomp guard")}
	got := withDriftAdjudication(guardOnly)
	if len(got) != 2 || got[1].Name != adjudicateDriftStep {
		t.Errorf("withDriftAdjudication did not append the check to a guard-only plan: %v", got)
	}
	if got := withDriftAdjudication([]GenStep{productionStep(t, "load checksums")}); len(got) != 1 {
		t.Errorf("a plan without the guard needs no check, got %d steps", len(got))
	}
}
