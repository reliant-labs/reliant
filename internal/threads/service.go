package threads

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/chataccess"
	"github.com/reliant-labs/reliant/internal/chatviews"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Service provides centralized thread and context window management.
// All thread creation, forking, compaction, and interruption should go through this service.
type Service struct {
	repo             Repository
	chats            chataccess.Service
	messages         Messages
	senders          Senders
	ambientMemory    AmbientMemory
	temporalSignaler TemporalSignaler
	toolCanceler     ToolCanceler
}

// New builds a Service from deps. Construction permits a nil Repo, but operations
// that access the repository (including CreateWorkflowWithThread) require it.
// The signaler and tool canceler are only needed by InterruptThread and the pause path.
func New(deps Deps) *Service {
	if deps.AmbientMemory == nil {
		if memory, ok := deps.Repo.(AmbientMemory); ok {
			deps.AmbientMemory = memory
		} else if o, ok := deps.Repo.(interface{ ORM() *orm.Client }); ok && o.ORM() != nil {
			deps.AmbientMemory = repoAmbientMemory{deps.Repo}
		}
	}
	return &Service{repo: deps.Repo, chats: deps.Chats, messages: deps.Messages, senders: deps.Senders, ambientMemory: deps.AmbientMemory, temporalSignaler: deps.Signaler, toolCanceler: deps.ToolCanceler}
}

// CreateThreadOpts contains options for creating a new root thread.
type CreateThreadOpts struct {
	ID     string  // Optional - generated if empty
	ChatID string  // Required
	Title  *string // Optional

	// Origin records how the thread was created. Defaults to ThreadOriginMain,
	// since a thread created through this entry point has no parent.
	Origin db.ThreadOrigin
	// OriginNodeID names the creating graph node; only meaningful for
	// ThreadOriginNode.
	OriginNodeID *string
}

// ForkThreadOpts contains options for forking a thread.
type ForkThreadOpts struct {
	ID             string // Optional - generated if empty
	ChatID         string // Required - can differ from parent (cross-chat fork)
	ParentThreadID string // Required
	// ForkAtContextWindowID is the parent thread's context window this fork
	// branches from -- required independent of ForkAtMessageID because the
	// new thread's own context window inherits its Sequence from it, and
	// that holds even when the parent CW has no messages yet.
	ForkAtContextWindowID string
	// ForkAtMessageID is the last parent-thread message this fork inherits.
	// Nil means the parent's context window had no messages at fork time
	// (fork of an empty thread) -- the new thread inherits nothing from it,
	// not everything.
	ForkAtMessageID *string
	Title           *string // Optional
}

// CreateWorkflowWithThreadOpts contains options for creating a workflow and thread atomically.
type CreateWorkflowWithThreadOpts struct {
	// Workflow - all fields needed to create the workflow
	Workflow *db.Workflow

	// Thread
	ThreadID             string  // Empty = generate new ID, exists = reuse/inherit, doesn't exist = create new
	ChatID               string  // Required - the conversation this thread belongs to
	ThreadTitle          *string // Optional human-readable title
	ParentThread         *string // Optional - parent thread ID for non-fork child threads (e.g., spawn)
	ExcludeAmbientMemory bool

	// Origin records how the thread was created — see db.ThreadOrigin. The
	// fork paths override this to ThreadOriginFork, since fork metadata is
	// self-describing. Defaults to ThreadOriginNode when unset.
	Origin db.ThreadOrigin
	// OriginNodeID names the creating graph node; only set for ThreadOriginNode.
	OriginNodeID *string

	// WorktreeID binds the new thread to a workspace other than its chat's.
	// Applies only when the thread is created here; ignored for an inherited
	// (already existing) thread and for forks.
	WorktreeID *string

	// Fork configuration (mutually exclusive - set at most one)
	// ForkFromMessage: Fork from a specific message (extracts thread, CW, ordinal internally)
	// ForkFromThread: Fork from a thread's latest state (uses ordinal 0 to include all messages)
	ForkFromMessage *string // Message ID to fork from
	ForkFromThread  *string // Thread ID to fork from (uses latest CW)
}

