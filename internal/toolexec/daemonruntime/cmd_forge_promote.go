// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func init() {
	RegisterCommand("forge.promote_plan", handleForgePromotePlan)
	RegisterCommand("forge.promote_apply", handleForgePromoteApply)
}

// =============================================================================
// forge.promote_* — THE FIRST STATE-CHANGING PATH IN THE forge.* FAMILY.
//
// Everything else in cmd_forge.go reads. These two are the promote path, and
// they are TWO COMMANDS rather than one command with a dry_run flag for a
// reason that is not stylistic:
//
//	a boolean defaults to false, and false is the destructive value.
//
// A caller that forgets the field, a proto that gains it after a client was
// written, a JSON payload that drops it in transit — every one of those turns
// the SAFE call into the WRITE. Two distinct command names have no such
// failure mode: the destructive one cannot be reached by omission, only by
// naming it. It is also the difference between two log lines rather than one
// log line plus a field you have to go and read.
//
// What promote actually does bounds the risk on both sides:
//
//   - It writes ONE pointer — env -> release, with the release's per-image
//     digests snapshotted — into the binding ledger (.forge/env-releases.json).
//   - It SHIPS NOTHING. Not one byte reaches any cluster until
//     `forge env deploy <env>` runs. forge states this in every plan document
//     (ships_nothing, next_step) and both handlers carry those fields through
//     verbatim, because a UI that implies a deploy happened here is the most
//     likely way this path misleads someone.
//
// There is deliberately NO forge.deploy command here. Deploy mutates a live
// cluster rather than a file in the repo — a different risk class, and a
// separate decision from this one.
// =============================================================================

// -----------------------------------------------------------------------------
// WHY THE CONCURRENCY GUARD LIVES HERE, IN THE SAME COMMAND AS THE WRITE.
//
// An EnvBinding keeps NO HISTORY: it is {Release, Resolved, PromotedAt}, so a
// promote OVERWRITES the previous release outright. There is no prior-release
// field to read back and nothing to undo from — recovery means finding the old
// value in git, if the ledger was even committed. An overwrite the operator
// did not intend is therefore close to unrecoverable, which is what justifies
// an optimistic-concurrency check on a single-file write.
//
// The guard is: re-plan (read-only), compare the binding forge actually holds
// against the binding the caller says it last saw, and write only if they
// agree. The check is INSIDE this command, not split across the RPC hop, for
// two reasons:
//
//  1. A guard on the far side of a network hop leaves a window between the
//     check and the write, which is precisely the race the guard exists to
//     close.
//  2. Splitting it would leave an UNGUARDED write primitive registered on the
//     daemon. Any caller that skipped the check would still get the overwrite.
//     Here the guard and the write are the same command; there is no argv that
//     performs one without the other.
//
// Cost of the guard: apply runs forge twice — once for the guard plan, once
// for the real promote (which computes its own plan internally, from the same
// function). Two local ledger reads and a git range walk. That is a cheap
// price for making an unrecoverable overwrite require agreement about state.
//
// A useful side effect: the version-capability check lands on the READ-ONLY
// call. A forge too old to know `--plan` fails the guard plan and returns
// supported:false having written nothing.
// -----------------------------------------------------------------------------

// forgePromoteRefusalReasonStaleBinding is the stable machine token for the
// one refusal this path can produce. A token rather than prose because the RPC
// layer branches on it; the human sentence is Detail.
const forgePromoteRefusalReasonStaleBinding = "stale_current_release"

// forgePromoteRefusal is why a promote was NOT applied, plus the binding that
// was actually found.
//
// The found binding is carried because "someone else promoted in between" is
// only actionable if the caller can see what they promoted TO. Without it the
// only recovery is a blind retry, which is how a rollback gets applied twice.
type forgePromoteRefusal struct {
	// Reason is the stable token. Currently always
	// forgePromoteRefusalReasonStaleBinding.
	Reason string `json:"reason"`

	// ExpectedCurrentRelease is what the caller said the env was bound to.
	// Empty when the caller expected an unbound env.
	ExpectedCurrentRelease string `json:"expected_current_release,omitempty"`

	// ExpectedUnbound is true when the caller expected a first promote.
	ExpectedUnbound bool `json:"expected_unbound,omitempty"`

	// ActualBound and ActualCurrentRelease are what the ledger holds NOW.
	// Bound is separate from a non-empty release so "never promoted" is
	// distinguishable from a binding carrying a blank version — the same
	// distinction forge's own plan document makes.
	ActualBound          bool   `json:"actual_bound"`
	ActualCurrentRelease string `json:"actual_current_release,omitempty"`

	// ActualPromotedAt is RFC3339 for when the binding that was found was
	// WRITTEN. It is the timestamp that tells an operator whether they were
	// beaten by seconds or are looking at a week-old page.
	ActualPromotedAt string `json:"actual_promoted_at,omitempty"`

	// Detail is the one-sentence human phrasing.
	Detail string `json:"detail"`
}

