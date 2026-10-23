package cli

// The exit-code table every hosted deploy verb shares.
//
// It extends the convention `forge release verify` and `forge env status`
// already follow, where the distinction that matters is not pass/fail but
// WHETHER WE LOOKED: 1 means "looked, and it is wrong", and 2 means "could
// not determine". That difference is what lets CI tell a bad release from a
// broken control plane, and collapsing the two into a generic failure is the
// thing this table exists to prevent.
//
// CI branches on these codes directly, which is why they are named here
// rather than written as literals at each `exitCodeError{code: …}`. A verb
// that invented its own numbering would silently change the meaning of a
// pipeline's `if [ $? -eq 3 ]`, and a reader of that pipeline has no way to
// discover the drift.
//
// 3–8 are the codes the hosted primitives add. Each names ONE outcome a
// pipeline reacts to differently:
//
//   - 3 (conflict) means someone else moved the environment. The pipeline
//     must stop and page a human: retrying would stomp whatever landed.
//   - 4 (refused) means the write was declined, not lost — a rollout is in
//     flight, or the environment is pinned. A pipeline may wait and retry.
//   - 5 (plan unconfirmed) means nobody approved the plan. Nothing was
//     written; the pipeline must add --yes (or approve interactively).
//   - 6 (superseded) means a newer promotion replaced the one being waited
//     on. The wait's subject is gone, so neither retry nor failure is right
//     — the pipeline's release was overtaken.
//   - 7 (queued) means the deploy was ACCEPTED and RECORDED and is waiting on
//     a HUMAN — today, billing for what it runs. Nothing failed and nothing
//     is to be retried: the control plane applies it on its own the moment
//     the hold clears. The pipeline (or agent) hands the printed action URL
//     to a person and stops; `--wait` blocks until it is live instead.
//   - 8 (timed out) means the rollout was still progressing when the budget
//     ran out. Retrying the WAIT is correct; re-promoting is not.
//
// 6, 7 and 8 are deliberately NOT 1. A timeout, an overtaken wait and a queued
// deploy are all "we never saw this finish", and reporting any of them as "the
// release is bad" would fail builds for releases that were fine.
//
// WHY 5 MOVED, AND WHY THAT IS THE RIGHT TRADE. 5 meant "the wait's budget
// expired" until O-13 gave `forge env deploy` a confirmation gate, which
// needed a code of its own for the one outcome a pipeline must never
// misread: nothing was deployed because nobody approved it. The two
// candidates were a NEW code for the gate, or a new code for the timeout.
//
// The gate took 5 because it is the one that must be impossible to confuse
// with success. A pipeline that upgrades without adding --yes now exits 5,
// and the worst available reading of 5 is "a wait timed out" — which also
// means "not deployed", so a stale handler still fails the job rather than
// passing it. Had the gate taken 8 instead, an old handler's `[ $? -eq 5 ]`
// → "retry the wait" branch would retry a deploy that had never been
// approved. Pre-1.0, so the renumber is a clean break rather than an alias:
// `forge env deploy --help`'s table is the contract, and
// internal/cli/exitcodes.go is the only place either number is written.
// Known consumer: reliant's internal/toolexec/daemonruntime/cmd_forge_deploy.go
// carries forge's exit code as DATA and branches on no specific value.
const (
	// exitOK is success, or an idempotent no-op. A re-run of a promote
	// that already landed exits 0: reaching the requested end state is
	// what was asked for.
	exitOK = 0

	// exitWrong: we looked, and it is wrong — degraded, drifted, a check
	// failed, or the input was invalid. Raised by wait, verify and record.
	exitWrong = 1

	// exitUndetermined: we could not look. The control plane was
	// unreachable, auth was refused, or the thing is unobservable. Never
	// folded into success or failure: "we cannot see it" is not
	// permission.
	exitUndetermined = 2

	// exitConflict: the environment's current promotion is not the one the
	// caller expected (a compare-and-set mismatch). Promote only.
	exitConflict = 3

	// exitRefused: the write was declined — a rollout is in flight and
	// --supersede was not given, or the environment is pinned. Promote
	// only.
	exitRefused = 4

	// exitPlanUnconfirmed: the deploy plan was computed and printed, and
	// nobody approved it — no TTY to prompt on and no --yes. NOTHING was
	// promoted. Deploy only (O-13, §13 F-18).
	//
	// It is not exitRefused (4), which means a write was attempted and
	// declined by the server. Here forge declined to attempt one, which a
	// pipeline fixes by adding a flag rather than by waiting and retrying.
	exitPlanUnconfirmed = 5

	// exitSuperseded: the environment was promoted past the promotion
	// being waited on. Wait only.
	exitSuperseded = 6

	// exitQueued: the deploy was accepted and recorded, and the control
	// plane is HOLDING it on a human action (billing). It proceeds on its
	// own when the hold clears — nothing is to be re-run. Deploy, wait and
	// status. Not exitRefused (4): nothing was declined. Not exitTimedOut
	// (8): nothing is moving, and no amount of waiting by a pipeline will
	// move it — a person has to act.
	exitQueued = 7

	// exitTimedOut: the budget expired while the rollout was still
	// progressing. Wait only. (Was 5 before O-13 — see the header.)
	exitTimedOut = 8
)

