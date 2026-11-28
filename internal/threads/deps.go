// forge:deps — consumer-declared interfaces for this package's Deps.
// Copyright (c) 2025 Reliant Labs

package threads

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/chataccess"
	"github.com/reliant-labs/reliant/internal/db"
)

// Deps is what a Service is built from. Plain Deps, not a forge contract
// package: it is built by hand by the worker, the handlers and the launcher.
type Deps struct {
	// Repo is everything the service reads and writes.
	Repo Repository
	// Chats reads the chat a pause or interrupt names, as a chat the caller
	// may write (chataccess). Required by InterruptThread and
	// CancelChatToolCalls; a service built without it refuses them.
	Chats chataccess.Service
	// Messages answers SaveMessage's idempotency checks: whether a message
	// id or an activity's message already exists. Required for SaveMessage
	// (a read-only caller may leave it nil). store.MessageExtStore
	// satisfies it.
	Messages Messages
	// Senders titles the sender of an agent-to-agent message SaveMessage
	// writes. Required for SaveMessage (a read-only caller may leave it
	// nil). store.ThreadExtStore satisfies it.
	Senders Senders
	// AmbientMemory commits a new thread's ambient-memory policy inside the
	// creating transaction. When nil, New derives it only when Repo supplies
	// the role or a non-nil ORM handle; otherwise it remains nil and workflow
	// creation fails closed. An explicit implementation is always retained,
	// even with a nil Repo (workflow creation still requires Repo).
	// store.ThreadExtStore satisfies it.
	//
	// forge:optional-dep
	AmbientMemory AmbientMemory
	// Signaler wakes a thread's workflow when it is interrupted. Nil sends
	// no signal: the interrupt still cancels the thread's running tools.
	Signaler TemporalSignaler
	// ToolCanceler cancels a thread's running tool executions on the daemon.
	// Nil leaves them to finish.
	ToolCanceler ToolCanceler
}

// Messages finds the message an earlier attempt of a save wrote, by id or by
// the activity that wrote it. Both return (nil, nil) when none exists.
// store.MessageExtStore satisfies it.
type Messages interface {
	FindMessage(ctx context.Context, id string) (*db.Message, error)
	FindMessageByActivity(ctx context.Context, chatID, workflowID, activityID string) (*db.Message, error)
}

// Senders maps sender thread ids to the heading a message from each is shown
// under. store.ThreadExtStore satisfies it.
type Senders interface {
	SenderTitlesByThreadIDs(ctx context.Context, threadIDs []string) (map[string]string, error)
}

// AmbientMemory latches a thread's ambient-memory exclusion once, and answers
// the value in force. store.ThreadExtStore satisfies it.
type AmbientMemory interface {
	ResolveThreadAmbientMemoryExclusion(ctx context.Context, chatID, threadID string, requested bool) (bool, error)
}

