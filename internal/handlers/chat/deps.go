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
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/daemonchoice"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/messageproto"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/threads"
)

// OrgResolver answers the org questions of chat rpcs. *orgs.Resolver is one.
type OrgResolver interface {
	Named(ctx context.Context, userID, requested string) (string, error)
	DefaultOrg(ctx context.Context, userID string) (string, error)
	ListScope(ctx context.Context, userID, requested string, stamp orgs.Backfill) (*core.OrgScope, error)
}

// Questions lists a chat's open questions: by thread for a send that answers
// one, and chat-wide for the run lifecycle's terminate. store.QuestionExtStore
// satisfies it.
type Questions interface {
	PendingQuestionsByThread(ctx context.Context, chatID string) (map[string]*db.Question, error)
	ListPendingQuestionsByChat(ctx context.Context, chatID string) ([]*db.Question, error)
}

// Messages reads one message by id and a thread's newest turn: the send's
// idempotency check asks whether a client message id is already taken and
// whether anything answered it. store.MessageExtStore satisfies it.
type Messages interface {
	FindMessage(ctx context.Context, id string) (*core.Message, error)
	LatestTurnInThread(ctx context.Context, threadID string) (*core.Message, error)
	// ClaimUserActivation records a message as the user's activation (their
	// first typed message ever) once, durably, and reports whether this call
	// did (user_first_message).
	ClaimUserActivation(ctx context.Context, userID, chatID, messageID string) (bool, error)
}

// ToolBlocks reads tool-call and tool-result content blocks by tool call id.
// store.MessageContentBlockExtStore satisfies it.
type ToolBlocks interface {
	FindToolBlock(ctx context.Context, toolCallID string, blockType reliantv1.ContentBlockType) (*core.MessageContentBlock, error)
	ListToolBlocks(ctx context.Context, toolCallID string, blockType reliantv1.ContentBlockType) ([]*core.MessageContentBlock, error)
}

// Spawns reads the spawn tree a branch inherits and a resumed run is rebuilt
// from: the sub-agents a branch's chat inherits, the background spawns still
// open in a root execution, and a sub-agent that stopped on a failure.
// store.ToolCallExtStore satisfies it. Each takes the chat, thread or workflow
// id the handler already authorized.
type Spawns interface {
	ListInheritedSpawnChildren(ctx context.Context, threadID string) ([]*core.InheritedSpawnChild, error)
	ListLiveBackgroundSpawns(ctx context.Context, rootWorkflowID string) ([]*core.LiveBackgroundSpawn, error)
	GetStoppedSpawnAgent(ctx context.Context, chatID, threadID string) (*core.StoppedSpawnAgent, error)
}

// durableState is what a resumed run is rebuilt from: the repository's durable
// rows and the spawn tree. It is the one value handed to the runtime's
// *FromDurableState helpers, which declare their own roles over it.
type durableState struct {
	Repository
	Spawns
}

// TriggerReads reads the launch event of a chat, which a restart rebuilds its
// WorkflowInput from (launch.LoadChatTrigger). store.TriggerEventExtStore
// satisfies it.
type TriggerReads interface {
	GetTriggerEventByChatID(ctx context.Context, chatID string) (*core.TriggerEvent, error)
}

