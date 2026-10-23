package cli

// Anti-stomp: every promote is a compare-and-set against the promotion the
// plan READ (control-plane docs/design/hosted-deploy-primitives.md §3.1).
//
// THE STOMP THIS CLOSES. A row lock orders two writers; it does not stop a
// writer acting on a stale read. CI job A plans "prod is on v5, moving to
// v6", waits for an approval, a human hotfixes prod to v5.1 in the meantime,
// and job A then writes v6 straight over the hotfix. With a compare-and-set
// the write asserts "prod is still on the promotion I planned from", and the
// hotfix turns job A red (exit 3) instead of being overwritten.
//
// A PROMOTION ID, NOT A VERSION. v1 → v2 → v1 makes "current is v1"
// ambiguous; an id names exactly one entry.
//
// THE NO-OP COMES FIRST. A retry of a promote that already landed carries the
// PREVIOUS id as its expectation — it was planned before its own write. If
// the CAS ran first, every retried success would read as a conflict. So the
// order is the server's (C3): reaching the requested end state is a no-op
// whatever the expectation says, and only a real move is compared.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/release"
)

// expectUnboundLiteral is the --expect-current value that means
// --expect-unbound. It lets a CI variable captured as "no promotion yet"
// round-trip through ONE flag. Promotion ids are UUIDs (hosted) or
// timestamp-hex (file), so the word cannot collide with one.
const expectUnboundLiteral = "unbound"

// appendGuard is the compare-and-set a promote write asserts, plus the one
// override the in-flight refusal admits.
//
// The zero value asserts nothing — the shape every non-promote caller (a
// fixture seeding a ledger, a conversion) passes. a release deploy never
// sends the zero value: it always expects SOMETHING, because the plan always
// read something.
type appendGuard struct {
	// ExpectedCurrentID: the env's current promotion must be this one.
	ExpectedCurrentID string
	// ExpectUnbound: the env must have no promotion at all. Exclusive with
	// ExpectedCurrentID (the wire is a oneof).
	ExpectUnbound bool
	// SupersedeInFlight: promote even though the current promotion has not
	// finished rolling out. Recorded by the server on the new row; a file
	// ledger has no rollout to supersede and ignores it.
	SupersedeInFlight bool
	// ResolveVersionFromSource: send NO version, and let the control plane
	// resolve the release from the promotion's FromPromotionID under the
	// target environment's lock (§3.4).
	//
	// WHY THIS IS ON THE GUARD and not read off the promotion. The
	// promotion's Release carries the plan's PREVIEW of that release, and
	// release.Promotion.Validate requires it to be non-empty — a
	// promotion with no release is not a valid ledger entry, and blanking
	// it to signal "ask the server" would make the domain type lie about
	// what it records. So the preview stays on the promotion, where the
	// plan and the recorded entry both want it, and the instruction
	// "do not send it" travels here, beside SupersedeInFlight: this
	// struct is already the bag of directives for HOW the write is
	// performed, not only what it compares against.
	ResolveVersionFromSource bool
}

// expects reports whether the guard asserts anything about the current
// promotion.
func (g appendGuard) expects() bool { return g.ExpectUnbound || g.ExpectedCurrentID != "" }

// describe renders the expectation for a human: "promotion X" or "no
// promotion".
func (g appendGuard) describe() string {
	if g.ExpectUnbound {
		return "no promotion (unbound)"
	}
	return "promotion " + g.ExpectedCurrentID
}

// guardFor is the expectation a promote sends: the --expect-current
// override when one was given, otherwise exactly what the plan read.
//
// THERE IS NO "NO EXPECTATION" OUTCOME. The plan always reads the current
// promotion (or its absence), so every promote this CLI writes moves from what
// the plan showed. That is the whole point: anti-stomp needs no flag.
func guardFor(plan promotePlan, expectCurrent string, supersede bool) appendGuard {
	g := appendGuard{SupersedeInFlight: supersede}
	switch {
	case expectCurrent == expectUnboundLiteral:
		g.ExpectUnbound = true
	case expectCurrent != "":
		g.ExpectedCurrentID = expectCurrent
	case plan.Current.Bound:
		g.ExpectedCurrentID = plan.Current.PromotionID
	default:
		g.ExpectUnbound = true
	}
	return g
}

