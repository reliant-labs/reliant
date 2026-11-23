package deploy

import (
	"time"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// ObservedState is the destination-neutral health vocabulary: the ONE mapping
// between a tier's Phase, the control plane's deployments.observed_state
// column, and forge's deploystate verdicts.
//
// The three vocabularies disagreed. control-plane's ledger had seven values
// (pending, progressing, ready, degraded, suspended, deleted, unknown).
// forge's deploystate had converged / converging / diverged / degraded /
// unknown. The tiers had a phase set per kind. The semantic mismatch that
// matters is "converged": control-plane called a deployment ready the
// instant a replica was ready, while forge's definition requires the state
// to HOLD for a stability window. A pod that is ready, crashes, and comes
// back ready every 90 seconds is ready at every instant anyone samples it,
// and converged at none of them. forge's definition wins because it is the
// one that answers the question a promotion gate asks.
type ObservedState string

// The values are the control plane's observed_state column, spelled
// identically so the ledger can store them unconverted.
const (
	ObservedPending     ObservedState = "pending"
	ObservedProgressing ObservedState = "progressing"
	ObservedReady       ObservedState = "ready"
	ObservedDegraded    ObservedState = "degraded"
	ObservedSuspended   ObservedState = "suspended"
	ObservedDeleted     ObservedState = "deleted"
	ObservedUnknown     ObservedState = "unknown"
)

// ObservedStateOf maps a tier Phase onto the ledger vocabulary.
//
//	Phase        observed_state   why
//	Pending      pending          declared, nothing applied
//	Progressing  progressing      applied, converging
//	Ready        ready            serving what was declared
//	Degraded     degraded         exists but not serving
//	Failed       degraded         the platform refused; nothing serves.
//	                              The ledger has no "failed", and degraded
//	                              is the one that pages.
//	Locked       deleted          the resource is gone; the retained data is
//	                              not addressable through it
//	""/other     unknown          unset is visible as a value, never as ready
func ObservedStateOf(p v1alpha1.Phase) ObservedState {
	switch p {
	case v1alpha1.PhasePending:
		return ObservedPending
	case v1alpha1.PhaseProgressing:
		return ObservedProgressing
	case v1alpha1.PhaseReady:
		return ObservedReady
	case v1alpha1.PhaseDegraded, v1alpha1.PhaseFailed:
		return ObservedDegraded
	case v1alpha1.PhaseLocked:
		return ObservedDeleted
	default:
		return ObservedUnknown
	}
}

// DefaultStabilityWindow is how long a tier must have been continuously ready
// before it counts as converged.
const DefaultStabilityWindow = 2 * time.Minute

// Converged is forge's definition: ready now AND continuously ready for at
// least window. It takes the time of the last transition INTO ready (the Ready
// condition's lastTransitionTime) and the current time as arguments, so it
// stays pure and every caller agrees given the same observation.
//
// A zero readySince means the transition time is unknown, and that is NOT
// converged. Treating an unknown duration as "long enough" is the instant
// "ready" this definition exists to replace.
func Converged(p v1alpha1.Phase, readySince, now time.Time, window time.Duration) bool {
	if ObservedStateOf(p) != ObservedReady || readySince.IsZero() {
		return false
	}
	return now.Sub(readySince) >= window
}