// Repository is everything the chat handlers read and write.
type Repository interface {
	// AssignUnassignedOrg stamps the caller's projects and chats that have
	// no org with their default org (the lazy backfill, orgs.ListScope).
	AssignUnassignedOrg(ctx context.Context, userID, orgID string) error
	// Chat rows: listing and search, delete, and the state, unread,
	// adoption and machine flags the handlers flip.
	ListChats(ctx context.Context, filters db.ChatFilters) ([]*db.Chat, error)
	SearchChats(ctx context.Context, filters db.ChatSearchFilters) ([]*db.Chat, error)
	ListArchivedChats(ctx context.Context, userID string, org *core.OrgScope, page db.ArchivedChatPage) ([]*db.ArchivedChatInfo, bool, error)
	DeleteChat(ctx context.Context, id string) error
	UpdateChatState(ctx context.Context, chatID string, state db.ChatState, reason string) error
	UpdateChatUnread(ctx context.Context, chatID string, unread bool, reason string) error
	SetChatAdopted(ctx context.Context, chatID, userID string, adopted bool) (bool, error)
	// RecordChatPersonTurn marks a launched chat attended from a person's send on.
	RecordChatPersonTurn(ctx context.Context, chatID, userID string) error
	SetChatDaemonBlocked(ctx context.Context, chatID string, blocked bool) error
	SetChatPausedForMachine(ctx context.Context, chatID string, paused bool) (bool, error)
	SetChatQueuedForMachine(ctx context.Context, chatID string, queued bool) (bool, error)
	// MoveChatToProject moves an idle chat, with its plans, to another project.
	MoveChatToProject(ctx context.Context, m db.ChatMove) (bool, error)
	// The projects attached to a chat beside its primary (chat_projects).
	ListChatProjects(ctx context.Context, chatID string) ([]*db.ChatProject, error)
	AttachChatProject(ctx context.Context, cp *db.ChatProject) error
	DetachChatProject(ctx context.Context, chatID, projectID string) error
	CopyChatProjects(ctx context.Context, fromChatID, toChatID string) error

	// Threads and the workflows that run them.
	ListThreadsByConversation(ctx context.Context, chatID string) ([]*db.Thread, error)
	ReviveThread(ctx context.Context, threadID string) (int64, error)
	ListForkedThreadIDs(ctx context.Context, threadIDs []string) ([]string, error)
	ThreadAwaitsReply(ctx context.Context, threadID string) (bool, error)
	CountMessagesInThread(ctx context.Context, threadID string) (int, error)
	ListTranscriptSiblingMessagesInRange(ctx context.Context, chatID, mainThreadID string, fromSeq int64, toSeq *int64, limit int) ([]*db.Message, error)
	GetWorkflowByThread(ctx context.Context, chatID, thread string) (*db.Workflow, error)
	ListWorkflowsByChat(ctx context.Context, chatID string) ([]*db.Workflow, error)

	// Messages, their content blocks, tool calls and open questions.
	ListMessages(ctx context.Context, chatID string, opts db.MessageListOptions) ([]*db.Message, error)
	ListContentBlocksForMessages(ctx context.Context, messageIDs []string) ([]*db.MessageContentBlock, error)
	GetToolCall(ctx context.Context, id string) (*db.ToolCall, error)
	ListContentBlocksByIDs(ctx context.Context, ids []string) ([]*db.MessageContentBlock, error)
	ListMessagesByIDs(ctx context.Context, ids []string) ([]*db.Message, error)

	// Plans and their tasks.
	CreatePlan(ctx context.Context, plan *db.Plan) error
	CreateTask(ctx context.Context, task *db.Task) error
	ListPlansByChatID(ctx context.Context, chatID string) ([]*db.Plan, error)
	ListTasksByPlan(ctx context.Context, planID string) ([]*db.Task, error)
	// CopyChatPlans gives toThreadID a pending copy of every plan of
	// fromChatID, with their tasks, in one statement (BranchChat).
	CopyChatPlans(ctx context.Context, fromChatID, toThreadID string) error

	// Step executions the workflow view renders.
	GetStepExecutionsForChat(ctx context.Context, chatID string) ([]*db.ChatStepExecution, error)
	GetBasicStepExecutionsForChat(ctx context.Context, chatID string) ([]*db.ChatStepExecution, error)
	ListStepExecutionRecordsForChat(ctx context.Context, chatID, nodePath string, limit int) ([]*db.StepExecution, error)

	// The chat and user update logs.
	GetLatestUpdateSequence(ctx context.Context, chatID string) (int64, error)
	// The compaction watermark: at or below it the log keeps only each
	// entity's newest update.
	GetChatCompactedThrough(ctx context.Context, chatID string) (int64, error)
	GetUpdatesSince(ctx context.Context, chatID string, sinceSeq int64, limit int) ([]db.ChatUpdate, error)
	ListErrorUpdatesInRange(ctx context.Context, chatID string, from *time.Time, thread string, beforeSeq int64) ([]db.ChatUpdate, error)
	GetLatestWorkflowNodeUpdates(ctx context.Context, chatID, workflowID string) ([]db.ChatUpdate, error)
	GetLatestUserUpdateSequence(ctx context.Context, userID string) (int64, error)

	// Worktrees a chat list resolves and a branch un-archives.
	ListWorktreesByIDs(ctx context.Context, ids []string) ([]*db.Worktree, error)
	ListMainWorktreesForUser(ctx context.Context, userID string, projects []string) ([]*db.Worktree, error)
	// The projects of a chat list's main workspaces, read at once.
	ListProjectsByIDs(ctx context.Context, ids []string) ([]*db.Project, error)
	UnarchiveWorktree(ctx context.Context, id string) error

	// The durable state a resumed run is rebuilt from. The callees that
	// read it (runtime.ResumeInputFromDurableState,
	// runtime.StoppedSpawnFromDurableState, launch.LoadChatTrigger) declare
	// unexported or anonymous roles, so their methods are written out here.
	GetWorkflowCheckpoint(ctx context.Context, workflowID string) (*db.WorkflowCheckpoint, error)
	ListToolGrants(ctx context.Context, chatID string) ([]*db.ToolGrant, error)
	// CreateNumberedMessage appends a message the handler builds itself (the
	// branch repair), numbered from the counters.
	CreateNumberedMessage(ctx context.Context, msg *db.Message) error

	// GetChat, CreateChat, UpdateChat, GetThread, SaveMessageToThread,
	// GetWorkflow, RunTx and the rest of the 74 methods the handlers call
	// directly are declared by the roles below.
	threads.Repository
	runs.Repository
	launch.Store
	// GetChat is the chat read launch.Store and runs.Repository no longer
	// carry; the handlers read it through ChatViews.
	// GetProjectWithUserCheck is the org-aware project read the handlers and the
	// launcher share; db.Repo delegates it to projectaccess.
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*db.Project, error)
	// GetProject reads a project the handler already authorized (the
	// workspace-machine and move-to-project paths).
	GetProject(ctx context.Context, id string) (*db.Project, error)
	daemonchoice.Reader
	daemonplacement.WorktreeReader
	daemonplacement.PinStore
	messageproto.ToolCallReader
}

