package launch

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Each launch error carries the svcerr kind a handler returns as it is: the
// client reads the code and the Reason, never the cause, while errors.Is and
// errors.As still reach the cause for the server.
func TestLaunchErrors_CarryTheirKind(t *testing.T) {
	t.Parallel()

	cause := errors.New("dsn=postgres://u:secret@h")
	cases := []struct {
		name     string
		err      error
		wantCode connect.Code
		wantMsg  string
	}{
		{"malformed spec", &ValidationError{Kind: ValidationInvalidArgument, Reason: "project_id is required", Err: cause}, connect.CodeInvalidArgument, "project_id is required"},
		{"world cannot host it", &ValidationError{Kind: ValidationFailedPrecondition, Reason: "project has no main worktree"}, connect.CodeFailedPrecondition, "project has no main worktree"},
		{"not found", &NotFoundError{Reason: "chat not found", Err: cause}, connect.CodeNotFound, "chat not found"},
		{"internal", &InternalError{Reason: "failed to create chat", Err: cause}, connect.CodeInternal, "failed to create chat"},
		{"already launched", &AlreadyLaunchedError{ChatID: "chat-1"}, connect.CodeAlreadyExists, "already launched: chat chat-1"},
		{"not pending", ErrNotPending, connect.CodeFailedPrecondition, "chat has already started; use SendMessage"},
		{"client message reused", ErrClientMessageReused, connect.CodeAlreadyExists, "client_message_id was already used for a different message; use a new id for a new message"},
		{"workflow lookup", &WorkflowLookupError{Err: cause}, connect.CodeInternal, "failed to look up workflow"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ce := svcerr.ToConnect(tc.err)
			assert.Equal(t, tc.wantCode, ce.Code())
			assert.Equal(t, tc.wantMsg, ce.Message())
			assert.NotContains(t, ce.Message(), "secret")
		})
	}
}

func TestLaunchErrors_KeepTheirIdentityAndCause(t *testing.T) {
	t.Parallel()

	notFound := &NotFoundError{Reason: "chat not found", Err: sql.ErrNoRows}
	assert.ErrorIs(t, notFound, ErrNotFound)
	assert.ErrorIs(t, notFound, sql.ErrNoRows, "the cause is withheld from the client, not from the server")

	internal := &InternalError{Reason: "failed to start chat", Err: context.DeadlineExceeded}
	assert.ErrorIs(t, internal, ErrInternal)
	assert.ErrorIs(t, internal, context.DeadlineExceeded)
	assert.Equal(t, connect.CodeInternal, svcerr.Code(internal), "an internal launch failure stays Internal")

	lookup := &WorkflowLookupError{Err: errors.New("store down")}
	var asLookup *WorkflowLookupError
	require.ErrorAs(t, &ValidationError{Reason: "x", Err: lookup}, &asLookup)
	assert.Same(t, lookup, asLookup)

	assert.ErrorIs(t, &AlreadyLaunchedError{ChatID: "c"}, ErrAlreadyLaunched)
}
