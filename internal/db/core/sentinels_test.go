package core

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// The domain sentinels are svcerr kinds, declared where they live, so a
// handler's bare svcerr.Wrap(err) answers each with its own code and its own
// sentence instead of Internal "internal server error". Two things must not
// change in the move: errors.Is against the sentinel (callers branch on it
// through any amount of wrapping), and the sentinel's text, which reaches
// clients and logs verbatim.
func TestSentinelsAreClassifiedWithUnchangedIdentityAndText(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code connect.Code
		text string
	}{
		{ErrChatNotFound, connect.CodeNotFound, "chat not found"},
		{ErrWorktreeNotFound, connect.CodeNotFound, "worktree not found"},
		{ErrProjectNotFound, connect.CodeNotFound, "project not found or access denied"},
		{ErrTriggerNotFound, connect.CodeNotFound, "trigger not found"},
		{ErrTriggerEventNotFound, connect.CodeNotFound, "trigger event not found"},
		{ErrTriggerRegistrationNotFound, connect.CodeNotFound, "trigger registration not found"},
		{ErrRunEventNotFound, connect.CodeNotFound, "run event not found"},
		{ErrProviderAccountNotFound, connect.CodeNotFound, "provider account not found"},
		{ErrConnectionNotFound, connect.CodeNotFound, "connection not found"},
		{ErrModelEndpointNotFound, connect.CodeNotFound, "model endpoint not found"},
		{ErrIntegrationWriteNotFound, connect.CodeNotFound, "integration write not found"},
		{ErrPlanNotFound, connect.CodeNotFound, "plan not found: sql: no rows in result set"},

		{ErrWorktreeNameTaken, connect.CodeAlreadyExists, "a live worktree already has this name"},
		{ErrConnectionNameTaken, connect.CodeAlreadyExists, "connection name already in use"},
		{ErrModelEndpointNameTaken, connect.CodeAlreadyExists, "a model endpoint with that name already exists"},
		{ErrOAuthFlowInvalid, connect.CodeFailedPrecondition, "oauth flow is invalid, expired or already used"},
		{ErrGenerationConflict, connect.CodeAborted, "connection secret changed concurrently"},
		{ErrInvalidAccountOrder, connect.CodeInvalidArgument, "account order must list each of the provider's accounts exactly once"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.text {
				t.Errorf("Error() = %q, want %q", got, tc.text)
			}
			// As the store returns it: wrapped with the id, then wrapped again.
			err := fmt.Errorf("load: %w", fmt.Errorf("%w: id-1", tc.err))
			if !errors.Is(err, tc.err) {
				t.Fatalf("errors.Is lost the sentinel through wrapping")
			}
			wire := svcerr.ToConnect(err)
			if wire.Code() != tc.code {
				t.Errorf("wire code = %v, want %v", wire.Code(), tc.code)
			}
			// svcerr publishes the sentinel's own sentence, never the wrapper's
			// id: ErrPlanNotFound's text is the one exception, the constructor's
			// detail ("plan not found") being what it was declared with.
			want := tc.text
			if tc.err == ErrPlanNotFound {
				want = "plan not found"
			}
			if wire.Message() != want {
				t.Errorf("wire message = %q, want %q", wire.Message(), want)
			}
		})
	}

	// Distinct sentinels stay distinct even where they share a kind.
	if errors.Is(ErrChatNotFound, ErrWorktreeNotFound) {
		t.Error("ErrChatNotFound matches ErrWorktreeNotFound")
	}
	// ErrPlanNotFound still answers the tool layer's sql.ErrNoRows branch.
	if !errors.Is(fmt.Errorf("x: %w", ErrPlanNotFound), sql.ErrNoRows) {
		t.Error("ErrPlanNotFound lost sql.ErrNoRows")
	}
}
