// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package question

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// Questions lists a chat's or a user's questions. Each takes the chat or user
// the handler already authorized, and fails closed on an empty one.
// store.QuestionExtStore satisfies it.
type Questions interface {
	ListAnswerablePendingQuestionsByChat(ctx context.Context, chatID string) ([]*db.Question, error)
	ListAnswerablePendingQuestionsByUser(ctx context.Context, userID string) ([]*db.Question, error)
	ListAsyncQuestionsByChat(ctx context.Context, chatID string) ([]*db.Question, error)
	PendingQuestionsByThread(ctx context.Context, chatID string) (map[string]*db.Question, error)
}

// Repository is the slice of *db.Repo the question handlers use.
type Repository interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetChatRow(ctx context.Context, id string) (*db.ChatRow, error)
	GetQuestionByID(ctx context.Context, id string) (*db.Question, error)
	EmitUserRefetch(ctx context.Context, userID string, refetchType db.RefetchType, opts db.RefetchOpts) error
	// ResolveQuestionBy resolves a pending question as answeredBy's answer
	// (nil for the owner), false when it was already resolved.
	ResolveQuestionBy(ctx context.Context, id string, responseData, answeredBy *string) (bool, error)
	EmitQuestionUpdate(ctx context.Context, chatID string, update db.QuestionUpdate) error
	SaveMessageToThread(ctx context.Context, chatID, thread string, role int32, content string, workflowID *string, attachmentIDs []string, displayStyle *int32) (*db.Message, error)
}

// Signaler signals a workflow run, resetting an interrupted one so the
// signal still lands. *workflow.PauseService implements it.
type Signaler interface {
	SignalWithRecovery(ctx context.Context, workflowID, signalName string, signalData interface{}, inputUpdate map[string]interface{}) error
}

// Messenger sends a user's message to a chat's thread: SendMessage for the
// main thread, SendAgentMessage for a sub-agent's. *chat.Service implements
// it.
type Messenger interface {
	SendMessage(ctx context.Context, req *connect.Request[pb.SendMessageRequest]) (*connect.Response[pb.SendMessageResponse], error)
	SendAgentMessage(ctx context.Context, req *connect.Request[pb.SendAgentMessageRequest]) (*connect.Response[pb.SendAgentMessageResponse], error)
}

// Mailbox queues the answer given behind a blocking question and announces it
// to the chat's open clients. *agentmailbox.Mailbox satisfies it.
type Mailbox interface {
	EnqueueAgentMessageOnce(ctx context.Context, msg *db.AgentMessage) (bool, error)
}

// ReplyGate is what the question service asks the chat service about a
// person answering in someone else's chat (control-plane
// docs/design/chat-collaboration.md §5.3, §6.1): whether a member's answer is
// refused, and who an answer's message is recorded as from. The chat service
// satisfies it.
type ReplyGate interface {
	MemberReplyRefusal(ctx context.Context, chat *db.Chat, sender string) error
	ReplyAuthor(ctx context.Context, chat *db.Chat, sender string) db.MessageAuthor
}
