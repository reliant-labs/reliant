// with_message_test.go — the exact client-facing message, on any code.
//
// WHY THIS EXISTS. Every other constructor composes its message from a
// detail plus a shape the package chose, and NotFound is the sharp case: it
// appends " not found", so no argument to it produces "that coupon code isn't
// valid". WithCause does not help either — it keeps the OUTER message and
// demotes the inner one to a server-only cause, which is precisely the
// display copy you were trying to set.
//
// So a handler with real user-facing copy on a standard code had to leave
// svcerr and hand-roll connect.NewError(...), which is the per-handler error
// mapping forgeconv-no-handler-error-mapping forbids — and which silently
// drops the sentinel, the reason header and the cause.
//
// The properties pinned here are the ones that make the override safe to use
// in place of that hand-rolled call: the message is byte-exact, the CODE is
// unchanged, and the error chain (errors.Is / errors.As / Cause) survives.
package svcerr_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// TestWithMessage_SetsTheExactClientText is the motivating case, written the
// way control-plane's coupon handler now writes it.
func TestWithMessage_SetsTheExactClientText(t *testing.T) {
	t.Parallel()
	const want = "that coupon code isn't valid"

	err := svcerr.WithMessage(svcerr.NotFound("coupon"), want)
	ce := svcerr.ToConnect(err)

	if ce.Code() != connect.CodeNotFound {
		t.Errorf("code = %v, want CodeNotFound — WithMessage must not change the code", ce.Code())
	}
	if ce.Message() != want {
		t.Errorf("message = %q, want %q (byte-exact)", ce.Message(), want)
	}
	// The composed shape it replaces must be gone entirely, not merely
	// prefixed: "<detail> not found" is what made NotFound unusable here.
	if strings.Contains(ce.Message(), "not found") {
		t.Errorf("message still carries the composed %q shape: %q", "not found", ce.Message())
	}
}

// TestWithMessage_WorksOnEveryCode proves this is a general facility rather
// than a NotFound special case — the coupon handler alone needs five codes.
func TestWithMessage_WorksOnEveryCode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		base error
		code connect.Code
		msg  string
	}{
		{"not found", svcerr.NotFound("coupon"), connect.CodeNotFound,
			"that coupon code isn't valid"},
		{"failed precondition", svcerr.FailedPrecondition("inactive"), connect.CodeFailedPrecondition,
			"that coupon is no longer active"},
		{"resource exhausted", svcerr.ResourceExhausted("exhausted"), connect.CodeResourceExhausted,
			"that coupon has been fully redeemed"},
		{"already exists", svcerr.AlreadyExists("redeemed"), connect.CodeAlreadyExists,
			"you've already redeemed that coupon"},
		{"invalid argument", svcerr.InvalidArgument("code"), connect.CodeInvalidArgument,
			"enter a coupon code"},
		{"permission denied", svcerr.PermissionDenied("nope"), connect.CodePermissionDenied,
			"you don't have access to that"},
		// A bare sentinel, with no detail at all, must also accept copy.
		{"bare sentinel", svcerr.ErrExpired, connect.CodeFailedPrecondition,
			"that coupon has expired"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ce := svcerr.ToConnect(svcerr.WithMessage(tc.base, tc.msg))
			if ce.Code() != tc.code {
				t.Errorf("code = %v, want %v", ce.Code(), tc.code)
			}
			if ce.Message() != tc.msg {
				t.Errorf("message = %q, want %q", ce.Message(), tc.msg)
			}
		})
	}
}

// TestWithMessage_PreservesTheErrorChain is what makes the override a
// replacement for connect.NewError rather than a different loss. The
// hand-rolled call discards the sentinel; this must not.
func TestWithMessage_PreservesTheErrorChain(t *testing.T) {
	t.Parallel()
	base := svcerr.NotFound("coupon")
	err := svcerr.WithMessage(base, "that coupon code isn't valid")

	if !errors.Is(err, svcerr.ErrNotFound) {
		t.Error("WithMessage broke errors.Is on the sentinel")
	}
	if !svcerr.IsNotFound(err) {
		t.Error("WithMessage broke svcerr.IsNotFound")
	}
	if got := svcerr.Code(err); got != connect.CodeNotFound {
		t.Errorf("svcerr.Code = %v, want CodeNotFound", got)
	}
	// And through the mapped Connect error, which is what a handler returns.
	if !errors.Is(svcerr.ToConnect(err), svcerr.ErrNotFound) {
		t.Error("the sentinel is unreachable through the mapped connect error")
	}
}

