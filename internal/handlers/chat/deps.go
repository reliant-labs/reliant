// forge:deps — consumer-declared interfaces for this package's Deps.
// Repository is this package's own declaration of what the chat handlers read
// and write; mock_gen.go mocks it. *db.Repo satisfies it, and the composition
// root hands it to New.
//
// It lists the methods the handlers call directly, then embeds the role of
// every callee they hand the repository to (threads, runs, launch, chat
// selection, daemon choice and placement, message assembly), so a callee that
// needs more points the compiler here.
package chat

import (
	"context"

	"github.com/reliant-labs/reliant/internal/chatselection"
	"github.com/reliant-labs/reliant/internal/daemonchoice"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/messageproto"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/threads"
)

// Repository is everything the chat handlers read and write.
type Repository interface {
	// Chat rows: listing and search, delete, and the state, unread,
	// adoption and machine flags the handlers flip.
	ListChats(ctx context.Context, filters db.ChatFilters) ([]*db.Chat, error)
	// Ownership and the chat's own columns, from the chats row alone.
	GetChatRow(ctx context.Context, id string) (*db.ChatRow, error)
	GetChatRowWithUserCheck(ctx context.Context, id, userID string) (*db.ChatRow, error)
	// The chat with its activity-derived fields, for a handler that reads one.
	GetChatWithUserCheck(ctx context.Context, id, userID string) (*db.Chat, error)
	SearchChats(ctx context.Context, filters db.ChatSearchFilters) ([]*db.Chat, error)
	ListArchivedChats(ctx context.Context, userID string) ([]*db.ArchivedChatInfo, error)
	DeleteChat(ctx context.Context, id string) error
	UpdateChatState(ctx context.Context, chatID string, state db.ChatState, reason string) error
	UpdateChatUnread(ctx context.Context, chatID string, unread bool, reason string) error
	SetChatAdopted(ctx context.Context, chatID, userID string, adopted bool) (bool, error)
	SetChatDaemonBlocked(ctx context.Context, chatID string, blocked bool) error
	SetChatPausedForMachine(ctx context.Context, chatID string, paused bool) (bool, error)
	SetChatQueuedForMachine(ctx context.Context, chatID string, queued bool) (bool, error)
	// ListCapacityWakeTargets lists the root runs of a user's chats that may
	// have a thread blocked on a provider (WakeUserChats).
	ListCapacityWakeTargets(ctx context.Context, userID string) ([]string, error)

	// Threads and the workflows that run them.
	ListThreadsByConversation(ctx context.Context, chatID string) ([]*db.Thread, error)
	ReviveThread(ctx context.Context, threadID string) (int64, error)
	ListForkedThreadIDs(ctx context.Context, threadIDs []string) ([]string, error)
	ListInheritedSpawnChildren(ctx context.Context, threadID string) ([]*db.InheritedSpawnChild, error)
	ThreadAwaitsReply(ctx context.Context, threadID string) (bool, error)
	IsLatestTurnInThread(ctx context.Context, threadID, messageID string) (bool, error)
	CountMessagesInThread(ctx context.Context, threadID string) (int, error)
	CountMessagesInChatOutsideThread(ctx context.Context, chatID, threadID string) (int, error)
	ListTranscriptSiblingMessagesInRange(ctx context.Context, chatID, mainThreadID string, fromSeq int64, toSeq *int64, limit int) ([]*db.Message, error)
	GetWorkflowByThread(ctx context.Context, chatID, thread string) (*db.Workflow, error)
	ListWorkflowsByChat(ctx context.Context, chatID string) ([]*db.Workflow, error)

	// Messages, their content blocks, tool calls and open questions.
	ListMessages(ctx context.Context, chatID string, opts db.MessageListOptions) ([]*db.Message, error)
	ListContentBlocksForMessages(ctx context.Context, messageIDs []string) ([]*db.MessageContentBlock, error)
	GetToolCall(ctx context.Context, id string) (*db.ToolCall, error)
	GetToolResultBlock(ctx context.Context, toolCallID string) (*db.MessageContentBlock, error)
	ListToolResultBlocks(ctx context.Context, toolCallID string) ([]*db.MessageContentBlock, error)
	PendingQuestionsByThread(ctx context.Context, chatID string) (map[string]*db.Question, error)

	// The agent mailbox: messages queued for a thread until it can take them.
	EnqueueAgentMessage(ctx context.Context, msg *db.AgentMessage) error
	EnqueueAgentMessageOnce(ctx context.Context, msg *db.AgentMessage) (inserted bool, err error)
	GetAgentMessage(ctx context.Context, id string) (*db.AgentMessage, error)
	CancelQueuedAgentMessage(ctx context.Context, id, chatID string) (cancelled bool, err error)
	DeleteUndeliveredAgentMessage(ctx context.Context, id, chatID string) (deleted bool, err error)
	CountQueuedAgentMessagesForThread(ctx context.Context, toThreadID string) (int64, error)
	ListQueuedAgentMessagesForThread(ctx context.Context, toThreadID string) ([]*db.AgentMessage, error)

	// Plans and their tasks.
	CreatePlan(ctx context.Context, plan *db.Plan) error
	CreateTask(ctx context.Context, task *db.Task) error
	ListPlansByChatID(ctx context.Context, chatID string) ([]*db.Plan, error)
	ListTasksByPlan(ctx context.Context, planID string) ([]*db.Task, error)

	// Step executions the workflow view renders.
	GetStepExecutionsForChat(ctx context.Context, chatID string) ([]*db.ChatStepExecution, error)
	GetBasicStepExecutionsForChat(ctx context.Context, chatID string) ([]*db.ChatStepExecution, error)
	ListStepExecutionRecordsForChat(ctx context.Context, chatID, nodePath string, limit int) ([]*db.StepExecution, error)

	// The chat and user update logs.
	GetLatestUpdateSequence(ctx context.Context, chatID string) (int64, error)
	GetUpdatesSince(ctx context.Context, chatID string, sinceSeq int64, limit int) ([]db.ChatUpdate, error)
	GetLatestWorkflowNodeUpdates(ctx context.Context, chatID, workflowID string) ([]db.ChatUpdate, error)
	GetLatestUserUpdateSequence(ctx context.Context, userID string) (int64, error)

	// Worktrees a chat list resolves and a branch un-archives.
	ListWorktreesByIDs(ctx context.Context, ids []string) ([]*db.Worktree, error)
	ListMainWorktreesForUser(ctx context.Context, userID string, projectID *string) ([]*db.Worktree, error)
	UnarchiveWorktree(ctx context.Context, id string) error

	// The durable state a resumed run is rebuilt from. The callees that
	// read it (runtime.ResumeInputFromDurableState,
	// runtime.StoppedSpawnFromDurableState, launch.LoadChatTrigger) declare
	// unexported or anonymous roles, so their methods are written out here.
	GetWorkflowCheckpoint(ctx context.Context, workflowID string) (*db.WorkflowCheckpoint, error)
	ListLiveBackgroundSpawns(ctx context.Context, rootWorkflowID string) ([]*db.LiveBackgroundSpawn, error)
	ListToolGrants(ctx context.Context, chatID string) ([]*db.ToolGrant, error)
	GetStoppedSpawnAgent(ctx context.Context, chatID, threadID string) (*db.StoppedSpawnAgent, error)
	GetTriggerEventByChatID(ctx context.Context, chatID string) (*core.TriggerEvent, error)
	// CreateNumberedMessage appends a message the handler builds itself (the
	// branch repair), numbered from the counters.
	CreateNumberedMessage(ctx context.Context, msg *db.Message) error

	// GetChat, CreateChat, UpdateChat, GetThread, SaveMessageToThread,
	// GetWorkflow, RunTx and the rest of the 74 methods the handlers call
	// directly are declared by the roles below.
	threads.Repository
	runs.Repository
	launch.Store
	chatselection.Store
	daemonchoice.Reader
	daemonplacement.WorktreeReader
	daemonplacement.PinStore
	messageproto.Reader
}
