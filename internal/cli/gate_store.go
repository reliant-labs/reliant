package cli

// The gate evidence store: RecordGate / ListGates against a control plane
// (control-plane docs/design/hosted-deploy-primitives.md §3.3).
//
// WHY THESE WIRE SHAPES LIVE HERE AND NOT IN hosted_ledger.go. That file is
// F0's, and its shapes serve the release ledger (cut, promote, list). These
// two procedures are F4's, and keeping them in their own file keeps the two
// tasks' edits disjoint by construction (§4.3). The wireGate TYPE itself is
// reused from there rather than redeclared: a second declaration of the same
// message is exactly how a client ends up sending `startedAt` on one path and
// `started_at` on another.
//
// EVIDENCE IS A DIFFERENT AUTHORITY FROM PROMOTE, DELIBERATELY. Recording
// needs `deploy:write`; promote needs admin. A CI test job should be able to
// report its own result without being able to move production — if the two
// shared a credential, every job that reports a test outcome would hold
// promote authority, and the scope would be meaningless. Nothing in forge
// enforces that split (the server does); what forge must not do is make them
// the same request.
//
// APPEND-ONLY, IDEMPOTENT ON (promotion, name, run id). A re-run appends a
// NEW row and the server returns `created: false` for a repeat of the same
// (promotion, name, run id) — which is why the run id includes GitHub's
// RUN_ATTEMPT (run_identity.go): without it, a re-run's gates would collide
// with the first attempt's and the caller would be handed the OLD rows while
// believing it had recorded the new ones.

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/release"
)

const (
	procRecordGate = "controlplane.v1.DeployService/RecordGate"
	procListGates  = "controlplane.v1.DeployService/ListGates"
)

// wireRecordGateResponse is controlplane.v1.RecordDeployGateResponse.
//
// Created is the IDEMPOTENCY ANSWER, and it is reported to the user rather
// than swallowed: "recorded" and "already recorded" are both exit 0, but they
// are different facts, and a pipeline that silently re-recorded would give no
// hint that its run id is not varying per attempt the way it should.
type wireRecordGateResponse struct {
	Gate    wireGate `json:"gate"`
	Created bool     `json:"created"`
}

type wireListGatesResponse struct {
	Gates []wireGate `json:"gates"`
}

// gateStore records and reads one environment's gate evidence. A concrete
// type with methods, not an interface: there is one implementation and no
// substitution point, so an interface here would be indirection rather than
// abstraction (the repo's package-boundary rule). Tests drive it against an
// httptest control plane, which is the real wire path.
type gateStore struct {
	client   cloudCaller
	endpoint string
}

// recordGate appends one gate to a promotion.
//
// The gate goes through gateToWire, which is the STRICT path: Validate
// refuses a status outside the closed set and a gate carrying a RawStatus
// (one that only survived a read because it was mapped). So a status the
// server would reject as InvalidArgument never reaches it, and the caller
// gets the message naming the legal set instead of a wire error.
//
// RecordedBy and RecordedAt are not sent — gateToWire drops them. Evidence
// whose author the author chose is not attributable.
func (s gateStore) recordGate(ctx context.Context, promotionID string, gate release.Gate) (release.Gate, bool, error) {
	w, err := gateToWire(gate)
	if err != nil {
		return release.Gate{}, false, err
	}
	var resp wireRecordGateResponse
	req := map[string]any{"promotionId": promotionID, "gate": w}
	if err := s.client.Call(ctx, procRecordGate, req, &resp); err != nil {
		return release.Gate{}, false, err
	}
	// Read back through the LENIENT path: the server owns recordedBy and
	// recordedAt, and the row it returns is the authority on what was
	// stored.
	return gateFromWire(resp.Gate), resp.Created, nil
}

