// forge:deps — consumer-declared interfaces for this package's Deps.
// Repository is this package's own declaration of what the streaming handlers
// read; mock_gen.go mocks it. *db.Repo satisfies it, and the composition root
// hands it to New.
//
// It lists the methods the handlers call directly, then embeds the role of
// every callee they hand the repository to (threads for the snapshot's
// messages, messageproto for its tool calls), so a callee that needs more
// points the compiler here.
package streaming

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/messageproto"
	"github.com/reliant-labs/reliant/internal/threads"
)

// SpawnCalls maps a chat's child threads to the spawn tool calls that started
// them, for a reconnect snapshot's cancel reconciliation. store.ToolCallExtStore
// satisfies it.
type SpawnCalls interface {
	SpawnToolCallIDsByChildThread(ctx context.Context, chatID string) (map[string]string, error)
}

// Heads is a chat's update heads, the snapshot's other-updates: the newest
// non-message update of each entity. The source says where they were read from
// (the heads table, or the log for a chat the heads cannot vouch for).
// store.ChatUpdateExtStore satisfies it.
type Heads interface {
	SnapshotUpdateHeads(ctx context.Context, chatID string) ([]core.ChatUpdate, string, error)
}

// UserLog is a user's update log after a cursor. store.UserUpdateExtStore
// satisfies it.
type UserLog interface {
	UserUpdatesSince(ctx context.Context, userID string, sinceSeq int64, limit int) ([]core.UserUpdate, error)
}

// OrgFeed is an org's feed of what its members shared (StreamOrgUpdates).
// store.OrgUpdateExtStore satisfies it.
type OrgFeed interface {
	LatestOrgUpdateSequence(ctx context.Context, orgID string) (int64, error)
	OrgUpdatesSince(ctx context.Context, orgID string, sinceSeq int64, limit int) ([]core.OrgUpdate, error)
}

// Siblings is the snapshot's rows of threads that are neither the main thread
// nor a spawn thread. store.MessageExtStore satisfies it.
type Siblings interface {
	ListRecentTranscriptSiblingMessages(ctx context.Context, chatID, mainThreadID string, fromSeq int64, limit int) ([]*core.Message, error)
}

// ChatRows reads a chat's own row, who it belongs to and which org. store.ChatExtStore
// satisfies it.
type ChatRows interface {
	GetChatRow(ctx context.Context, id string) (*db.ChatRow, error)
}

// TriggerReads reads the launch event of a given kind that started a chat, to
// walk an agent.start_run chain to its parent. store.TriggerEventExtStore
// satisfies it.
type TriggerReads interface {
	GetTriggerEventByChat(ctx context.Context, kind core.TriggerEventKind, chatID string) (*core.TriggerEvent, error)
}

// Repository is everything the streaming handlers read.
type Repository interface {
	// The chat and user update logs a stream replays and tails.
	GetLatestUpdateSequence(ctx context.Context, chatID string) (int64, error)
	// The compaction watermark: a replay from below it would skip rows.
	GetChatCompactedThrough(ctx context.Context, chatID string) (int64, error)
	GetUpdatesSince(ctx context.Context, chatID string, sinceSeq int64, limit int) ([]db.ChatUpdate, error)
	GetLatestUserUpdateSequence(ctx context.Context, userID string) (int64, error)

	// The org of the saved workflow a user update is about, which
	// decides whether the user stream delivers it (orgGate).
	GetWorkflowDraft(ctx context.Context, id string) (*db.WorkflowDraft, error)

	// What a chat snapshot is assembled from.
	ListThreadsByConversation(ctx context.Context, chatID string) ([]*db.Thread, error)
	CountMessagesInChat(ctx context.Context, chatID string) (int, error)
	ListContentBlocksForMessages(ctx context.Context, messageIDs []string) ([]*db.MessageContentBlock, error)
	ListLiveToolCallsByChat(ctx context.Context, chatID string) ([]*db.ToolCall, error)

	// GetAttachmentsByIDs and GetContextUsage are called directly too; the
	// roles below already declare them.
	threads.Repository
	messageproto.ToolCallReader
}

// Projects reads the org of the project a user update is about (orgGate).
// store.ProjectExtStore satisfies it.
type Projects interface {
	GetProject(ctx context.Context, id string) (*db.Project, error)
}

// Senders titles the senders of the messages a read assembles (a sender that
// is another run's main thread is titled by that run).
// store.ThreadExtStore satisfies it.
type Senders interface {
	SenderTitlesByThreadIDs(ctx context.Context, threadIDs []string) (map[string]string, error)
}