// forgePromoteApplyResponse is forge.promote_apply's reply.
//
// It is forgeReportResponse plus one optional field. A refusal is a SUCCESSFUL
// daemon response carrying PromoteRefused, not a transport error, because the
// refusal is structured data (the binding found, the fresh plan) and daemon
// errors cross as flat strings that would lose all of it. Turning it into a
// failed RPC is the job of the layer that faces the browser, where an
// unchecked error fails closed.
//
// Report on a refusal is the FRESH plan — the read-only one the guard just
// computed against current state — so a caller can re-render the real diff
// without a second round trip. On success it is the plan that was APPLIED.
type forgePromoteApplyResponse struct {
	forgeResponseMeta
	Report json.RawMessage `json:"report,omitempty"`

	// PromoteRefused is set exactly when nothing was written because the
	// caller's view of the current binding was stale. Absent on success.
	PromoteRefused *forgePromoteRefusal `json:"promote_refused,omitempty"`
}

// forgePromotePlanFacts is the MINIMUM forge needs to be asked to decide
// whether to write.
//
// Reading forge's document at all is a deliberate, narrow exception to this
// package's transport rule, so the boundary of the exception matters: these
// are IDENTITY fields — which env, which release, is it bound, was it applied.
// Not one verdict field is read. Nothing here re-derives what counts as drift,
// whether a rollback is acceptable, or whether a plan is safe; those stay
// forge's, and they reach the caller untouched in the raw report. The guard
// only ever answers "is the state the caller described the state that exists".
type forgePromotePlanFacts struct {
	Env     string `json:"env"`
	DryRun  bool   `json:"dry_run"`
	Applied bool   `json:"applied"`
	Current struct {
		Bound      bool   `json:"bound"`
		Release    string `json:"release"`
		PromotedAt string `json:"promoted_at"`
	} `json:"current"`
	Target struct {
		Release string `json:"release"`
	} `json:"target"`
}

// --- shared request validation -----------------------------------------------

// forgePromoteArgs are the fields both commands need.
type forgePromoteArgs struct {
	ProjectPath string `json:"project_path"`
	// Env is the environment to bind. Passed as `--to <env>`.
	Env string `json:"env"`
	// Release is the version to bind it to, forge's positional argument.
	Release string `json:"release"`
}

// validate rejects a request before any forge process starts.
//
// The leading-dash check is not decoration. Both values land in argv, so a
// release of "--to" or an env of "--plan" would be consumed by forge as a FLAG
// and change which operation ran — on the apply path that means an argument
// deciding whether a write happens. Rejecting the shape is the only place that
// can be prevented cheaply and completely.
func (a forgePromoteArgs) validate() error {
	if strings.TrimSpace(a.ProjectPath) == "" {
		return fmt.Errorf("project_path is required")
	}
	if strings.TrimSpace(a.Env) == "" {
		return fmt.Errorf("env is required")
	}
	if strings.TrimSpace(a.Release) == "" {
		return fmt.Errorf("release is required")
	}
	for name, value := range map[string]string{"env": a.Env, "release": a.Release} {
		if strings.HasPrefix(strings.TrimSpace(value), "-") {
			return fmt.Errorf("%s must not begin with '-' (it would be read as a flag): %q", name, value)
		}
	}
	return nil
}

// planArgs is the READ-ONLY invocation. --plan is a true dry run in forge: the
// single write (applyPromotePlan) is guarded downstream of the plan, so the
// same function produces this document and the applied one.
func (a forgePromoteArgs) planArgs() []string {
	return []string{"env", "promote", strings.TrimSpace(a.Release), "--to", strings.TrimSpace(a.Env), "--plan", "--json"}
}

