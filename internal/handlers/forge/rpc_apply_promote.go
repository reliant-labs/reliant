package forge

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemoncmd"
)

// =============================================================================
// PROMOTE — the first state-changing path on this surface.
//
// Two methods, PlanPromote (read-only) and ApplyPromote (the write), and the
// split is the safety design rather than a naming preference. A single method
// with a `dry_run` bool would make FALSE — the destructive value — the default
// for any caller that omits the field: a client written before the field
// existed, a payload that loses it, a hand-built request. Two method names
// cannot fail that way. The destructive call is reachable only by naming it,
// and which one a user triggered is legible from the method in a log line.
//
// Neither method deploys, and nothing here may imply otherwise. Promote writes
// one pointer into the binding ledger; not one byte reaches a cluster until
// `forge env deploy` runs. forge says so in every plan document
// (ships_nothing, next_step) and both handlers pass those fields through
// verbatim in report_json.
//
// There is deliberately NO Deploy RPC. Deploying mutates a live cluster rather
// than a file in the repo — a different risk class, and a separate decision.
// =============================================================================

// forgePromoteReply is forge.promote_apply's daemon reply: the standard forge
// envelope plus the refusal the concurrency guard may produce.
//
// Refused is a pointer so "absent" is distinguishable from a zero-valued
// refusal. The daemon returns a refusal as a SUCCESSFUL response (it is
// structured data, and daemon errors cross as flat strings that would lose the
// binding it found); converting it into a failed RPC is this layer's job, where
// failing closed is what a browser client needs.
type forgePromoteReply struct {
	forgeReportReply
	Refused *struct {
		Reason                 string `json:"reason"`
		ExpectedCurrentRelease string `json:"expected_current_release"`
		ExpectedUnbound        bool   `json:"expected_unbound"`
		ActualBound            bool   `json:"actual_bound"`
		ActualCurrentRelease   string `json:"actual_current_release"`
		ActualPromotedAt       string `json:"actual_promoted_at"`
		Detail                 string `json:"detail"`
	} `json:"promote_refused"`
}