// Senders titles the senders of the messages a read assembles (a sender that
// is another run's main thread is titled by that run).
// store.ThreadExtStore satisfies it.
type Senders interface {
	SenderTitlesByThreadIDs(ctx context.Context, threadIDs []string) (map[string]string, error)
}

// Mailbox queues the messages a send puts in a thread's mailbox and announces
// each one to the chat's open clients. *agentmailbox.Mailbox satisfies it.
type Mailbox interface {
	EnqueueAgentMessage(ctx context.Context, msg *db.AgentMessage) error
	EnqueueAgentMessageOnce(ctx context.Context, msg *db.AgentMessage) (inserted bool, err error)
}

// AgentMessages reads and removes mailbox rows: the idempotency checks of a
// repeated send, the cancel of a queued message, and the queue depth a send
// gates on. store.AgentMessageExtStore satisfies it.
type AgentMessages interface {
	GetAgentMessage(ctx context.Context, id string) (*db.AgentMessage, error)
	CancelQueuedAgentMessage(ctx context.Context, id, chatID string) (cancelled bool, err error)
	DeleteUndeliveredAgentMessage(ctx context.Context, id, chatID string) (deleted bool, err error)
	CountQueuedAgentMessagesForThread(ctx context.Context, toThreadID string) (int64, error)
	ListQueuedAgentMessagesForThread(ctx context.Context, toThreadID string) ([]*db.AgentMessage, error)
}

// ChatRows reads a chat's own row from the chats table alone: its ownership
// and plain columns. store.ChatExtStore satisfies it.
type ChatRows interface {
	GetChatRow(ctx context.Context, id string) (*db.ChatRow, error)
}

// ChatViews reads a chat with its computed state (activity, main thread, run).
// store.ChatsWithActivityExtStore satisfies it.
type ChatViews interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
}

// QuestionResolver resolves a question and announces the chat's activity.
// *questions.Service satisfies it.
type QuestionResolver interface {
	ResolveQuestion(ctx context.Context, id string, responseData *string) error
}
