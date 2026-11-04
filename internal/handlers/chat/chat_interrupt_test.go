// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/threads"
)

func TestInterruptThread_TranslatesServiceResult(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	svc := &Service{deps: Deps{Repo: repo}, threads: threads.NewService(repo)}

	resp, err := svc.InterruptThread(ctx, connect.NewRequest(&reliantv1.InterruptThreadRequest{
		ChatId:   fx.chatID,
		ThreadId: fx.rootThreadID,
	}))
	require.NoError(t, err)
	assert.Equal(t, int32(0), resp.Msg.CancelledToolCalls)
	assert.Empty(t, resp.Msg.UndeliverableToolCalls)
}

// Every way threads.InterruptThread fails reaches the client with the code its
// svcerr kind names: the handler maps nothing. A read failure that is not a
// store miss is the server's, and its text stays in the log.
func TestInterruptThread_ErrorCodes(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	interrupt := func(t *testing.T, threadsSvc *threads.Service, chatID, threadID string) *connect.Error {
		t.Helper()
		svc := &Service{deps: Deps{Repo: repo}, threads: threadsSvc}
		_, err := svc.InterruptThread(ctx, connect.NewRequest(&reliantv1.InterruptThreadRequest{
			ChatId:   chatID,
			ThreadId: threadID,
		}))
		require.Error(t, err)
		return svcerr.ToConnect(err)
	}

	t.Run("a missing thread id is InvalidArgument", func(t *testing.T) {
		ce := interrupt(t, threads.NewService(repo), fx.chatID, "")
		assert.Equal(t, connect.CodeInvalidArgument, ce.Code())
		assert.Equal(t, "thread_id is required", ce.Message())
	})
	t.Run("a chat the caller cannot see is NotFound", func(t *testing.T) {
		ce := interrupt(t, threads.NewService(repo), uuid.NewString(), fx.rootThreadID)
		assert.Equal(t, connect.CodeNotFound, ce.Code())
		assert.Equal(t, "chat not found", ce.Message())
	})
	t.Run("a thread of another chat is NotFound", func(t *testing.T) {
		ce := interrupt(t, threads.NewService(repo), fx.chatID, uuid.NewString())
		assert.Equal(t, connect.CodeNotFound, ce.Code())
		assert.Equal(t, "thread not found", ce.Message())
	})
	t.Run("an undeliverable signal is Unavailable, without Temporal's text", func(t *testing.T) {
		ce := interrupt(t, threads.NewService(repo, threads.WithTemporalSignaler(failingSignaler{})), fx.chatID, fx.rootThreadID)
		assert.Equal(t, connect.CodeUnavailable, ce.Code())
		assert.Equal(t, "interrupt undeliverable", ce.Message())
	})
	for _, tc := range []struct {
		name string
		err  error
		want connect.Code
	}{
		{"a deadline is DeadlineExceeded", context.DeadlineExceeded, connect.CodeDeadlineExceeded},
		{"a cancellation is Canceled", context.Canceled, connect.CodeCanceled},
		{"an opaque read failure is Internal", errors.New("dsn=postgres://u:secret@h"), connect.CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broken := &MockRepository{GetChatRowWithUserCheckFunc: func(context.Context, string, string) (*db.ChatRow, error) { return nil, tc.err }}
			ce := interrupt(t, threads.NewService(broken), fx.chatID, fx.rootThreadID)
			assert.Equal(t, tc.want, ce.Code())
			assert.NotContains(t, ce.Message(), "secret")
		})
	}
}

type failingSignaler struct{}

func (failingSignaler) SignalWorkflow(context.Context, string, string, string, interface{}) error {
	return errors.New("dial tcp temporal-frontend:7233: connection refused")
}
