package cli

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
)

// TestHostedErrorHasCode_ReadsTheCodeNotTheMessage is the F0 rule that
// replaced `strings.Contains(err.Error(), "already_exists")`.
//
// The old spelling was wrong in BOTH directions, and both are pinned here:
// a server that rewords its prose must keep its classification, and a
// message that merely MENTIONS a code must not be read as that code.
func TestHostedErrorHasCode_ReadsTheCodeNotTheMessage(t *testing.T) {
	conflict := &cloud.Error{
		HTTPStatus: http.StatusConflict,
		Code:       cloud.CodeAlreadyExists,
		Message:    "that version has already been cut",
		Procedure:  "controlplane.v1.DeployService/CutRelease",
	}
	if !hostedErrorHasCode(conflict, cloud.CodeAlreadyExists) {
		t.Error("an already_exists error must be recognised by its code")
	}
	if hostedErrorHasCode(conflict, cloud.CodeNotFound) {
		t.Error("a code must not match a different code")
	}

	// Reworded prose, same code. Under the old strings.Contains spelling
	// this depended entirely on the server's wording.
	reworded := &cloud.Error{Code: cloud.CodeAlreadyExists, Message: "a totally different sentence"}
	if !hostedErrorHasCode(reworded, cloud.CodeAlreadyExists) {
		t.Error("the classification must not depend on the message text")
	}

	// THE FALSE POSITIVE the old code could not avoid: a message that
	// talks ABOUT a code is not that code. forge's own error text names
	// codes, so this case was reachable in practice.
	mentions := &cloud.Error{
		Code:    cloud.CodeInvalidArgument,
		Message: `the server rejected the request; it did not report already_exists or not_found`,
	}
	if hostedErrorHasCode(mentions, cloud.CodeAlreadyExists) {
		t.Error("a message that merely mentions a code must not read as that code")
	}
	if hostedErrorHasCode(mentions, cloud.CodeNotFound) {
		t.Error("a message that merely mentions a code must not read as that code")
	}

	// The classification survives the context wrapping every caller adds.
	wrapped := fmt.Errorf("cut release %q: %w", "v1.4.0", conflict)
	if !hostedErrorHasCode(wrapped, cloud.CodeAlreadyExists) {
		t.Error("a wrapped error must keep its code (errors.As, not a type assertion)")
	}

	// A plain error carries no code and must not match anything.
	if hostedErrorHasCode(errors.New("already_exists"), cloud.CodeAlreadyExists) {
		t.Error("a plain error whose TEXT is a code must not be classified as one")
	}
	if hostedErrorHasCode(nil, cloud.CodeNotFound) {
		t.Error("nil carries no code")
	}
}

// TestHostedErrorReason_IsTheRefusalDiscriminator: every promote refusal
// shares FailedPrecondition, so the code alone cannot say which happened.
func TestHostedErrorReason_IsTheRefusalDiscriminator(t *testing.T) {
	for _, reason := range []string{reasonPromotionConflict, reasonRolloutInFlight, reasonEnvironmentPinned} {
		err := &cloud.Error{Code: cloud.CodeFailedPrecondition, Reason: reason, Message: "refused"}
		if got := hostedErrorReason(err); got != reason {
			t.Errorf("hostedErrorReason = %q, want %q", got, reason)
		}
		if got := hostedErrorReason(fmt.Errorf("promote: %w", err)); got != reason {
			t.Errorf("wrapped reason = %q, want %q", got, reason)
		}
	}
	if got := hostedErrorReason(errors.New("boom")); got != "" {
		t.Errorf("a plain error carries no reason; got %q", got)
	}
}

// TestExitCodeForRefusal_SeparatesConflictFromRefused pins the distinction
// CI acts on: 3 means stop and page a human (someone else moved the env, so
// retrying would stomp it), 4 means the write was declined but nothing was
// lost, so waiting and retrying is legitimate.
func TestExitCodeForRefusal_SeparatesConflictFromRefused(t *testing.T) {
	for _, tc := range []struct {
		reason string
		want   int
	}{
		{reasonPromotionConflict, exitConflict},
		{reasonRolloutInFlight, exitRefused},
		{reasonEnvironmentPinned, exitRefused},
		// A reason forge does not know is a forge gap, not an
		// unobservable environment: the server refused, which means it
		// looked. Reporting 2 would tell CI to retry a write that will
		// be refused identically.
		{"some_future_reason", exitWrong},
		{"", exitWrong},
	} {
		if got := exitCodeForRefusal(tc.reason); got != tc.want {
			t.Errorf("exitCodeForRefusal(%q) = %d, want %d", tc.reason, got, tc.want)
		}
	}
}
