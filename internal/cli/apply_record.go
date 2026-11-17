package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// applyRecordBudget is how long a recorded apply may stay unreported before a
// reader sees it as ABANDONED (a forge killed mid-apply never writes an outcome).
const applyRecordBudget = 30 * time.Minute

// beginApplyRecord brackets a self-managed apply with the machine ledger's own
// Apply / ApplyOutcome records and returns the closer.
//
// WHY THIS EXISTS. A promotion is recorded BEFORE the apply (the ledger decides,
// then something converges reality to it), so a failed apply leaves a promotion
// that says "prod is on vX" with nothing running vX. Moving the write after the
// apply would break the compare-and-set and every reconciled/hosted env that
// reads the promotion as desired state. The truth belongs in a SECOND record:
// the apply's outcome. The ledger already had the storage for it (and the plan
// already diffs against the newest SUCCEEDED apply); forge simply never wrote one.
//
// A re-apply of the same release is unaffected: release.Decide returns the
// existing promotion for a same-release promote, so the deploy runs the apply
// again rather than being refused as "already on vX".
//
// BEST-EFFORT by design: a ledger that cannot be written must not block the
// apply it is describing. Only the machine ledger is bracketed — a hosted env's
// outcome is observed by its control plane, and a record forge wrote there would
// claim work the control plane did.
func beginApplyRecord(env string, plan promotePlan, ledger envLedger, projectDir string) func(error) {
	noop := func(error) {}
	if ledger.Hosted || plan.Recorded == nil {
		return noop
	}
	if projectDir == "" {
		projectDir = projectDirForKCL()
	}
	store, err := openMachineLedger(projectDir)
	if err != nil {
		return noop
	}
	bundles, err := store.Bundles(env)
	if err != nil {
		return noop
	}
	bundleID := ""
	for i := len(bundles) - 1; i >= 0; i-- {
		if bundles[i].Release == plan.Recorded.Release {
			bundleID = bundles[i].ID
			break
		}
	}
	if bundleID == "" {
		return noop
	}
	now := time.Now().UTC().Truncate(time.Second)
	apply, err := store.BeginApply(release.Apply{
		Env:         env,
		BundleID:    bundleID,
		PromotionID: plan.Recorded.ID,
		AppliedBy:   promoteActor("").User,
		PlanDigest:  plan.Recorded.PlanDigest,
		CreatedAt:   now,
		DeadlineAt:  now.Add(applyRecordBudget),
	}, true)
	if err != nil {
		fmt.Printf("[deploy] Note: the apply could not be recorded in the ledger: %v\n", err)
		return noop
	}
	return func(applyErr error) {
		outcome := release.ApplyOutcome{ApplyID: apply.ID, Status: release.ApplySucceeded, FinishedAt: time.Now().UTC().Truncate(time.Second)}
		if applyErr != nil {
			outcome.Status = release.ApplyFailed
			outcome.Summary = oneLine(applyErr.Error(), 300)
		}
		if ferr := store.FinishApply(env, outcome); ferr != nil {
			fmt.Printf("[deploy] Note: the apply outcome could not be recorded in the ledger: %v\n", ferr)
		}
	}
}

func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > limit {
		s = s[:limit] + "…"
	}
	return s
}
