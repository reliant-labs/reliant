package cli

import (
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// latestApplyLine says how the apply of the CURRENT promotion ended, from the
// machine ledger's own apply records, or "" when the ledger holds none for it.
//
// The promotion timestamp is not a deploy time and the verify below can only
// say DRIFT; neither tells a reader that the last attempt to apply this release
// FAILED. Printed beside "promoted" so the two facts are read together: bound
// to vX, apply failed. A promotion with no apply record (an older forge, a
// reconciled env, a hosted one) prints nothing rather than a guess.
func latestApplyLine(projectDir, env, promotionID string, now time.Time) string {
	if promotionID == "" {
		return ""
	}
	store, err := openMachineLedger(projectDir)
	if err != nil {
		return ""
	}
	applies, err := store.Applies(env)
	if err != nil {
		return ""
	}
	for i := len(applies) - 1; i >= 0; i-- {
		a := applies[i]
		if a.Apply.PromotionID != promotionID {
			continue
		}
		return describeApply(release.DeriveApplyState(a.Apply, a.Outcome, now), a.Outcome)
	}
	return ""
}

func describeApply(state release.ApplyState, outcome *release.ApplyOutcome) string {
	switch state {
	case release.ApplyStateOK:
		return "  apply    SUCCEEDED (reported by forge)"
	case release.ApplyStateFail, release.ApplyStateTO:
		detail := ""
		if outcome != nil && outcome.Summary != "" {
			detail = ": " + outcome.Summary
		}
		return fmt.Sprintf("  apply    %s — this release is RECORDED but its apply did not finish; it is not (fully) running%s", state, detail)
	case release.ApplyAbandoned:
		return "  apply    ABANDONED — forge started applying this release and never reported an outcome"
	default:
		return "  apply    RUNNING"
	}
}
