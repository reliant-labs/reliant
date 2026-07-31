package threads

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Repository defines the subset of db.Repository needed by the threads service.
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

	// Message operations (for SaveMessage)
	CreateMessage(ctx context.Context, msg *db.Message) error
	DeleteMessage(ctx context.Context, messageID string) error
	GetMessageByActivityID(ctx context.Context, chatID, activityID string) (*db.Message, error)
	GetMessageByWorkflowAndActivityID(ctx context.Context, chatID, workflowID, activityID string) (*db.Message, error)
	GetEffectiveMessageCount(ctx context.Context, chatID, threadID string) (int, error)
	GetNextOrdinal(ctx context.Context, threadID string) (int64, error)
	GetContextUsage(ctx context.Context, chatID, threadID string) (*db.ContextUsage, error)

	// Content block operations
	CreateContentBlock(ctx context.Context, block *db.MessageContentBlock) error
	ListContentBlocks(ctx context.Context, messageID string) ([]*db.MessageContentBlock, error)

	// Attachment operations
	GetAttachment(ctx context.Context, id string) (*db.Attachment, error)
	GetAttachmentsByIDs(ctx context.Context, ids []string) ([]*db.Attachment, error)

	// Chat update operations
	CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error

	// Transaction support
	RunTx(ctx context.Context, f func(ctx context.Context) error) error
}

// Service provides centralized thread and context window management.
// All thread creation, forking, and compaction should go through this service.
type Service struct {
	repo Repository
}

// NewService creates a new threads.Service with the given repository.
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// CreateThreadOpts contains options for creating a new root thread.
type CreateThreadOpts struct {
	ID             string  // Optional - generated if empty
	ConversationID string  // Required
	Title          *string // Optional

	// Origin records how the thread was created. Defaults to ThreadOriginMain,
	// since a thread created through this entry point has no parent.
	Origin db.ThreadOrigin
	// OriginNodeID names the creating graph node; only meaningful for
	// ThreadOriginNode.
	OriginNodeID *string
}

// ForkThreadOpts contains options for forking a thread.
type ForkThreadOpts struct {
	ID                    string  // Optional - generated if empty
	ConversationID        string  // Required - can differ from parent (cross-chat fork)
	ParentThreadID        string  // Required
	ForkAtOrdinal         int64   // Required
	ForkAtContextWindowID string  // Required
	Title                 *string // Optional
}

// CreateWorkflowWithThreadOpts contains options for creating a workflow and thread atomically.
type CreateWorkflowWithThreadOpts struct {
	// Workflow - all fields needed to create the workflow
	Workflow *db.Workflow

	// Thread
	ThreadID     string  // Empty = generate new ID, exists = reuse/inherit, doesn't exist = create new
	ChatID       string  // Required - the conversation this thread belongs to
	ThreadTitle  *string // Optional human-readable title
	ParentThread *string // Optional - parent thread ID for non-fork child threads (e.g., spawn)

	// Origin records how the thread was created — see db.ThreadOrigin. The
	// fork paths override this to ThreadOriginFork, since fork metadata is
	// self-describing. Defaults to ThreadOriginNode when unset.
	Origin db.ThreadOrigin
	// OriginNodeID names the creating graph node; only set for ThreadOriginNode.
	OriginNodeID *string

	// Fork configuration (mutually exclusive - set at most one)
	// ForkFromMessage: Fork from a specific message (extracts thread, CW, ordinal internally)
	// ForkFromThread: Fork from a thread's latest state (uses ordinal 0 to include all messages)
	ForkFromMessage *string // Message ID to fork from
	ForkFromThread  *string // Thread ID to fork from (uses latest CW)
}

// forkOpts contains resolved fork-specific options (internal use only).
type forkOpts struct {
	parentThreadID        string
	forkAtOrdinal         int64
	forkAtContextWindowID string
}

