package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

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
	e := envHistoryEntry{Promotion: release.Promotion{RecordedGates: []release.Gate{failedApplyGate("boom")}}}
	if got := evidenceLabel(e); !strings.Contains(got, "NOT APPLIED") {
		t.Errorf("evidence = %q; want NOT APPLIED", got)
	}
	// A later successful apply of the same promotion clears it.
	e.RecordedGates = append(e.RecordedGates, release.Gate{Name: applyGateName, Status: release.GateStatusPassed})
	if got := evidenceLabel(e); strings.Contains(got, "NOT APPLIED") {
		t.Errorf("evidence = %q after a successful re-apply; want it cleared", got)
	}
}

// A hosted env's client-side apply files its verdict on the promotion it
// realized, so every reader of the control plane's ledger — not just this
// machine — can tell a recorded release from a running one.
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