// The domain reasons a control plane sends on a refused promote, under
// cloud.ReasonHeader. The Connect code for all three is FailedPrecondition —
// the CATEGORY — and the reason is what says which refusal happened.
const (
	// reasonPromotionConflict: the environment's current promotion is not
	// the one the caller's compare-and-set expected.
	reasonPromotionConflict = "promotion_conflict"
	// reasonRolloutInFlight: the current promotion has not finished
	// rolling out, and --supersede was not given.
	reasonRolloutInFlight = "rollout_in_flight"
	// reasonEnvironmentPinned: the environment's reconcile policy is
	// pinned, which refuses every change.
	reasonEnvironmentPinned = "environment_pinned"
	// reasonSourceMoved: `promote --from` named a source promotion that is
	// no longer the source environment's current one. A conflict — someone
	// else moved the source — so it shares exitConflict.
	reasonSourceMoved = "source_moved"

	// reasonPlanStale (O-13, F-19): the server recomputed the deploy plan
	// under the environment's row lock and it no longer matches the digest
	// the approval named. Live moved between plan and approve — someone
	// else promoted, a new bundle was applied, drift appeared.
	//
	// It shares exitConflict with promotion_conflict DELIBERATELY. To the
	// operator, "someone promoted under me" and "the world changed under
	// me" are one situation with one response: re-plan, look at what
	// changed, decide again. A separate code would ask every pipeline to
	// re-derive a distinction it does not act on.
	reasonPlanStale = "plan_stale"
	// reasonPlanUnacknowledged (O-13): the recomputed plan holds a
	// stop-class finding — a stateful deletion, a load balancer losing its
	// address — that the request did not name in acknowledgedFindings.
	//
	// exitRefused, not exitConflict: nothing moved and nothing was lost.
	// The write was declined pending a decision, and re-running it with the
	// finding acknowledged is the legitimate next step. A blanket --yes
	// does not satisfy it, which is the whole point.
	reasonPlanUnacknowledged = "plan_unacknowledged"
	// reasonBundleMissing (F-4): the bundle's blob is no longer in the
	// registry, so an apply would be applying bytes it cannot prove it has.
	//
	// exitWrong: we looked, and the thing named is not there. Not
	// undetermined — the registry answered — and not a retryable refusal,
	// since nothing about waiting makes a deleted blob reappear. The remedy
	// is to re-build and re-record.
	reasonBundleMissing = "bundle_missing"
)

// exitCodeForRefusal maps a refusal REASON to its exit code — the one table
// every verb reads, so a pipeline's `if [ $? -eq 3 ]` means the same thing
// whichever verb produced it.
//
// Conflict and refused are separated because a pipeline reacts to them
// oppositely. A conflict (3) means someone else moved the environment, so
// retrying would stomp whatever landed and the run must stop. A refusal (4)
// means the write was declined but nothing was lost — a rollout is in
// flight, or the env is pinned — so waiting and retrying is legitimate.
// Collapsing both into one code would force every pipeline to re-derive the
// difference from message text, which is the thing §3.A exists to stop.
//
// An unrecognised reason is exitWrong, NOT exitUndetermined: the server
// refused, which means it looked. A reason forge does not know about is a
// forge gap, not an unobservable environment, and reporting it as 2 would
// tell CI to retry a write that will be refused identically.
func exitCodeForRefusal(reason string) int {
	switch reason {
	case reasonPromotionConflict, reasonSourceMoved, reasonPlanStale:
		return exitConflict
	case reasonRolloutInFlight, reasonEnvironmentPinned, reasonPlanUnacknowledged:
		return exitRefused
	case reasonBundleMissing:
		return exitWrong
	default:
		return exitWrong
	}
}
