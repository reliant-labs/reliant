package prpolicy

import (
	"fmt"

	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

// AttemptState is how many fix attempts a PR has made at one failure:
// `pr_pipeline.fix_signature` and `fix_attempts`.
type AttemptState struct {
	// Signature is the failure the attempts were at. Empty before the first.
	Signature Signature
	// Attempts is how many fix attempts were made at Signature.
	Attempts int
}

// MaxFixAttempts is the fix attempts allowed per signature: pipeline.yaml
// `fix.attempts_per_signature`, or its default when cfg was never resolved.
func MaxFixAttempts(cfg config.ResolvedFix) int {
	if cfg.AttemptsPerSignature < 1 {
		return config.DefaultFixAttempts
	}
	return cfg.AttemptsPerSignature
}

// NextAttempt decides whether a fix may be attempted at sig, the failure the
// PR has now. A new signature starts the count again: the last fix changed
// the failure, which is progress. The same signature counts one more
// attempt, until [MaxFixAttempts] attempts have been made at it; then
// NextAttempt returns state unchanged and exhausted, and the PR needs a
// person (`needs_person`, with [ExhaustedReason] as the reason).
//
// The returned state is the one to record when the attempt starts.
func NextAttempt(state AttemptState, sig Signature, cfg config.ResolvedFix) (next AttemptState, exhausted bool) {
	if sig != state.Signature {
		return AttemptState{Signature: sig, Attempts: 1}, false
	}
	if state.Attempts >= MaxFixAttempts(cfg) {
		return state, true
	}
	return AttemptState{Signature: sig, Attempts: state.Attempts + 1}, false
}

// ExhaustedReason says, in plain words, why a PR whose fix attempts ran out
// needs a person: `pr_pipeline.needs_person_reason`.
func ExhaustedReason(state AttemptState) string {
	return fmt.Sprintf("%d fix %s left the same failure (signature %s) in place; a person needs to look",
		state.Attempts, plural(state.Attempts, "attempt", "attempts"), state.Signature.Short())
}

// MaxReviewRounds is the review rounds allowed per PR: pipeline.yaml
// `review.max_rounds`, or its default when cfg was never resolved.
func MaxReviewRounds(cfg config.ResolvedReview) int {
	if cfg.MaxRounds < 1 {
		return config.DefaultReviewMaxRounds
	}
	return cfg.MaxRounds
}

// ReviewRoundsExhausted reports whether a PR that has had roundsRun review
// rounds may have no other: the review → fix → re-review loop then stops,
// and the PR needs a person.
func ReviewRoundsExhausted(roundsRun int, cfg config.ResolvedReview) bool {
	return roundsRun >= MaxReviewRounds(cfg)
}
