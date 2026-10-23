package cli

// Whether a promotion was APPLIED, as the ledger can say it.
//
// A promotion is a pointer, and the apply that realizes it is a second event
// that can fail. On 2026-10-07 prod's binding moved to a release whose apply
// then failed, and every reader — `env status`, `--history`, the next plan —
// presented that release as what prod ran. Nothing in the ledger said
// otherwise, because nothing had been written to say it.
//
// Both ledgers now record the apply's verdict on the promotion it realized:
//
//   - a HOSTED ledger as a recorded gate named "apply" (the same post-promote
//     evidence `forge gate record` writes), so every reader of the control
//     plane's ledger sees it, not just this machine;
//   - a FILE ledger as the machine ledger's apply record (beginApplyRecord),
//     which already existed and is read here.
//
// The readers below turn either into one fact: this promotion's latest apply
// FAILED, with what it said.
//
// The hosted gate is also a LEDGER-ONLY env's applied baseline: its details
// name the bundle the apply shipped, and the next plan diffs against the
// newest passed one (deploy_plan_applied.go). The machine ledger's apply
// record already served a file-ledger env that way.

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// applyGateName is the recorded gate a client-side apply's verdict is filed
// under on a hosted ledger.
const applyGateName = "apply"

// The apply gate's details keys for the delivery-unit split: which units forge
// applied from this machine, and which it left to the hub. Recorded beside the
// applied bundle so the ledger says, per promotion, who applied what: an env
// moving onto Flux one unit at a time has promotions applied by both, and a
// reader asking "did forge touch the reliant unit on this promotion" must not
// have to re-render an old declaration.
const (
	applyGateUnitsForge = "units_forge"
	applyGateUnitsHub   = "units_hub"
)

// recordApplyOutcome files the client-side apply's verdict on the promotion
// it realized, for a HOSTED ledger. Best-effort: the deploy's outcome is the
// apply's, and a ledger that cannot take the evidence must not change it — so
// a failure to record is printed, never returned.
//
// A deploy held on a person (queued) recorded nothing about the cluster half
// worth judging, so it records nothing here.
func recordApplyOutcome(ctx context.Context, env string, plan promotePlan, ledger envLedger, started time.Time, applyErr error, o promoteFollowOptions) {
	if ledger.Offline != nil && plan.Recorded != nil && !o.clientDeploy.dryRun {
		recordOfflineApplyOutcome(env, plan, ledger, started, applyErr, o)
		return
	}
	if !ledger.Hosted || plan.Recorded == nil || o.clientDeploy.dryRun || queuedOf(applyErr) != nil {
		return
	}
	gate := applyOutcomeGate(started, time.Now(), applyErr,
		appliedBundleDigest(env, plan.Recorded.Release, o.clientDeploy), o.planDigest)
	gate = withUnitSplit(gate, ledger.Units)
	backend, err := resolveGateBackend(ctx, env)
	if err == nil && backend.store == nil {
		err = fmt.Errorf("no gate store for %s", env)
	}
	if err == nil {
		_, _, err = backend.store.recordGate(ctx, plan.Recorded.ID, gate)
	}
	if err != nil {
		o.notice("[deploy] Note: the apply's %s verdict could not be recorded on promotion %s: %v\n",
			gate.Status, plan.Recorded.ID, err)
	}
}

// appliedBundleDigest is the bundle an apply of release shipped WHOLE: the one
// this process recorded for (env, release), or "" for a SCOPED apply
// (--target, --frontends-only). A scoped apply shipped part of the bundle, so
// naming it would make the next plan treat the rest as applied too — and
// under-report every change it skipped. Naming none leaves the next plan's
// Live honestly unknown until a whole-env deploy records one again.
func appliedBundleDigest(env, version string, applied deployOptions) string {
	if len(applied.targets) > 0 || applied.frontendsOnly {
		return ""
	}
	return recordedBundleDigest(env, version)
}

