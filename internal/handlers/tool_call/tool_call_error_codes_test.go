package tool_call_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	tool_call "github.com/reliant-labs/reliant/internal/handlers/tool_call"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
)

// The tool-call lookup both rpcs start with passes a classified repository
// error through and redacts an opaque one: an outage is no longer "tool call
// not found".
func TestToolCallRPCs_RepositoryFailureCodes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
		want connect.Code
	}{
		{"a store miss is NotFound", svcerr.NotFound("tool call"), connect.CodeNotFound},
		{"a deadline is DeadlineExceeded", context.DeadlineExceeded, connect.CodeDeadlineExceeded},
		{"a cancellation is Canceled", context.Canceled, connect.CodeCanceled},
		{"an opaque error is Internal", errors.New("dsn=postgres://u:secret@h"), connect.CodeInternal},
	} {
		repo := func() *tool_call.MockRepository {
			return &tool_call.MockRepository{
				GetContentBlockByToolCallIDFunc: func(context.Context, string) (*db.MessageContentBlock, error) { return nil, tc.err },
			}
		}
		t.Run("CancelToolCall: "+tc.name, func(t *testing.T) {
			t.Parallel()
			svc := tool_call.NewTestToolCall(t, tool_call.WithToolCallDeps(tool_call.Deps{Repo: repo()}))
			_, err := svc.CancelToolCall(tool_call.AuthedContext(t), connect.NewRequest(&pb.CancelToolCallRequest{ToolCallId: "call-1"}))
			requireClientReads(t, err, tc.want)
		})
		t.Run("ConvertToBackground: "+tc.name, func(t *testing.T) {
			t.Parallel()
			svc := tool_call.NewTestToolCall(t, tool_call.WithToolCallDeps(tool_call.Deps{Repo: repo()}))
			_, err := svc.ConvertToBackground(tool_call.AuthedContext(t), connect.NewRequest(&pb.ConvertToBackgroundRequest{ToolCallId: "call-1"}))
			requireClientReads(t, err, tc.want)
		})
	}
}

// A background request the daemon cannot take is classified by the machine's
// state (daemonerr.Error): a machine that is starting is Unavailable with the
// "no daemon connected" marker the web's machine-wait keys on, no machine at
// all is FailedPrecondition with "no daemon available", and a transport
// failure is Unavailable without the transport's text.
func TestConvertToBackground_DaemonFailureCodes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		err      error
		want     connect.Code
		contains string
	}{
		{"a starting machine", toolexec.ErrDaemonPending, connect.CodeUnavailable, "no daemon connected"},
		{"no machine", toolexec.ErrNoDaemon, connect.CodeFailedPrecondition, "no daemon available"},
		{"a busy machine", toolexec.ErrDaemonTimeout, connect.CodeDeadlineExceeded, "did not answer in time"},
		{"a transport failure", errors.New("nats: subject reliant.secret.daemon"), connect.CodeUnavailable, "background this command failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := tool_call.NewTestToolCall(t, tool_call.WithToolCallDeps(tool_call.Deps{
				Repo:   ownedToolCallRepo(),
				Router: &toolexectest.Recorder{BackgroundErr: tc.err},
			}))
			_, err := svc.ConvertToBackground(tool_call.AuthedContext(t), connect.NewRequest(&pb.ConvertToBackgroundRequest{ToolCallId: "call-1"}))
			requireClientReads(t, err, tc.want)
			msg := svcerr.ToConnect(err).Message()
			require.True(t, strings.Contains(msg, tc.contains), "message %q lacks %q", msg, tc.contains)
		})
	}
}

// ownedToolCallRepo answers call-1 as a tool call in a chat the caller owns.
func ownedToolCallRepo() *tool_call.MockRepository {
	callID := "call-1"
	return &tool_call.MockRepository{
		GetContentBlockByToolCallIDFunc: func(context.Context, string) (*db.MessageContentBlock, error) {
			return &db.MessageContentBlock{ID: "block-1", MessageID: "msg-1", ToolCallID: &callID}, nil
		},
		GetMessageFunc: func(_ context.Context, id string) (*db.Message, error) {
			return &db.Message{ID: id, ChatID: "chat-tool-call-codes"}, nil
		},
		GetChatFunc: func(_ context.Context, id string) (*db.Chat, error) {
			return &db.Chat{ID: id, UserID: "test-user"}, nil
		},
		GetChatRowFunc: func(_ context.Context, id string) (*db.ChatRow, error) {
			return &db.ChatRow{ID: id, UserID: "test-user"}, nil
		},
	}
}

func requireClientReads(t *testing.T, err error, want connect.Code) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, want, svcerr.Code(err), "err = %v", err)
	require.NotContains(t, svcerr.ToConnect(err).Message(), "secret")
}