// ResolveMessagesOpts contains options for resolving messages.
type ResolveMessagesOpts struct {
	ThreadID            string
	ContextWindowID     *string // Optional - uses latest if nil
	IncludeAllSequences bool    // Include messages from all context sequences
	MaxOrdinal          *int64  // Optional - filter to messages <= this ordinal
}

// ContextUsage contains context usage information.
type ContextUsage struct {
	ThreadID            string `json:"thread_id"`
	ContextSequence     int    `json:"context_sequence"`
	ThreadTokenCount    int64  `json:"thread_token_count"`
	CompactionThreshold int64  `json:"compaction_threshold"`
}

// CreateWorkflowWithThread creates a workflow and thread atomically in a transaction.
// This prevents FK constraint violations where a thread references a non-existent workflow.
//
// Behavior:
// 1. Creates workflow FIRST (inside transaction)
// 2. If ThreadID exists in DB → updates existing thread's workflow_id (inherited)
// 3. If ForkFromMessage/ForkFromThread provided → creates forked thread with workflow_id
// 4. Otherwise → creates new thread with workflow_id
//
// Returns the created/updated workflow, thread, and context window.
func (s *Service) CreateWorkflowWithThread(ctx context.Context, opts CreateWorkflowWithThreadOpts) (*db.Workflow, *db.Thread, *db.ContextWindow, error) {
	if opts.Workflow == nil {
		return nil, nil, nil, fmt.Errorf("workflow is required")
	}
	if opts.ChatID == "" {
		return nil, nil, nil, fmt.Errorf("chat ID is required")
	}
	if opts.ForkFromMessage != nil && opts.ForkFromThread != nil {
		return nil, nil, nil, fmt.Errorf("ForkFromMessage and ForkFromThread are mutually exclusive")
	}

	// Resolve fork point before transaction (read-only operation)
	var fork *forkOpts
	var err error
	if opts.ForkFromMessage != nil || opts.ForkFromThread != nil {
		fork, err = s.resolveForkPoint(ctx, opts.ForkFromMessage, opts.ForkFromThread)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to resolve fork point: %w", err)
		}
	}

	// FORK-DEBUG: Log resolved fork point
	if fork != nil {
		logging.Info("[FORK-DEBUG] CreateWorkflowWithThread resolved fork point",
			"parentThreadID", fork.parentThreadID,
			"forkAtOrdinal", fork.forkAtOrdinal,
			"forkAtContextWindowID", fork.forkAtContextWindowID,
			"workflowID", opts.Workflow.ID,
			"threadID", opts.ThreadID)
	}

	var resultWorkflow *db.Workflow
	var resultThread *db.Thread
	var resultCW *db.ContextWindow

	err = s.repo.RunTx(ctx, func(txCtx context.Context) error {
		// Step 1: Create workflow first (if it doesn't exist).
		// For inline workflows (workflow nodes that execute within parent), the
		// workflow ID is the same as the parent, so it already exists. CreateWorkflow
		// is idempotent (INSERT ... ON CONFLICT DO NOTHING), so a pre-existing ID is a
		// no-op rather than an error. This matters on Postgres: a real duplicate-key
		// violation would abort the whole transaction (SQLSTATE 25P02) and make every
		// later statement in this tx fail.
		if err := s.repo.CreateWorkflow(txCtx, opts.Workflow); err != nil {
			return fmt.Errorf("failed to create workflow: %w", err)
		}
		resultWorkflow = opts.Workflow

		// Step 2: Handle thread creation/update
		threadID := generateID(opts.ThreadID)
		workflowID := opts.Workflow.ID

		// Check if thread already exists (inherited case). Only a genuine
		// not-found may fall through to the create branch: any other error
		// (serialization conflict, aborted tx, connection loss) must abort
		// the closure so RunTx can retry the whole transaction — continuing
		// here would run the create path on a poisoned transaction and
		// surface a misleading 25P02 from whatever statement runs next.
		_, err := s.repo.GetThread(txCtx, threadID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("failed to check for existing thread: %w", err)
		}
		if err == nil {
			// Thread exists - update its workflow_id (inherited thread)
			updatedThread, err := s.repo.UpdateThreadWorkflow(txCtx, threadID, workflowID)
			if err != nil {
				return fmt.Errorf("failed to update thread workflow: %w", err)
			}
			resultThread = updatedThread

			// Get latest context window for inherited thread
			cw, err := s.repo.GetLatestContextWindow(txCtx, threadID)
			if err != nil {
				return fmt.Errorf("failed to get context window for inherited thread: %w", err)
			}
			resultCW = cw
		} else {
			// Thread doesn't exist - create it
			var thread *db.Thread
			var cw *db.ContextWindow

			if fork != nil {
				// Create forked thread
				thread, cw, err = s.forkThreadInternal(txCtx, ForkThreadOpts{
					ID:                    threadID,
					ConversationID:        opts.ChatID,
					ParentThreadID:        fork.parentThreadID,
					ForkAtOrdinal:         fork.forkAtOrdinal,
					ForkAtContextWindowID: fork.forkAtContextWindowID,
					Title:                 opts.ThreadTitle,
				}, &workflowID)
				if err != nil {
					return fmt.Errorf("failed to create forked thread: %w", err)
				}
			} else {
				// Create new thread — set parent if this is a child thread (e.g., spawn)
				origin := opts.Origin
				if origin == "" {
					origin = db.ThreadOriginNode
				}
				thread, cw, err = s.createThreadInternal(txCtx, opts.ChatID, threadID, opts.ThreadTitle, opts.ParentThread, &workflowID, origin, opts.OriginNodeID)
				if err != nil {
					return fmt.Errorf("failed to create thread: %w", err)
				}
			}

			resultThread = thread
			resultCW = cw
		}

		return nil
	})

	if err != nil {
		return nil, nil, nil, err
	}

	return resultWorkflow, resultThread, resultCW, nil
}