// applyArgs is the same command WITHOUT --plan. The only difference between
// the two argv is the flag that suppresses the write.
func (a forgePromoteArgs) applyArgs() []string {
	return []string{"env", "promote", strings.TrimSpace(a.Release), "--to", strings.TrimSpace(a.Env), "--json"}
}

// --- forge.promote_plan ------------------------------------------------------

type forgePromotePlanRequest struct {
	forgePromoteArgs
}

// handleForgePromotePlan previews a promote and writes NOTHING.
//
// Safe and idempotent: `--plan` computes the entire change set and stops before
// forge's only write. This is the command a UI calls to render the diff, and it
// needs no confirmation token precisely because there is nothing to confirm.
func handleForgePromotePlan(ctx context.Context, payload []byte) ([]byte, error) {
	var req forgePromotePlanRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if err := req.validate(); err != nil {
		return nil, err
	}

	return invokeForgeReport(ctx, forgeInvocation{
		ProjectPath: req.ProjectPath,
		Args:        req.planArgs(),
	})
}

// --- forge.promote_apply -----------------------------------------------------

type forgePromoteApplyRequest struct {
	forgePromoteArgs

	// ExpectedCurrentRelease is the release the caller last saw this env
	// bound to. The write proceeds only if the ledger still holds it.
	ExpectedCurrentRelease string `json:"expected_current_release,omitempty"`

	// ExpectUnbound asserts the caller saw NO binding at all — a first
	// promote. An explicit field rather than an empty
	// ExpectedCurrentRelease, because those are three different claims and
	// only two of them are legitimate: "I saw v1.3.0", "I saw nothing", and
	// "I did not populate this field". Collapsing the last two would let an
	// unset payload authorise a blind overwrite of an env that IS bound,
	// which is the exact accident the guard exists to stop.
	ExpectUnbound bool `json:"expect_unbound,omitempty"`
}

// validateConfirmation checks the caller stated a position on current state.
func (r forgePromoteApplyRequest) validateConfirmation() error {
	stated := strings.TrimSpace(r.ExpectedCurrentRelease)
	switch {
	case r.ExpectUnbound && stated != "":
		return fmt.Errorf("expect_unbound and expected_current_release %q are contradictory: "+
			"assert either that the env was unbound or which release it was bound to", stated)
	case !r.ExpectUnbound && stated == "":
		return fmt.Errorf("expected_current_release is required (or set expect_unbound for a first promote): " +
			"a promote overwrites the binding irrecoverably, so it must state the release it expects to replace")
	}
	return nil
}

