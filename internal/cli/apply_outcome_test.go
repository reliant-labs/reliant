package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// HistoryPage lets the in-memory ledger answer "what ran before", newest
// first, the way both real ledgers do.
func (m *memBindingStore) HistoryPage(_ context.Context, env string, _ historyQuery) (historyPage, error) {
	h := m.history[env]
	out := make([]release.Promotion, 0, len(h))
	for i := len(h) - 1; i >= 0; i-- {
		out = append(out, h[i])
	}
	return historyPage{Promotions: out}, nil
}

// failedApplyGate is the evidence a failed client-side apply leaves on a
// hosted promotion.
func failedApplyGate(summary string) release.Gate {
	return release.Gate{Name: applyGateName, Status: release.GateStatusFailed, Summary: summary}
}

// notAppliedFixture is prod after the 2026-10-07 release: bound to v2, whose
// apply failed, while v1 is what actually runs.
func notAppliedFixture() (*memBindingStore, *memReleaseLedger) {
	store := newMemBindingStore(map[string]release.Promotion{"prod": {ID: "p-1", Release: "v1",
		Resolved: map[string]string{"api": sha("1")}}})
	store.history["prod"] = append(store.history["prod"], release.Promotion{
		ID: "p-2", Env: "prod", Release: "v2", Kind: release.KindPromote,
		Resolved:      map[string]string{"api": sha("2")},
		RecordedGates: []release.Gate{failedApplyGate("deploy preflight failed — Images not found: temporalio/auto-setup:1.26.2")},
	})
	_, releases := selfManagedFixture()
	return store, releases
}