// TestWithMessage_DomainSentinelKeepsItsIdentity covers the shape control-plane
// actually uses: an APP-DEFINED sentinel wrapped by an svcerr constructor and
// then given copy. All three facts must survive together — the app's sentinel,
// the Connect code, and the exact text.
func TestWithMessage_DomainSentinelKeepsItsIdentity(t *testing.T) {
	t.Parallel()
	appSentinel := errors.New("coupon: not found")
	const want = "that coupon code isn't valid"

	err := svcerr.WithMessage(
		svcerr.WithCause(svcerr.NotFound("coupon"), appSentinel), want)

	if !errors.Is(err, appSentinel) {
		t.Error("the application's own sentinel is no longer reachable")
	}
	if !errors.Is(err, svcerr.ErrNotFound) {
		t.Error("the svcerr sentinel is no longer reachable")
	}
	ce := svcerr.ToConnect(err)
	if ce.Code() != connect.CodeNotFound {
		t.Errorf("code = %v, want CodeNotFound", ce.Code())
	}
	if ce.Message() != want {
		t.Errorf("message = %q, want %q", ce.Message(), want)
	}
}

// TestWithMessage_DoesNotLeakAWrappedDiagnostic is the redaction guard. An
// override sets what the client reads; it must not become a way for the text
// underneath to reach the wire.
func TestWithMessage_DoesNotLeakAWrappedDiagnostic(t *testing.T) {
	t.Parallel()
	raw := &pgShapedError{sqlstate: "42P01", detail: `relation "coupons" does not exist ` + plantedSecret}
	err := svcerr.WithMessage(svcerr.WithCause(svcerr.NotFound("coupon"), raw),
		"that coupon code isn't valid")

	ce := svcerr.ToConnect(err)
	if ce.Message() != "that coupon code isn't valid" {
		t.Errorf("message = %q", ce.Message())
	}
	if strings.Contains(ce.Error(), plantedSecret) {
		t.Errorf("the connection string reached the wire: %q", ce.Error())
	}
	if strings.Contains(ce.Error(), "SQLSTATE") {
		t.Errorf("driver diagnostics reached the wire: %q", ce.Error())
	}
	// Withheld from the client, still there for the operator.
	if cause := svcerr.Cause(err); cause == nil || !strings.Contains(cause.Error(), plantedSecret) {
		t.Errorf("the diagnostic must survive as a cause, got %v", cause)
	}
}

// TestWithMessage_DoesNotReclassifyAnUnrecognisedError is the boundary of the
// facility. Giving copy to a raw driver error is not a way to promote it: the
// code stays CodeInternal (nobody chose anything better) and the driver's own
// text stays withheld.
func TestWithMessage_DoesNotReclassifyAnUnrecognisedError(t *testing.T) {
	t.Parallel()
	raw := &pgShapedError{sqlstate: "42P01", detail: `relation "x" does not exist ` + plantedSecret}
	ce := svcerr.ToConnect(svcerr.WithMessage(raw, "something went wrong on our side"))

	if ce.Code() != connect.CodeInternal {
		t.Errorf("code = %v, want CodeInternal — a message must not promote an unclassified error", ce.Code())
	}
	if ce.Message() != "something went wrong on our side" {
		t.Errorf("message = %q", ce.Message())
	}
	if strings.Contains(ce.Error(), plantedSecret) {
		t.Errorf("driver text reached the wire: %q", ce.Error())
	}
}