// resolveForkPoint resolves fork parameters from either a message ID or thread ID.
// This is internal - callers use ForkFromMessage or ForkFromThread in opts.
func (s *Service) resolveForkPoint(ctx context.Context, messageID, threadID *string) (*forkOpts, error) {
	if messageID != nil {
		// Fork from specific message - extract thread, CW, and ordinal from message
		msg, err := s.repo.GetMessage(ctx, *messageID)
		if err != nil {
			return nil, fmt.Errorf("fork message not found: %w", err)
		}
		return &forkOpts{
			parentThreadID:        msg.ThreadID,
			forkAtOrdinal:         msg.Ordinal,
			forkAtContextWindowID: msg.ContextWindowID,
		}, nil
	}

	if threadID != nil {
		// Fork from thread's latest state - get latest CW and max ordinal to include all
		cw, err := s.repo.GetLatestContextWindow(ctx, *threadID)
		if err != nil {
			return nil, fmt.Errorf("failed to get latest context window for thread: %w", err)
		}

		// Get next ordinal for the thread, then subtract 1 to get max existing ordinal
		nextOrdinal, err := s.repo.GetNextOrdinal(ctx, *threadID)
		if err != nil {
			return nil, fmt.Errorf("failed to get next ordinal: %w", err)
		}
		maxOrdinal := nextOrdinal - 1

		// Use max ordinal to include all parent messages
		// If thread is empty (nextOrdinal=0), maxOrdinal=-1 will include nothing (correct)
		return &forkOpts{
			parentThreadID:        *threadID,
			forkAtOrdinal:         maxOrdinal,
			forkAtContextWindowID: cw.ID,
		}, nil
	}

	return nil, nil
}

// generateID generates a new UUID if the given id is empty.
func generateID(id string) string {
	if id == "" {
		return uuid.New().String()
	}
	return id
}

// contextWindowID generates a deterministic context window ID.
// Format: {chatID}:{threadID}:{sequence}
func contextWindowID(chatID, threadID string, sequence int) string {
	return fmt.Sprintf("%s:%s:%d", chatID, threadID, sequence)
}

// now returns the current UTC time (extracted for testing).
var now = func() time.Time {
	return time.Now().UTC()
}