// admitPromotion is the write rule a backend WITHOUT a server applies: the
// idempotent no-op first (release.Decide), then the compare-and-set against
// the history it just read. It returns the existing entry for a no-op, nil
// for a move the guard admits, or a *promoteRefusedError.
//
// The hosted backend does not call this — its server applies the same rule
// inside the transaction holding the env row, and a client-side pre-check
// there would be the read-then-write race the server's lock exists to close.
func admitPromotion(history []release.Promotion, p release.Promotion, guard appendGuard) (*release.Promotion, error) {
	existing, err := release.Decide(history, p)
	if err != nil || existing != nil {
		return existing, err
	}
	if !guard.expects() {
		return nil, nil
	}
	var current *release.Promotion
	if n := len(history); n > 0 {
		current = &history[n-1]
	}
	switch {
	case guard.ExpectUnbound && current == nil:
		return nil, nil
	case !guard.ExpectUnbound && current != nil && current.ID == guard.ExpectedCurrentID:
		return nil, nil
	}
	refusal := &promoteRefusedError{
		Reason:                     reasonPromotionConflict,
		ExpectedCurrentPromotionID: guard.ExpectedCurrentID,
		ExpectedUnbound:            guard.ExpectUnbound,
	}
	if current != nil {
		cur := *current
		refusal.ActualCurrent = &cur
		refusal.Detail = fmt.Sprintf("%s is on %s (promotion %s), not on the %s this promote expected",
			p.Env, cur.Release, cur.ID, guard.describe())
	} else {
		refusal.Detail = fmt.Sprintf("%s has no promotion, not the %s this promote expected", p.Env, guard.describe())
	}
	return nil, refusal
}

// promoteRefusedError is a promote the ledger DECLINED — not a failure to
// reach it. The two are different facts to a pipeline: a refusal means the
// ledger was read and the write was judged wrong, so retrying the same
// request changes nothing (exit 3) or only time does (exit 4).
//
// One type for both backends, so the renderer, the exit code and the --json
// refusal object have one source whichever ledger refused.
type promoteRefusedError struct {
	// Reason is one of the reason* constants; it alone picks the exit code.
	Reason string
	// The expectation the write carried, echoed so a log line is
	// self-contained.
	ExpectedCurrentPromotionID string
	ExpectedUnbound            bool
	// ActualCurrent is what IS there — the promotion that landed instead.
	// Nil when the env is unbound, or the server sent no detail.
	ActualCurrent *release.Promotion
	// ActualPhase is the rollout phase that made the env in flight, as the
	// wire enum names it. Set for rollout_in_flight.
	ActualPhase string
	// Detail is one human sentence, the server's when it sent one.
	Detail string
	// CurrentPlan is the plan the server recomputed under the env row lock
	// and judged against, sent with plan_stale and plan_unacknowledged
	// (O-13). Nil for every other reason, and nil when the plan the server
	// sent could not be read.
	//
	// It is what lets a refusal print "this is what the deploy looks like
	// NOW" rather than only "your approval is out of date". The operator's
	// next decision is made against this plan, so carrying it here — rather
	// than re-planning after the refusal — is what keeps the thing they
	// look at and the thing that refused them the same plan.
	CurrentPlan *release.Plan
	// Unacknowledged are the stop-class finding codes the recomputed plan
	// holds that the request did not name. Set for plan_unacknowledged, and
	// derived from CurrentPlan so the list and the plan cannot disagree.
	Unacknowledged []string
	// cause is the wire error, for errors.As/Unwrap — never for display
	// beyond what Detail already says.
	cause error
}

func (e *promoteRefusedError) Error() string {
	msg := "promote refused (" + e.Reason + ")"
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	if hint := refusalHint(e.Reason); hint != "" {
		msg += "\n  " + hint
	}
	// F-19: a stale plan carries the FRESHLY RECOMPUTED one, and showing it
	// here is the difference between an operator knowing what changed and
	// running a second command to find out. The refusal is the moment the
	// question is asked, so it is the moment to answer it.
	//
	// Only the sections and the new digest, not the whole document: the
	// operator's next act is to compare this against what they approved,
	// and a full re-render would bury that under the parts that did not
	// change.
	if e.CurrentPlan != nil {
		msg += "\n" + summarizeRecomputedPlan(*e.CurrentPlan)
	}
	return msg
}