// A re-plan after a failed apply must not call the re-deploy a no-op. The
// 2026-10-07 re-plan said `direction SAME — NO MOVE` and `Images (4 unchanged,
// 0 changed)` while prod still ran the previous digests; applying it rolled 6
// Deployments and a DaemonSet.
func TestPromotePlan_NotAppliedBindingDiffsFromWhatRuns(t *testing.T) {
	store, releases := notAppliedFixture()
	plan, err := computePromotePlan(context.Background(), promotePlanOptions{
		Env: "prod", Version: "v2", ProjectDir: t.TempDir(),
		Bindings: store, Releases: releases, Git: allCommitsPresent(),
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Tally.Changed != 1 || !plan.Changed {
		t.Errorf("tally %+v, changed=%v: the image the target still runs at v1 must show as changed", plan.Tally, plan.Changed)
	}
	var out bytes.Buffer
	renderPromotePlanText(&out, plan)
	for _, want := range []string{"NOT APPLIED", "live runs v1", "Images live v1 → target"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the plan does not say %q:\n%s", want, out.String())
		}
	}
}

// After a recorded-but-not-applied deploy, re-running the same command with
// the same --approve is refused as plan_stale: the binding moved, so the plan
// did too. The hint must say what actually works.
func TestRenderPromotePlan_FailedApplyHintSaysReplanAndReapprove(t *testing.T) {
	plan := promotePlan{Env: "prod", Applied: true, FollowError: "apply failed",
		Target: promotePlanTarget{Release: "v2"}, Images: []promoteImageChange{}}
	var out bytes.Buffer
	renderPromotePlanText(&out, plan)
	got := out.String()
	if strings.Contains(got, "to apply it again") {
		t.Errorf("the hint still says to re-run the same command, which is refused as plan_stale:\n%s", got)
	}
	if !strings.Contains(got, "forge env deploy prod v2 --plan-only") || !strings.Contains(got, "--approve") {
		t.Errorf("the hint must say re-plan and re-approve:\n%s", got)
	}
}

// `env status --history` marks a promotion whose apply failed.
func TestEnvHistory_MarksAPromotionWhoseApplyFailed(t *testing.T) {
	p := release.Promotion{ID: "p-2", RecordedGates: []release.Gate{failedApplyGate("boom")}}
	if got := evidenceLabel(newEnvHistoryEntry(t.TempDir(), "prod", p)); !strings.Contains(got, "NOT APPLIED") {
		t.Errorf("evidence = %q; want NOT APPLIED", got)
	}
	// A later successful apply of the same promotion clears it.
	p.RecordedGates = append(p.RecordedGates, release.Gate{Name: applyGateName, Status: release.GateStatusPassed})
	if got := evidenceLabel(newEnvHistoryEntry(t.TempDir(), "prod", p)); strings.Contains(got, "NOT APPLIED") {
		t.Errorf("evidence = %q after a successful re-apply; want it cleared", got)
	}
}

// v1785LedgerFixture is control-plane prod on 2026-10-31: v1.7.83 applied,
// then v1.7.85 promoted and its apply refused before anything shipped.
func v1785LedgerFixture() *memBindingStore {
	store := newMemBindingStore(map[string]release.Promotion{"prod": {ID: "p-83", Release: "v1.7.83",
		Resolved:      map[string]string{"ghcr.io/acme/api": sha("3")},
		RecordedGates: []release.Gate{{Name: applyGateName, Status: release.GateStatusPassed, Summary: "applied from this machine"}}}})
	store.history["prod"] = append(store.history["prod"], release.Promotion{
		ID: "p-85", Env: "prod", Release: "v1.7.85", Kind: release.KindPromote,
		Resolved:      map[string]string{"ghcr.io/acme/api": sha("5")},
		RecordedGates: []release.Gate{failedApplyGate(`release component reliant-temporal-worker has missing predecessor "workspace-base"`)},
	})
	return store
}

// `--history --json` says, per promotion, whether it was applied. A script
// that read `.promotions[0].release` as "prod applied this" (release-local.sh
// --resume-web) shipped v1.7.85's web half against an API still on v1.7.83;
// `.promotions[0].apply.status` is what it checks now.
func TestEnvHistory_JSONSaysWhetherEachPromotionWasApplied(t *testing.T) {
	store := v1785LedgerFixture()
	store.history["prod"] = append([]release.Promotion{{ID: "p-81", Env: "prod", Release: "v1.7.81"}}, store.history["prod"]...)
	var err error
	out := captureStdout(t, func() {
		err = runEnvHistory(context.Background(), store, "prod", historyQuery{}, true, &bytes.Buffer{})
	})
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	var doc struct {
		Promotions []struct {
			Release string `json:"release"`
			Apply   struct {
				Status  string `json:"status"`
				Summary string `json:"summary"`
			} `json:"apply"`
		} `json:"promotions"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	want := []string{"v1.7.85 failed", "v1.7.83 passed", "v1.7.81 unrecorded"}
	var got []string
	for _, p := range doc.Promotions {
		got = append(got, p.Release+" "+p.Apply.Status)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("apply per promotion = %v, want %v", got, want)
	}
	if !strings.Contains(doc.Promotions[0].Apply.Summary, "missing predecessor") {
		t.Errorf("the failed apply does not say what failed: %q", doc.Promotions[0].Apply.Summary)
	}
}

// `env status <env>` says plainly what runs when the bound release's apply
// failed: the newest applied release, and the bound one as NOT APPLIED —
// before any per-image line, and in --json as `apply`.
func TestEnvStatus_SaysWhatRunsWhenTheBoundReleaseWasNotApplied(t *testing.T) {
	opts := envStatusOptions{
		Bindings: v1785LedgerFixture(),
		Lister:   &stubLister{images: []cluster.WorkloadImage{runningImage("ghcr.io/acme/api", sha("3"))}},
		Resolver: stubResolver{target: envTarget{KubeContext: "test-context", Namespace: "test-ns"}},
	}
	var err error
	out := captureStdout(t, func() { err = runEnvStatusRelease(context.Background(), "prod", opts) })
	if got := exitCodeForError(err); got != exitWrong {
		t.Errorf("exit %d (%v), want %d: the env does not run what it is bound to", got, err, exitWrong)
	}
	want := `current  v1.7.83 applied; v1.7.85 promoted but NOT APPLIED (apply failed: release component reliant-temporal-worker has missing predecessor "workspace-base")`
	if !strings.Contains(out, want) {
		t.Errorf("status does not say what runs:\nwant %s\n%s", want, out)
	}
	if !strings.Contains(out, "apply    FAILED (recorded on the ledger)") {
		t.Errorf("status does not show the recorded apply gate:\n%s", out)
	}
	if i, j := strings.Index(out, "NOT APPLIED"), strings.Index(out, "drift"); i < 0 || (j >= 0 && j < i) {
		t.Errorf("NOT APPLIED must come before the per-image verdicts:\n%s", out)
	}

	opts.JSON = true
	out = captureStdout(t, func() { _ = runEnvStatusRelease(context.Background(), "prod", opts) })
	var doc struct {
		Release string         `json:"release"`
		Apply   envApplyReport `json:"apply"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if doc.Release != "v1.7.85" || doc.Apply.Status != applyStatusFailed ||
		doc.Apply.LastAppliedRelease != "v1.7.83" || doc.Apply.LastAppliedPromotionID != "p-83" || doc.Apply.LastAppliedStatus != applyStatusPassed {
		t.Errorf("json = release %q apply %+v; want bound v1.7.85, apply failed, last applied v1.7.83 (p-83, passed)", doc.Release, doc.Apply)
	}
}

// A bound release whose apply passed reads as applied, with no "current" line.
func TestEnvStatus_AppliedReleaseHasNoNotAppliedLine(t *testing.T) {
	store := v1785LedgerFixture()
	h := store.history["prod"]
	h[len(h)-1].RecordedGates = append(h[len(h)-1].RecordedGates, release.Gate{Name: applyGateName, Status: release.GateStatusPassed})
	var err error
	out := captureStdout(t, func() {
		err = runEnvStatusRelease(context.Background(), "prod", envStatusOptions{
			Bindings: store,
			Lister:   &stubLister{images: []cluster.WorkloadImage{runningImage("ghcr.io/acme/api", sha("5"))}},
			Resolver: stubResolver{target: envTarget{KubeContext: "test-context", Namespace: "test-ns"}},
		})
	})
	if err != nil {
		t.Fatalf("a re-applied, matching env must exit 0: %v", err)
	}
	if strings.Contains(out, "NOT APPLIED") || !strings.Contains(out, "apply    SUCCEEDED (recorded on the ledger)") {
		t.Errorf("a re-applied release must read as applied:\n%s", out)
	}
}

// A hosted env's client-side apply files its verdict on the promotion it
// realized, so every reader of the control plane's ledger — not just this
// machine — can tell a recorded release from a running one.
//
// A mixed env whose hosted half is STILL APPLYING records no apply gate at
// all: a passed one would claim a hosted bundle nobody has confirmed applied,
// and a failed one would make the next plan report a live release NOT APPLIED.
func TestDeployRelease_StillApplyingRecordsNoApplyGate(t *testing.T) {
	var wait capturedWait
	wait.install(t)
	pending := &deploytarget.ApplyPendingError{Env: "prod", Bundle: "sha256:new", Waited: 10 * time.Minute}
	apply := capturedClientDeploy{err: fmt.Errorf("deploy hosted: %w", pending)}
	apply.install(t)
	fake, store := hostedPromoteFixture(t, "v1")
	gstore := newGateTestStore(t, fake)
	useGateBackend(t, gateBackend{hosted: true, store: gstore, bindings: store})

	_, err := runHostedPromote(t, store, "v2", promoteOptions{
		Ledger: envLedger{Bindings: store, Releases: store, Hosted: true, Mixed: true, HostedTiers: []hostedTier{{Name: "api", Kind: hostedTierWorkload}}},
		Follow: waitByDefault(),
	})
	if got := exitCodeForError(err); got != exitTimedOut {
		t.Errorf("exit code = %d (err %v), want %d (still applying)", got, err, exitTimedOut)
	}
	cur, _, _ := store.Current(context.Background(), "prod")
	gates, lerr := gstore.listGates(context.Background(), cur.ID)
	if lerr != nil {
		t.Fatalf("list gates: %v", lerr)
	}
	for _, g := range gates {
		if g.Name == applyGateName {
			t.Fatalf("a still-applying deploy recorded an apply gate %+v; want none", g)
		}
	}
}

func TestDeployRelease_HostedApplyVerdictIsRecordedOnThePromotion(t *testing.T) {
	for _, tc := range []struct {
		name     string
		applyErr error
		want     release.GateStatus
	}{
		{"failed", &exitCodeError{code: exitWrong, msg: "worker: CrashLoopBackOff"}, release.GateStatusFailed},
		{"passed", nil, release.GateStatusPassed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wait capturedWait
			wait.install(t)
			apply := capturedClientDeploy{err: tc.applyErr}
			apply.install(t)
			fake, store := hostedPromoteFixture(t, "v1")
			gstore := newGateTestStore(t, fake)
			useGateBackend(t, gateBackend{hosted: true, store: gstore, bindings: store})

			_, _ = runHostedPromote(t, store, "v2", promoteOptions{
				Ledger: envLedger{Bindings: store, Releases: store, Hosted: true, Mixed: true, HostedTiers: []hostedTier{{Name: "api", Kind: hostedTierWorkload}}},
				Follow: waitByDefault(),
			})
			cur, _, _ := store.Current(context.Background(), "prod")
			gates, err := gstore.listGates(context.Background(), cur.ID)
			if err != nil {
				t.Fatalf("list gates: %v", err)
			}
			var got *release.Gate
			for i := range gates {
				if gates[i].Name == applyGateName {
					got = &gates[i]
				}
			}
			if got == nil || got.Status != tc.want {
				t.Fatalf("apply gate on %s = %+v; want status %s", cur.ID, got, tc.want)
			}
			if tc.applyErr != nil && !strings.Contains(got.Summary, "CrashLoopBackOff") {
				t.Errorf("the failed apply's gate does not say what failed: %q", got.Summary)
			}
			if got.StartedAt == nil || got.FinishedAt == nil || got.FinishedAt.Before(got.StartedAt.Add(-time.Second)) {
				t.Errorf("the apply gate carries no window: %+v", got)
			}
		})
	}
}
