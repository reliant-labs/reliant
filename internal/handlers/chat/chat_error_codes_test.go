package chat_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	chat "github.com/reliant-labs/reliant/internal/handlers/chat"
)

// readFailures is what a repository read can fail with, and the code the
// client reads for each: a classified error keeps its own code, and an
// opaque one is Internal with the handler's summary and none of its text.
var readFailures = []struct {
	name string
	err  error
	want connect.Code
}{
	{"a store miss is NotFound", core.ErrChatNotFound, connect.CodeNotFound},
	{"a deadline is DeadlineExceeded", context.DeadlineExceeded, connect.CodeDeadlineExceeded},
	{"a cancellation is Canceled", context.Canceled, connect.CodeCanceled},
	{"an opaque error is Internal", errors.New("dsn=postgres://u:secret@h"), connect.CodeInternal},
}

func requireClientReads(t *testing.T, err error, want connect.Code) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, want, svcerr.Code(err), "err = %v", err)
	require.NotContains(t, svcerr.ToConnect(err).Message(), "secret")
}

func newChatService(t *testing.T, repo *chat.MockRepository) *chat.Service {
	t.Helper()
	return chat.NewTestChat(t, chat.WithChatDeps(chat.Deps{Repo: repo}))
}

func ownedChat(_ context.Context, id string) (*db.Chat, error) {
	return &db.Chat{ID: id, UserID: "test-user"}, nil
}

func ownedChatRow(_ context.Context, id string) (*db.ChatRow, error) {
	return &db.ChatRow{ID: id, UserID: "test-user"}, nil
}

// The ownership read every chat rpc starts with passes a classified store
// error through and redacts an opaque one. One rpc per shape of the read: by
// id (Repo.GetChat, then the owner compared), and by id and owner
// (GetChatWithUserCheck).
func TestChatOwnershipRead_RepositoryFailureCodes(t *testing.T) {
	t.Parallel()

	for _, tc := range readFailures {
		byID := func(t *testing.T) *chat.Service {
			return newChatService(t, &chat.MockRepository{
				GetChatFunc:    func(context.Context, string) (*db.Chat, error) { return nil, tc.err },
				GetChatRowFunc: func(context.Context, string) (*db.ChatRow, error) { return nil, tc.err },
			})
		}
		byOwner := func(t *testing.T) *chat.Service {
			return newChatService(t, &chat.MockRepository{
				GetChatWithUserCheckFunc:    func(context.Context, string, string) (*db.Chat, error) { return nil, tc.err },
				GetChatRowWithUserCheckFunc: func(context.Context, string, string) (*db.ChatRow, error) { return nil, tc.err },
			})
		}
		ctx := chat.AuthedContext(t)

		t.Run("GetChat: "+tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := byID(t).GetChat(ctx, connect.NewRequest(&pb.GetChatRequest{ChatId: "chat-1"}))
			requireClientReads(t, err, tc.want)
		})
		t.Run("UpdateChatState: "+tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := byID(t).UpdateChatState(ctx, connect.NewRequest(&pb.UpdateChatStateRequest{
				ChatId: "chat-1", State: pb.ChatState_CHAT_STATE_ARCHIVED,
			}))
			requireClientReads(t, err, tc.want)
		})
		t.Run("TerminateChat: "+tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := byID(t).TerminateChat(ctx, connect.NewRequest(&pb.TerminateChatRequest{ChatId: "chat-1"}))
			requireClientReads(t, err, tc.want)
		})
		t.Run("GetWorkflowExecutions: "+tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := byID(t).GetWorkflowExecutions(ctx, connect.NewRequest(&pb.GetWorkflowExecutionsRequest{ChatId: "chat-1"}))
			requireClientReads(t, err, tc.want)
		})
		t.Run("SendAgentMessage: "+tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := byOwner(t).SendAgentMessage(ctx, connect.NewRequest(&pb.SendAgentMessageRequest{
				ChatId: "chat-1", ThreadId: "thread-1", Message: "hello",
			}))
			requireClientReads(t, err, tc.want)
		})
		t.Run("RetryUsageLimitWait: "+tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := byOwner(t).RetryUsageLimitWait(ctx, connect.NewRequest(&pb.RetryUsageLimitWaitRequest{ChatId: "chat-1"}))
			requireClientReads(t, err, tc.want)
		})
	}
}