// ApplyPromote WRITES the binding, guarded by the caller's view of current
// state.
//
// The confirmation token is expected_current_release (or expect_unbound for a
// first promote). The daemon re-plans, compares, and writes only if the ledger
// still holds what the caller said it last saw. A disagreement means someone
// promoted in between, and it matters because a forge EnvBinding keeps no
// history: {release, resolved, promoted_at}, overwritten in place, with nothing
// to undo from except git.
//
// A REFUSAL FAILS THE RPC. The daemon reports it as structured data on a
// successful response, and this is where that becomes a FailedPrecondition
// carrying a ForgePromoteRefusal detail. The reason is fail-closed behaviour: a
// refusal returned as a 200 with a nullable field is safe only if every client
// remembers to check it, and a client that forgets tells a user the promote
// worked. On the error path the default behaviour of every client is to treat it
// as a failure, and the structured facts still survive in the detail.
func (s *Service) ApplyPromote(
	ctx context.Context,
	req *connect.Request[reliantv1.PromoteForgeEnvRequest],
) (*connect.Response[reliantv1.PromoteForgeEnvResponse], error) {
	userID, err := daemoncmd.UserID(ctx)
	if err != nil {
		return nil, err
	}
	env, err := forgeEnvArg(req.Msg.Env)
	if err != nil {
		return nil, err
	}
	release, err := forgeReleaseArg(req.Msg.Release)
	if err != nil {
		return nil, err
	}

	// The confirmation token must be an unambiguous claim about current state,
	// checked BEFORE the project lookup so an unauthorised request costs
	// nothing. An unset expected_current_release must never be readable as "I
	// saw nothing" — that is how an empty request would authorise a blind
	// overwrite of a bound environment.
	expected := strings.TrimSpace(req.Msg.ExpectedCurrentRelease)
	switch {
	case req.Msg.ExpectUnbound && expected != "":
		return nil, svcerr.InvalidArgument(fmt.Sprintf("expect_unbound and expected_current_release %q are contradictory: assert either "+
			"that the environment was unbound or which release it was bound to", expected))
	case !req.Msg.ExpectUnbound && expected == "":
		return nil, svcerr.InvalidArgument(fmt.Sprintf("expected_current_release is required (or set expect_unbound for a first promote): "+
			"promoting overwrites %q's binding irrecoverably, so the request must state the release it "+
			"expects to replace", env))
	}

	target, err := s.forgeTarget(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, err
	}

	payload := struct {
		ProjectPath            string `json:"project_path"`
		Env                    string `json:"env"`
		Release                string `json:"release"`
		ExpectedCurrentRelease string `json:"expected_current_release,omitempty"`
		ExpectUnbound          bool   `json:"expect_unbound,omitempty"`
		OrgID                  string `json:"org_id,omitempty"`
		ProjectID              string `json:"project_id,omitempty"`
	}{
		ProjectPath:            target.Path,
		Env:                    env,
		Release:                release,
		ExpectedCurrentRelease: expected,
		ExpectUnbound:          req.Msg.ExpectUnbound,
		OrgID:                  req.Msg.GetOrgId(),
		ProjectID:              target.Project.ID,
	}

	// The apply reply has a field the shared forgeDispatch envelope does not
	// carry, so it is dispatched directly. Error mapping is reproduced rather
	// than shared: readsCluster is false here, which means the reachability
	// escape hatch does not apply at all, and this path must never reinterpret
	// a timeout as "unknown" — a promote that timed out may or may not have
	// written, and calling that a reachability verdict would hide it.
	var reply forgePromoteReply
	if err := s.daemon(target).Dispatch(ctx, userID, "forge.promote_apply", payload, &reply, forgePromoteApplyTimeoutMs); err != nil {
		return nil, s.dispatchError(ctx, target, "forge.promote_apply", err)
	}

	// THE GUARD REFUSED: nothing was written. Fail closed.
	if reply.Refused != nil {
		detail := &reliantv1.ForgePromoteRefusal{
			Reason:                 forgePromoteRefusalReason(reply.Refused.Reason),
			ExpectedCurrentRelease: reply.Refused.ExpectedCurrentRelease,
			ExpectedUnbound:        reply.Refused.ExpectedUnbound,
			ActualBound:            reply.Refused.ActualBound,
			ActualCurrentRelease:   reply.Refused.ActualCurrentRelease,
			ActualPromotedAt:       reply.Refused.ActualPromotedAt,
			Detail:                 reply.Refused.Detail,
		}

		// The structured detail is what makes this recoverable without a blind
		// retry. If it cannot be attached the error still stands — failing
		// closed is the property that must not depend on the detail encoding
		// (svcerr.WithDetail attaches it best-effort).
		return nil, svcerr.WithDetail(svcerr.FailedPrecondition(fmt.Sprintf("promote refused, nothing was written: %s", reply.Refused.Detail)), detail)
	}

	// Applied. report_json is the plan forge ACTUALLY applied, not the preview,
	// and it still carries ships_nothing/next_step — this promote moved a
	// pointer and deployed nothing.
	return connect.NewResponse(&reliantv1.PromoteForgeEnvResponse{
		Meta:       forgeMeta(reply.forgeReportReply, false, ""),
		ReportJson: reply.reportJSON(),
	}), nil
}

// forgePromoteRefusalReason maps the daemon's stable token onto the enum.
//
// An unrecognised token yields UNSPECIFIED rather than a guess, and the RPC
// still fails — a refusal this layer cannot classify is a refusal, not a
// success. That ordering is what keeps a newer daemon's additional reason from
// being laundered into an applied promote.
func forgePromoteRefusalReason(token string) reliantv1.ForgePromoteRefusalReason {
	switch token {
	case "stale_current_release":
		return reliantv1.ForgePromoteRefusalReason_FORGE_PROMOTE_REFUSAL_REASON_STALE_CURRENT_RELEASE
	default:
		return reliantv1.ForgePromoteRefusalReason_FORGE_PROMOTE_REFUSAL_REASON_UNSPECIFIED
	}
}