// forkOpts contains resolved fork-specific options (internal use only).
type forkOpts struct {
	parentThreadID        string
	forkAtContextWindowID string
	// forkAtMessageID is nil when the fork inherits nothing from its parent
	// (the parent's context window had no messages at fork time).
	forkAtMessageID *string
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
		logging.DebugContext(ctx, "[Fork] CreateWorkflowWithThread resolved fork point",
			"parentThreadID", fork.parentThreadID,
			"forkAtMessageID", fork.forkAtMessageID,
			"forkAtContextWindowID", fork.forkAtContextWindowID,
			"workflowID", opts.Workflow.ID,
			"threadID", opts.ThreadID)
	}

	var resultWorkflow *db.Workflow
	var resultThread *db.Thread
	var resultCW *db.ContextWindow

	// READ COMMITTED, because every invariant this transaction keeps is held
	// by a lock or a single statement, not by SERIALIZABLE's proof:
	//   - the workflow insert is one INSERT … ON CONFLICT DO NOTHING, and its
	//     admission triggers (recovery_workflow_admission, chat_write_admission,
	//     project_delete_admission) are row-lock protocols: the chat row, then
	//     its worktree rows in id order, FOR NO KEY UPDATE, then a bump of each
	//     worktree's activity_generation;
	//   - two creates of one new thread id (a retried activity overlapping
	//     its first attempt) collide on threads_pkey, SQLSTATE 23505, which the
	//     attempt budget below retries; the retry finds the thread and takes
	//     the inherit path;
	//   - an inherited thread's workflow_id is one UPDATE, last writer wins,
	//     as it was when SERIALIZABLE retried the loser;
	//   - the new thread's context window counter is the new thread's own;
	//   - the ambient-memory exclusion is a monotonic latch: a lineage read,
	//     then an UPDATE that only ever sets it, never clears it.
	// SERIALIZABLE made every child a workflow starts at once in one chat (a
	// parallel node, a fan-out of spawns) abort the others. Each admission
	// waits for the chat row lock and then finds the worktree row bumped by
	// the transaction it waited for; SERIALIZABLE refuses to lock a row
	// updated after its snapshot, so every waiter failed with SQLSTATE 40001
	// "could not serialize access due to concurrent update" on every attempt
	// until one ran out of all seven (prod issues 1f232560, 53e2370c). With no
	// worktree bound, the same inserts' predicate locks aborted one another
	// with "read/write dependencies" instead. READ COMMITTED re-reads the row
	// it waited for and goes on
	// (TestCreateWorkflowWithThread_ConcurrentChildrenInOneChatDoNotConflict).
	createTx := orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts)
	err = s.repo.RunTx(ctx, createTx, func(txCtx context.Context) error {
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
		if err != nil && !svcerr.IsNotFound(err) {
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
					ChatID:                opts.ChatID,
					ParentThreadID:        fork.parentThreadID,
					ForkAtContextWindowID: fork.forkAtContextWindowID,
					ForkAtMessageID:       fork.forkAtMessageID,
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
				thread, cw, err = s.createThreadInternal(txCtx, opts.ChatID, threadID, opts.ThreadTitle, opts.ParentThread, &workflowID, origin, opts.OriginNodeID, opts.WorktreeID)
				if err != nil {
					return fmt.Errorf("failed to create thread: %w", err)
				}
			}

			resultThread = thread
			resultCW = cw
		}

		// Commit the runtime policy atomically with thread creation: a crash
		// between creation and the child's first turn must not lose its cap.
		if s.ambientMemory == nil {
			return fmt.Errorf("persist thread memory exclusion: %w", chatviews.ErrNoChatReads)
		}
		excluded, err := s.ambientMemory.ResolveThreadAmbientMemoryExclusion(txCtx, opts.ChatID, resultThread.ID, opts.ExcludeAmbientMemory)
		if err != nil {
			return fmt.Errorf("persist thread memory exclusion: %w", err)
		}
		resultThread.ExcludeAmbientMemory = excluded
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
		// Fork from specific message - extract thread and CW from message
		msg, err := s.repo.GetMessage(ctx, *messageID)
		if err != nil {
			return nil, lookupFailed(err, "fork message not found", "failed to load fork message")
		}
		return &forkOpts{
			parentThreadID:        msg.ThreadID,
			forkAtContextWindowID: msg.ContextWindowID,
			forkAtMessageID:       &msg.ID,
		}, nil
	}

	if threadID != nil {
		// Fork from thread's latest state - inherit everything the thread
		// currently has. The fork message is the thread's latest message
		// (highest seq); if the thread has none yet, forkAtMessageID stays
		// nil and the new thread correctly inherits nothing.
		cw, err := s.repo.GetLatestContextWindow(ctx, *threadID)
		if err != nil {
			return nil, fmt.Errorf("failed to get latest context window for thread: %w", err)
		}

		latestMsg, err := s.repo.GetLatestMessageInThread(ctx, *threadID)
		if err != nil {
			return nil, fmt.Errorf("failed to get latest message for thread: %w", err)
		}

		var forkAtMessageID *string
		if latestMsg != nil {
			forkAtMessageID = &latestMsg.ID
		}

		return &forkOpts{
			parentThreadID:        *threadID,
			forkAtContextWindowID: cw.ID,
			forkAtMessageID:       forkAtMessageID,
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

// repoAmbientMemory is the ambient-memory latch of a repository that has a
// database handle: the generated thread store over it.
type repoAmbientMemory struct{ repo any }

func (r repoAmbientMemory) ResolveThreadAmbientMemoryExclusion(ctx context.Context, chatID, threadID string, requested bool) (bool, error) {
	return chatviews.AmbientMemory(r.repo)(ctx, chatID, threadID, requested)
}