// applyOutcomeGate is the evidence one client-side apply leaves on the
// promotion it realized.
//
// ITS DETAILS ARE THE ENV'S APPLIED BASELINE. bundleDigest names the bundle
// this apply shipped — the release's bundle of record, whose objects the
// deploy verified its own render against — and a ledger-only env's next plan
// diffs against the newest passed gate's bundle (appliedBaseline). Without it
// that plan has nothing recorded to diff against, and reports every object as
// added and no removal at all. planDigest is the review that approved it,
// which a ledger-only env's promotion cannot carry (serverPlanDigest).
//
// Either may be "": a bundle that was never recorded, a deploy with no plan.
// A gate with no bundle digest is still the truth about the apply; it just
// cannot serve as a baseline, and the next plan says so.
func applyOutcomeGate(started, finished time.Time, applyErr error, bundleDigest, planDigest string) release.Gate {
	startedUTC, finishedUTC := started.UTC(), finished.UTC()
	gate := release.Gate{
		Name: applyGateName, Status: release.GateStatusPassed,
		Summary:   "applied from this machine",
		StartedAt: &startedUTC, FinishedAt: &finishedUTC,
	}
	if applyErr != nil {
		gate.Status = release.GateStatusFailed
		gate.Summary = oneLine(applyErr.Error(), 300)
	}
	details := map[string]any{}
	if bundleDigest != "" {
		details[applyGateBundleDigest] = bundleDigest
	}
	if planDigest != "" {
		details[applyGatePlanDigest] = planDigest
	}
	if len(details) > 0 {
		gate.Details = details
	}
	return gate
}

// withUnitSplit records on the apply gate which delivery units forge applied and
// which it left to the hub. Nothing is added for an env that declares no split,
// so every gate recorded before the field is unchanged.
func withUnitSplit(g release.Gate, split unitSplit) release.Gate {
	if split.Source == unitSplitNone || len(split.Hub)+len(split.Forge) == 0 {
		return g
	}
	if g.Details == nil {
		g.Details = map[string]any{}
	}
	g.Details[applyGateUnitsForge] = append([]string{}, split.Forge...)
	g.Details[applyGateUnitsHub] = append([]string{}, split.Hub...)
	return g
}

// promotionApply is what the ledger says about whether a promotion was
// applied. The zero value is "nothing recorded", which is not a failure:
// promotions written before this evidence existed, and every env the control
// plane converges itself, carry none.
type promotionApply struct {
	// Failed is true when the LATEST recorded apply of the promotion did not
	// succeed (failed, or timed out).
	Failed bool
	// Summary is what the failed apply said.
	Summary string
}

// applyOf reads a promotion's apply evidence: the latest "apply" gate on a
// hosted promotion, else the latest machine-ledger apply record for it.
func applyOf(projectDir, env string, p release.Promotion) promotionApply {
	if g, ok := latestApplyGate(p); ok {
		return promotionApply{Failed: g.Status == release.GateStatusFailed, Summary: g.Summary}
	}
	store, err := openMachineLedger(projectDir)
	if err != nil {
		return promotionApply{}
	}
	records, err := store.Applies(env)
	if err != nil {
		return promotionApply{}
	}
	for i := len(records) - 1; i >= 0; i-- {
		r := records[i]
		if r.Apply.PromotionID != p.ID || r.Outcome == nil {
			continue
		}
		return promotionApply{Failed: r.Outcome.Status != release.ApplySucceeded, Summary: r.Outcome.Summary}
	}
	return promotionApply{}
}

// latestApplyGate is the newest "apply" gate recorded on p, in append order.
func latestApplyGate(p release.Promotion) (release.Gate, bool) {
	for i := len(p.RecordedGates) - 1; i >= 0; i-- {
		if p.RecordedGates[i].Name == applyGateName {
			return p.RecordedGates[i], true
		}
	}
	return release.Gate{}, false
}

// lastAppliedPromotion is the newest promotion of env before the current one
// whose apply did not fail — the ledger's best answer to "what is actually
// running" when the current binding's apply failed. Reads at most one page of
// history; false when there is none, or the ledger cannot list history.
func lastAppliedPromotion(ctx context.Context, bindings bindingStore, projectDir, env, currentID string) (release.Promotion, bool) {
	reader, ok := bindings.(bindingHistoryReader)
	if !ok {
		return release.Promotion{}, false
	}
	page, err := reader.HistoryPage(ctx, env, historyQuery{Limit: 50})
	if err != nil {
		return release.Promotion{}, false
	}
	for _, p := range page.Promotions {
		if p.ID == currentID {
			continue
		}
		if !applyOf(projectDir, env, p).Failed {
			return p, true
		}
	}
	return release.Promotion{}, false
}