// TestWithMessage_OuterWrappingStillDoesNotReachTheClient keeps the package's
// central rule intact: fmt.Errorf context is for the operator. An override
// does not change that — the client reads the message, not the accumulation.
func TestWithMessage_OuterWrappingStillDoesNotReachTheClient(t *testing.T) {
	t.Parallel()
	const want = "that coupon code isn't valid"
	inner := svcerr.WithMessage(svcerr.NotFound("coupon"), want)
	wrapped := fmt.Errorf("redeem coupon for org %s on shard %s: %w", "org_123", "shard-7", inner)

	ce := svcerr.ToConnect(wrapped)
	if ce.Message() != want {
		t.Errorf("message = %q, want %q — wrapping context must stay server-side", ce.Message(), want)
	}
	if strings.Contains(ce.Message(), "org_123") || strings.Contains(ce.Message(), "shard-7") {
		t.Errorf("operator context reached the client: %q", ce.Message())
	}
	if ce.Code() != connect.CodeNotFound {
		t.Errorf("code = %v, want CodeNotFound", ce.Code())
	}
}

// TestWithMessage_ComposesWithWithReason proves the override does not cost the
// machine-readable half. A frontend routes on the reason code and DISPLAYS the
// message; needing both is the normal case, not an exotic one.
func TestWithMessage_ComposesWithWithReason(t *testing.T) {
	t.Parallel()
	const (
		wantMsg    = "that coupon code isn't valid"
		wantReason = "coupon_not_found"
	)
	// Both nesting orders must work — neither is more correct, and a user
	// will write whichever reads better at the call site.
	t.Run("message outside reason", func(t *testing.T) {
		t.Parallel()
		ce := svcerr.ToConnect(svcerr.WithMessage(
			svcerr.WithReason(svcerr.NotFound("coupon"), wantReason), wantMsg))
		if ce.Message() != wantMsg {
			t.Errorf("message = %q, want %q", ce.Message(), wantMsg)
		}
		if got := ce.Meta().Get(svcerr.ReasonHeader); got != wantReason {
			t.Errorf("reason = %q, want %q", got, wantReason)
		}
	})
	t.Run("reason outside message", func(t *testing.T) {
		t.Parallel()
		ce := svcerr.ToConnect(svcerr.WithReason(
			svcerr.WithMessage(svcerr.NotFound("coupon"), wantMsg), wantReason))
		if ce.Message() != wantMsg {
			t.Errorf("message = %q, want %q", ce.Message(), wantMsg)
		}
		if got := ce.Meta().Get(svcerr.ReasonHeader); got != wantReason {
			t.Errorf("reason = %q, want %q", got, wantReason)
		}
	})
}

// TestWithMessage_NilAndEmptyAreNoOps pins the degenerate inputs, matching
// WithReason / WithCause / WithDetail next door. An empty message is declined
// rather than published: "" on the wire is worse than the composed default.
func TestWithMessage_NilAndEmptyAreNoOps(t *testing.T) {
	t.Parallel()
	if got := svcerr.WithMessage(nil, "anything"); got != nil {
		t.Errorf("WithMessage(nil, ...) = %v, want nil", got)
	}
	base := svcerr.NotFound("coupon")
	if got := svcerr.WithMessage(base, ""); got != base {
		t.Errorf("WithMessage(err, \"\") must return err unchanged, got %v", got)
	}
	if msg := svcerr.ToConnect(svcerr.WithMessage(base, "")).Message(); msg != "coupon not found" {
		t.Errorf("an empty override must leave the composed message intact, got %q", msg)
	}
}

// TestWithMessage_LastOverrideWins pins the nesting rule. Two overrides is a
// mistake rather than a design, but it must resolve predictably: the
// outermost is the one the author wrote last and meant.
func TestWithMessage_LastOverrideWins(t *testing.T) {
	t.Parallel()
	err := svcerr.WithMessage(svcerr.WithMessage(svcerr.NotFound("coupon"), "inner"), "outer")
	if got := svcerr.ToConnect(err).Message(); got != "outer" {
		t.Errorf("message = %q, want %q", got, "outer")
	}
}