// A read after ownership is settled, and a write: the same passthrough, with
// the rpc's own summary for an opaque failure.
func TestChatRepositoryCalls_FailureCodes(t *testing.T) {
	t.Parallel()

	for _, tc := range readFailures {
		ctx := chat.AuthedContext(t)

		t.Run("ListChats: "+tc.name, func(t *testing.T) {
			t.Parallel()
			svc := newChatService(t, &chat.MockRepository{
				ListChatsFunc: func(context.Context, db.ChatFilters) ([]*db.Chat, error) { return nil, tc.err },
			})
			_, err := svc.ListChats(ctx, connect.NewRequest(&pb.ListChatsRequest{ProjectId: "project-1"}))
			requireClientReads(t, err, tc.want)
		})
		t.Run("GetWorkflowExecutions list: "+tc.name, func(t *testing.T) {
			t.Parallel()
			svc := newChatService(t, &chat.MockRepository{
				GetChatFunc:             ownedChat,
				GetChatRowFunc:          ownedChatRow,
				ListWorkflowsByChatFunc: func(context.Context, string) ([]*db.Workflow, error) { return nil, tc.err },
			})
			_, err := svc.GetWorkflowExecutions(ctx, connect.NewRequest(&pb.GetWorkflowExecutionsRequest{ChatId: "chat-1"}))
			requireClientReads(t, err, tc.want)
		})
		t.Run("UpdateChatState write: "+tc.name, func(t *testing.T) {
			t.Parallel()
			svc := newChatService(t, &chat.MockRepository{
				GetChatFunc:    ownedChat,
				GetChatRowFunc: ownedChatRow,
				UpdateChatStateFunc: func(context.Context, string, db.ChatState, string) error {
					return tc.err
				},
			})
			_, err := svc.UpdateChatState(ctx, connect.NewRequest(&pb.UpdateChatStateRequest{
				ChatId: "chat-1", State: pb.ChatState_CHAT_STATE_ARCHIVED,
			}))
			requireClientReads(t, err, tc.want)
		})
		t.Run("BranchChat: "+tc.name, func(t *testing.T) {
			t.Parallel()
			svc := newChatService(t, &chat.MockRepository{
				GetMessageFunc: func(context.Context, string) (*db.Message, error) { return nil, tc.err },
			})
			_, err := svc.BranchChat(ctx, connect.NewRequest(&pb.BranchChatRequest{MessageId: "message-1"}))
			requireClientReads(t, err, tc.want)
		})
	}
}

// A chat the caller does not own is NotFound, never PermissionDenied:
// existence is not disclosed.
func TestChatOwnership_ForeignChatIsNotFound(t *testing.T) {
	t.Parallel()

	svc := newChatService(t, &chat.MockRepository{
		GetChatFunc: func(_ context.Context, id string) (*db.Chat, error) {
			return &db.Chat{ID: id, UserID: "someone-else"}, nil
		},
	})
	_, err := svc.GetChat(chat.AuthedContext(t), connect.NewRequest(&pb.GetChatRequest{ChatId: "chat-1"}))
	requireClientReads(t, err, connect.CodeNotFound)
	require.Equal(t, "chat not found", svcerr.ToConnect(err).Message())
}

// The opaque failure's summary is the rpc's own words, and the cause stays
// in the log.
func TestChatOpaqueFailure_ReadsTheSummary(t *testing.T) {
	t.Parallel()

	svc := newChatService(t, &chat.MockRepository{
		GetChatFunc: func(context.Context, string) (*db.Chat, error) {
			return nil, errors.New("dsn=postgres://u:secret@h")
		},
		GetChatRowFunc: func(context.Context, string) (*db.ChatRow, error) {
			return nil, errors.New("dsn=postgres://u:secret@h")
		},
	})
	_, err := svc.TerminateChat(chat.AuthedContext(t), connect.NewRequest(&pb.TerminateChatRequest{ChatId: "chat-1"}))
	require.Equal(t, "failed to get chat", svcerr.ToConnect(err).Message())
	require.ErrorContains(t, svcerr.Cause(err), "secret")
}