// listGates reads every gate attached to a promotion: the promote-time gates
// first, then the recorded ones, oldest first (§3.3's ListGates contract).
//
// That ORDER IS THE SERVER'S and is preserved rather than re-sorted here,
// because it carries meaning time alone does not: the first group is what was
// known before the environment moved, and the second is what was learned
// after. Sorting the union by timestamp would destroy exactly the distinction
// a reader of a bad release needs first.
func (s gateStore) listGates(ctx context.Context, promotionID string) ([]release.Gate, error) {
	var resp wireListGatesResponse
	if err := s.client.Call(ctx, procListGates, map[string]any{"promotionId": promotionID}, &resp); err != nil {
		return nil, err
	}
	return gatesFromWire(resp.Gates), nil
}

// ─── Error classification (§3.A: map reasons and codes, never message text) ──

// gateExitError is a gate failure carrying its §3.A exit code.
type gateExitError struct {
	code int
	msg  string
	// hint is the next step, when there is one a caller can act on.
	hint string
	// cause is the wire error, for errors.As/Unwrap — never for display
	// beyond what msg already says.
	cause error
}

func (e *gateExitError) Error() string {
	msg := e.msg
	if e.hint != "" {
		msg += "\n  fix: " + e.hint
	}
	return msg
}

func (e *gateExitError) ExitCode() int { return e.code }
func (e *gateExitError) Unwrap() error { return e.cause }

// classifyGateError maps a failed gate call onto the shared exit-code table.
//
// THE DISTINCTION THAT MATTERS IS "DID WE LOOK". An unreachable control plane
// or a refused credential is exitUndetermined (2) — forge could not record,
// which is not the same as a check having failed and must not be reported as
// one. An invalid gate is exitWrong (1): we looked at the request and it was
// wrong.
//
// Branching on cloud.Error's Code, never on message text: a server that
// improves its prose must not change forge's control flow.
func classifyGateError(err error, what string) error {
	if err == nil {
		return nil
	}
	var cerr *cloud.Error
	if !errors.As(err, &cerr) {
		// Not a control-plane failure at all — a local problem (an
		// unreadable file, a bad flag). Those are already reported by
		// their own call sites; anything reaching here we cannot
		// classify, and an unclassified failure is one we DID observe.
		return &gateExitError{code: exitWrong, msg: err.Error(), cause: err}
	}
	switch {
	case cerr.HasCode(cloud.CodeInvalidArgument):
		// The server applied the closed set (or the 8 KiB details cap)
		// and refused. Its message names what was wrong.
		return &gateExitError{
			code: exitWrong, msg: fmt.Sprintf("%s: %s", what, cerr.Message), cause: err,
			hint: "the status must be one of passed, failed, skipped, error, and details are capped at 8 KiB",
		}
	case cerr.HasCode(cloud.CodeNotFound):
		return &gateExitError{
			code: exitWrong, msg: fmt.Sprintf("%s: %s", what, cerr.Message), cause: err,
			hint: "check the promotion id with `forge env status <env> --history`, or name the release with --release",
		}
	case cerr.HasCode(cloud.CodeUnauthenticated), cerr.HasCode(cloud.CodePermissionDenied):
		// COULD NOT LOOK, not "the check failed". Recording evidence
		// needs deploy:write; a deploy:read token is refused here.
		return &gateExitError{
			code: exitUndetermined, msg: fmt.Sprintf("%s: %s", what, cerr.Message), cause: err,
			hint: "recording evidence needs a token with deploy:write (or member access); " +
				"`forge cloud token create` issues one",
		}
	case cerr.HasCode(cloud.CodeUnimplemented):
		return &gateExitError{
			code: exitUndetermined,
			msg: fmt.Sprintf("%s: this control plane does not serve gate evidence yet (%s)",
				what, cerr.Procedure),
			cause: err,
			hint:  "the control plane needs the RecordGate/ListGates procedures; upgrade it",
		}
	default:
		// Unreachable, unavailable, a proxy's HTML page: we could not
		// look.
		return &gateExitError{code: exitUndetermined, msg: fmt.Sprintf("%s: %s", what, cerr.Error()), cause: err}
	}
}