// handleForgePromoteApply WRITES the binding, but only if the caller's view of
// current state still holds.
//
// Sequence, and the ordering is the safety property:
//
//  1. validate the request and the confirmation — no forge process for a
//     request that cannot be authorised;
//  2. run the READ-ONLY plan and read the binding forge actually holds;
//  3. compare against the caller's claim; on disagreement return a refusal
//     carrying the found binding and that fresh plan, having written nothing;
//  4. only then run the real promote, and return the document it produced —
//     not the preview from step 2. The caller shows what HAPPENED rather than
//     assuming its preview was still accurate.
func handleForgePromoteApply(ctx context.Context, payload []byte) ([]byte, error) {
	var req forgePromoteApplyRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if err := req.validate(); err != nil {
		return nil, err
	}
	if err := req.validateConfirmation(); err != nil {
		return nil, err
	}

	// STEP 2 — the guard plan. Read-only, and it doubles as the gate for
	// every non-writable state: a missing project dir, a non-forge project
	// and a too-old forge all resolve here, before any write is attempted.
	planRaw, err := invokeForgeReport(ctx, forgeInvocation{
		ProjectPath: req.ProjectPath,
		Args:        req.planArgs(),
	})
	if err != nil {
		return nil, err
	}

	// invokeForgeReport hands back marshalled bytes, so the parsed form is
	// recovered here rather than by reaching into it. cmd_forge.go is not
	// modified to expose an intermediate: one extra decode of a document
	// this process just produced is not worth restructuring the five
	// read-only commands' shared path for.
	var plan forgeReportResponse
	if err := json.Unmarshal(planRaw, &plan); err != nil {
		return nil, fmt.Errorf("%s: could not read promote plan: %v", forgeCommandFailedPrefix, err)
	}

	// Not a forge project, or a forge too old for --plan. Both are normal,
	// structured answers, and in both cases NOTHING WAS WRITTEN. Returning
	// the guard plan's own envelope preserves that: it carries
	// supported/is_forge_project/forge_version exactly as the read-only
	// commands do, and no applied plan, because there is none.
	if !plan.IsForgeProject || !plan.Supported {
		return json.Marshal(forgePromoteApplyResponse{
			forgeResponseMeta: plan.forgeResponseMeta,
			Report:            plan.Report,
		})
	}

	var facts forgePromotePlanFacts
	if err := json.Unmarshal(plan.Report, &facts); err != nil {
		return nil, fmt.Errorf("%s: promote plan was not the expected shape: %v", forgeCommandFailedPrefix, err)
	}

	// A plan that claims it was applied means --plan did not suppress the
	// write, which would mean the guard read state that had ALREADY been
	// mutated. Refuse to continue rather than write again on top of it.
	if facts.Applied || !facts.DryRun {
		return nil, fmt.Errorf("%s: promote plan reported applied=%v dry_run=%v — "+
			"the read-only preview appears to have written; refusing to promote",
			forgeCommandFailedPrefix, facts.Applied, facts.DryRun)
	}

	// Paranoia, cheap: the plan must be about the env that was asked for.
	if got, want := strings.TrimSpace(facts.Env), strings.TrimSpace(req.Env); got != "" && got != want {
		return nil, fmt.Errorf("%s: promote plan is for env %q but %q was requested; refusing to promote",
			forgeCommandFailedPrefix, got, want)
	}

	// STEP 3 — the guard itself.
	if refusal := forgePromoteStaleBinding(req, facts); refusal != nil {
		return json.Marshal(forgePromoteApplyResponse{
			forgeResponseMeta: plan.forgeResponseMeta,
			// The FRESH plan, so the caller can re-render the real diff
			// against the binding that actually exists.
			Report:         plan.Report,
			PromoteRefused: refusal,
		})
	}

	// STEP 4 — the write. Same command, minus --plan.
	appliedRaw, err := invokeForgeReport(ctx, forgeInvocation{
		ProjectPath: req.ProjectPath,
		Args:        req.applyArgs(),
	})
	if err != nil {
		return nil, err
	}
	var applied forgeReportResponse
	if err := json.Unmarshal(appliedRaw, &applied); err != nil {
		return nil, fmt.Errorf("%s: could not read applied promote: %v", forgeCommandFailedPrefix, err)
	}

	return json.Marshal(forgePromoteApplyResponse{
		forgeResponseMeta: applied.forgeResponseMeta,
		Report:            applied.Report,
	})
}

// forgePromoteStaleBinding compares the caller's claim about current state
// against what the ledger holds, and returns the refusal when they disagree.
//
// Nil means the claim held and the write may proceed.
func forgePromoteStaleBinding(req forgePromoteApplyRequest, facts forgePromotePlanFacts) *forgePromoteRefusal {
	expected := strings.TrimSpace(req.ExpectedCurrentRelease)
	actual := strings.TrimSpace(facts.Current.Release)

	var detail string
	switch {
	case req.ExpectUnbound && facts.Current.Bound:
		detail = fmt.Sprintf("env %q was expected to be unbound but is bound to %s (promoted at %s); "+
			"someone promoted it since this plan was read",
			req.Env, actual, facts.Current.PromotedAt)
	case !req.ExpectUnbound && !facts.Current.Bound:
		detail = fmt.Sprintf("env %q was expected to be bound to %s but has no binding at all",
			req.Env, expected)
	case !req.ExpectUnbound && actual != expected:
		detail = fmt.Sprintf("env %q is bound to %s, not the expected %s (promoted at %s); "+
			"someone promoted it since this plan was read",
			req.Env, actual, expected, facts.Current.PromotedAt)
	default:
		return nil
	}

	return &forgePromoteRefusal{
		Reason:                 forgePromoteRefusalReasonStaleBinding,
		ExpectedCurrentRelease: expected,
		ExpectedUnbound:        req.ExpectUnbound,
		ActualBound:            facts.Current.Bound,
		ActualCurrentRelease:   actual,
		ActualPromotedAt:       strings.TrimSpace(facts.Current.PromotedAt),
		Detail:                 detail,
	}
}