// Repository defines the subset of *db.Repo needed by the threads service.
// This allows for easier testing and decouples threads from the full repository.
type Repository interface {
	// Thread operations
	CreateThread(ctx context.Context, thread *db.Thread) (*db.Thread, error)
	GetThread(ctx context.Context, id string) (*db.Thread, error)
	GetThreadWithParent(ctx context.Context, id string) (*db.Thread, *string, error)
	GetRootThread(ctx context.Context, conversationID string) (*db.Thread, error)

	// Workflow operations
	CreateWorkflow(ctx context.Context, workflow *db.Workflow) error
	UpdateThreadWorkflow(ctx context.Context, threadID, workflowID string) (*db.Thread, error)

	// Context window operations
	CreateContextWindow(ctx context.Context, cw *db.ContextWindow) (*db.ContextWindow, error)
	GetContextWindow(ctx context.Context, id string) (*db.ContextWindow, error)
	GetLatestContextWindow(ctx context.Context, threadID string) (*db.ContextWindow, error)
	GetContextWindowBySequence(ctx context.Context, threadID string, sequence int) (*db.ContextWindow, error)
	GetMaxSequenceForThread(ctx context.Context, threadID string) (int, error)
	SetCompactionSummaryMessage(ctx context.Context, cwID, messageID string) (*db.ContextWindow, error)

	// Message operations (for resolution, token counting, and fork point resolution)
	GetMessage(ctx context.Context, id string) (*db.Message, error)
	GetMessagesByContextWindow(ctx context.Context, contextWindowID string, maxOrdinal *int64) ([]*db.Message, error)
	GetLatestMessageWithTokensInThread(ctx context.Context, threadID string, contextSequence int) (*db.Message, error)
	// GetLatestMessageInThread returns (nil, nil) for a thread with no
	// messages -- used by resolveForkPoint to fork a thread's latest state.
	GetLatestMessageInThread(ctx context.Context, threadID string) (*db.Message, error)

	// Bounded message operations for the display walk
	// (recentDisplayMessagesBeforeFromCW) and CountCurrentMessages -- these
	// bound the read or count in SQL, one window at a time, instead of
	// resolving a thread's whole history. The walk's read leaves out
	// DISPLAY_STYLE_HIDDEN rows, so its limit counts transcript rows.
	CountMessagesByContextWindow(ctx context.Context, contextWindowID string) (int, error)
	CountMessagesByContextWindowUpToSeq(ctx context.Context, contextWindowID string, maxSeq int64) (int, error)
	ListRecentTranscriptMessagesInContextWindowBeforeSeq(ctx context.Context, contextWindowID string, beforeSeq int64, limit int) ([]*db.Message, error)

	// Message operations (for SaveMessage)
	CreateMessage(ctx context.Context, msg *db.Message) error
	DeleteMessage(ctx context.Context, messageID string) error
	GetEffectiveMessageCount(ctx context.Context, chatID, threadID string) (int, error)
	// AllocateMessageOrder takes a message's ordinal and seq from their
	// counters (db.Repo.AllocateMessageOrder).
	AllocateMessageOrder(ctx context.Context, threadID, chatID string) (ordinal, seq int64, err error)
	// AllocateContextWindowSequence takes the sequence of a thread's next
	// context window from its counter.
	AllocateContextWindowSequence(ctx context.Context, threadID string) (int, error)
	GetContextUsage(ctx context.Context, chatID, threadID string) (*db.ContextUsage, error)

	// Content block operations
	CreateContentBlock(ctx context.Context, block *db.MessageContentBlock) error
	ListContentBlocks(ctx context.Context, messageID string) ([]*db.MessageContentBlock, error)

	// Tool call operations. A live message update carries each tool-call
	// block's durable status and, for a spawn, the thread it started — see
	// db.ContentBlockPayloadsWithToolCalls.
	// ListLiveToolCallsByChat is what interrupt and pause cancel: a chat's
	// non-terminal calls only, read through the partial live-calls index
	// rather than the chat's whole tool-call history.
	ListLiveToolCallsByChat(ctx context.Context, chatID string) ([]*db.ToolCall, error)
	ListToolCallsByMessageIDs(ctx context.Context, messageIDs []string) ([]*db.ToolCall, error)
	// UpsertToolCall / UpsertToolCallResult let a cancel record its outcome
	// durably at the moment the user asks for it -- see cancelToolCalls.
	UpsertToolCall(ctx context.Context, call *db.ToolCall) error
	UpsertToolCallResult(ctx context.Context, chatID string, result *db.ToolCallResult) error

	// Attachment operations
	GetAttachment(ctx context.Context, id string) (*db.Attachment, error)
	GetAttachmentsByIDs(ctx context.Context, ids []string) ([]*db.Attachment, error)

	// Chat update operations
	CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error

	// SaveMessageAtomic writes a message, its content blocks and its
	// chat_update in one statement, allocating ordinal/seq from counters
	// rather than scanning MAX(). This is the hot write path; see
	// db.SaveMessageAtomic for why it is one statement and why it is safe at
	// READ COMMITTED.
	SaveMessageAtomic(ctx context.Context, w db.AtomicMessageWrite) (*db.AtomicMessageResult, error)

	// SeedSeqCounterForFork raises a chat's seq allocator to cover history
	// inherited through a fork chain, so a branch does not restart at 0 and
	// sort its replies above the transcript it inherited.
	SeedSeqCounterForFork(ctx context.Context, chatID, forkAtContextWindowID string) error

	// Transaction support
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error
}

// TemporalSignaler is the Temporal signal operation needed to wake a thread.
type TemporalSignaler interface {
	SignalWorkflow(ctx context.Context, workflowID, runID, signalName string, signalArg interface{}) error
}

// ToolCanceler is the daemon operation needed to cancel one running tool execution.
type ToolCanceler interface {
	SendToolExecutionCancel(ctx context.Context, userID, requestID, reason string) error
}