// summarizeRecomputedPlan renders the plan a refusal carried: its digest, so
// an approval can name it, and its findings by class.
func summarizeRecomputedPlan(p release.Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  the plan as it is NOW (digest %s):\n", p.Digest)
	if len(p.Findings) == 0 {
		fmt.Fprintf(&b, "    no changes found\n")
	}
	for _, f := range p.Findings {
		fmt.Fprintf(&b, "    [%s] %s %s", f.Class, f.Code, f.Subject)
		if f.Detail != "" {
			fmt.Fprintf(&b, " — %s", f.Detail)
		}
		fmt.Fprintf(&b, "\n")
	}
	if stops := p.StopCodes(); len(stops) > 0 {
		fmt.Fprintf(&b, "    DESTRUCTIVE: %s\n", strings.Join(stops, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// ExitCode is §3.A's table, through the one mapping every verb reads.
func (e *promoteRefusedError) ExitCode() int { return exitCodeForRefusal(e.Reason) }

func (e *promoteRefusedError) Unwrap() error { return e.cause }

// refusalHint is the next step a human or pipeline takes for each reason.
// The reason, not the message, chooses it.
func refusalHint(reason string) string {
	switch reason {
	case reasonPromotionConflict:
		return "someone else moved this environment since the plan was read. Re-plan (`--plan`) and decide; retrying would overwrite what landed."
	case reasonSourceMoved:
		return "the source environment moved after its promotion was captured. Re-capture the source promotion and re-run."
	case reasonRolloutInFlight:
		return "the current promotion is still rolling out. Wait for it (`forge env status --wait`), or pass --supersede to replace it on purpose (recorded)."
	case reasonEnvironmentPinned:
		return "the environment's reconcile policy is pinned, which refuses every change. Unpin it on the control plane first."
	case reasonPlanStale:
		return "the environment changed after the plan was computed, so the approval no longer describes this deploy. Re-plan and approve the new plan; the refusal above shows what is there now."
	case reasonPlanUnacknowledged:
		return "this deploy destroys something that cannot be recreated by re-running it. Acknowledge each finding by code (--acknowledge-destructive <code>); --yes does not cover a stop-class finding."
	case reasonBundleMissing:
		return "the bundle's blob is no longer in the registry, so there are no bytes to apply. Re-build and re-record the bundle (`forge env build <env>`)."
	default:
		return ""
	}
}

// There is no apply-refusal decoder, because there is no apply request to be
// refused. It existed so a refused BeginApply produced the same domain
// refusal a refused Promote does — the same CAS, the same plan-digest
// recompute, the same stop-class check, under the same row lock. The PROMOTE
// is now the only write that carries any of that, so hostedStore's own
// refusalFromWire is the single decoder, and the exit-code mapping and the
// --json refusal object have one source by construction rather than by two
// implementations agreeing.

// adoptCurrentPlan decodes the recomputed plan a refusal carried, and the
// stop codes it holds.
//
// A plan that will not decode — an unknown finding class, a digest that does
// not recompute — is DROPPED and the refusal still stands. The reason already
// decided the exit code; the plan only enriches the message, and turning a
// clear refusal into a decode error would hide why the write was declined
// behind a complaint about the explanation. The dropped plan is not silent
// either: the operator still sees the reason and the hint, which name
// re-planning as the next step.
func (e *promoteRefusedError) adoptCurrentPlan(w *wirePlan) {
	if w == nil {
		return
	}
	p, err := planFromWire(*w)
	if err != nil {
		return
	}
	e.CurrentPlan = &p
	if e.Reason == reasonPlanUnacknowledged {
		// Derived from the plan rather than parsed out of the message, so
		// the codes printed and the plan shown cannot disagree. Nothing
		// was acknowledged from this error's point of view: it is the
		// server's verdict on the request that was sent.
		e.Unacknowledged = p.StopCodes()
	}
}

// refusalFromWire turns a refused Promote call into the domain refusal, or
// returns nil when err is not a refusal at all.
//
// A refusal is recognised by its REASON (the x-forge-error-reason header, or
// the detail's own reason) — never by message text. A refusal carrying no
// decodable detail is still a refusal: the reason alone picks the exit code,
// and the detail only enriches the message.
func (s *hostedStore) refusalFromWire(env string, err error) *promoteRefusedError {
	out := &promoteRefusedError{Reason: hostedErrorReason(err), cause: err}
	if w, ok := promoteRefusalOf(err); ok {
		out.Reason = w.Reason
		out.ExpectedCurrentPromotionID = w.ExpectedCurrentPromotionID
		out.ExpectedUnbound = w.ExpectedUnbound
		out.ActualPhase = w.ActualPhase
		out.Detail = w.Detail
		out.adoptCurrentPlan(w.CurrentPlan)
		if w.ActualCurrent != nil {
			// The actual promotion is DISPLAY data on a failure path. One
			// the server sent that forge cannot validate must not turn a
			// clear refusal into a decode error, so a bad one is dropped
			// and the refusal still stands.
			if p, perr := s.promotionFromWire(env, *w.ActualCurrent); perr == nil {
				out.ActualCurrent = &p
			}
		}
	}
	if out.Reason == "" {
		return nil
	}
	if out.Detail == "" {
		// The server's own message is the next-best sentence. Display
		// only — the reason above already decided everything.
		var cerr *cloud.Error
		if errors.As(err, &cerr) {
			out.Detail = strings.TrimSpace(cerr.Message)
		}
	}
	return out
}

// guardWireFields renders the guard as PromoteReleaseRequest's tags 7–9.
//
// The oneof is honoured by construction: exactly one of the two expectation
// members is ever set. Neither set is the zero guard, which no promote sends.
func guardWireFields(guard appendGuard, req map[string]any) {
	switch {
	case guard.ExpectUnbound:
		req["expectUnbound"] = true
	case guard.ExpectedCurrentID != "":
		req["expectedCurrentPromotionId"] = guard.ExpectedCurrentID
	}
	if guard.SupersedeInFlight {
		req["supersedeInFlight"] = true
	}
}

// promoteRefusalJSON is the --json "refusal" object: what was expected, what
// IS there, and why the write was declined.
type promoteRefusalJSON struct {
	Reason                     string `json:"reason"`
	ExpectedCurrentPromotionID string `json:"expected_current_promotion_id,omitempty"`
	ExpectedUnbound            bool   `json:"expected_unbound,omitempty"`
	// ActualCurrent is the promotion that landed instead; nil when the env
	// is unbound or the control plane did not say.
	ActualCurrent *release.Promotion `json:"actual_current,omitempty"`
	// ActualPhase is the rollout phase, lowercased ("progressing"), for
	// rollout_in_flight.
	ActualPhase string `json:"actual_phase,omitempty"`
	Detail      string `json:"detail,omitempty"`
	// CurrentPlan is the plan the server recomputed and refused against
	// (plan_stale, plan_unacknowledged). Emitted so a pipeline that reads
	// --json gets the same evidence a human reading the message does.
	CurrentPlan *release.Plan `json:"current_plan,omitempty"`
	// Unacknowledged are the stop-class codes still needing acknowledgement.
	Unacknowledged []string `json:"unacknowledged,omitempty"`
}

func (e *promoteRefusedError) toJSON() *promoteRefusalJSON {
	out := &promoteRefusalJSON{
		Reason:                     e.Reason,
		ExpectedCurrentPromotionID: e.ExpectedCurrentPromotionID,
		ExpectedUnbound:            e.ExpectedUnbound,
		ActualCurrent:              e.ActualCurrent,
		Detail:                     e.Detail,
		CurrentPlan:                e.CurrentPlan,
		Unacknowledged:             e.Unacknowledged,
	}
	if e.ActualPhase != "" {
		out.ActualPhase = rolloutPhaseName(e.ActualPhase)
	}
	return out
}

// applyRefusal is what a refused write leaves on the plan: applied stays
// false, and the refusal says why. Returns whether err was a refusal.
func (plan *promotePlan) applyRefusal(err error) bool {
	var refused *promoteRefusedError
	if !errors.As(err, &refused) {
		return false
	}
	plan.Applied = false
	plan.Refusal = refused.toJSON()
	return true
}
