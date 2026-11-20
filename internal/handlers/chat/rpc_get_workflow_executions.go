package chat

import (
	"context"
	"sort"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/chatproto"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// GetWorkflowExecutions returns the workflow execution tree for a chat
func (s *Service) GetWorkflowExecutions(
	ctx context.Context,
	req *reliantv1.GetWorkflowExecutionsRequest,
) (*reliantv1.GetWorkflowExecutionsResponse, error) {
	userID := auth.MustGetUserID(ctx)
	// Serves viewers of a shared chat, without what its steps produced
	// (chatproto.ElideWorkflowsForViewer).
	ctx = authz.WithViewerReads(ctx)

	if req.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// The owner, or a viewer of a shared chat.
	chat, err := s.deps.Repo.GetChatRow(ctx, req.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	access, denied := authz.ReadChatAccess(ctx, userID, chat)
	if denied != nil {
		return nil, svcerr.NotFound("chat")
	}

	// Get all workflows for this chat
	workflows, err := s.deps.Repo.ListWorkflowsByChat(ctx, req.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list workflows"))
	}

	if len(workflows) == 0 {
		return &reliantv1.GetWorkflowExecutionsResponse{}, nil
	}

	// Every step of every workflow of this chat, in ONE query.
	//
	// This was a loop calling GetStepExecutionsByWorkflow per workflow — 83
	// serial round trips for the worst real chat, each SELECT * including the
	// TOASTed output_json. Measured: 2.61s of SQL alone against a pool of 8
	// connections, so one sidebar refetch could starve every other RPC. The
	// single query is ~42ms and reads no output_json at all.
	//
	// A failure is no longer per-workflow recoverable, and should not be: the
	// old loop's `continue` meant a transient error silently returned a tree
	// with a workflow's steps missing, which renders as activity that never
	// happened. One query either answers or it does not.
	//
	// Which query depends on the view. BASIC (the default) is what the chat
	// timeline renders: user-facing steps plus the "-save" siblings that
	// recorded a message — 6 of 93,568 rows on the worst real chat, ~6ms
	// against ~1s. FULL is every step, for the workflow viewer and the CLI.
	// Either way it is ONE chat-scoped query.
	var steps []*db.ChatStepExecution
	if req.View == reliantv1.WorkflowExecutionView_WORKFLOW_EXECUTION_VIEW_FULL {
		steps, err = s.deps.Repo.GetStepExecutionsForChat(ctx, req.ChatId)
	} else {
		steps, err = s.deps.Repo.GetBasicStepExecutionsForChat(ctx, req.ChatId)
	}
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get step executions"))
	}
	stepsByWorkflow := make(map[string][]*db.ChatStepExecution)
	for _, step := range steps {
		stepsByWorkflow[step.WorkflowID] = append(stepsByWorkflow[step.WorkflowID], step)
	}

	// Build workflow map for tree construction
	workflowMap := make(map[string]*db.Workflow)
	for _, wf := range workflows {
		workflowMap[wf.ID] = wf
	}

	// Find root workflows (no parent or parent is chat itself)
	var roots []*db.Workflow
	for _, wf := range workflows {
		if wf.ParentID == nil {
			roots = append(roots, wf)
		}
	}

	if len(roots) == 0 {
		return &reliantv1.GetWorkflowExecutionsResponse{}, nil
	}

	// Sort roots by created_at descending (newest first)
	sort.Slice(roots, func(i, j int) bool {
		return roots[i].CreatedAt.After(roots[j].CreatedAt)
	})

	// Thread identity is read once for the whole chat. The tree used to call
	// GetThread (and, for child threads, GetContextWindowBySequence) once per
	// workflow with context.Background(): 199 workflows, ~400 serial queries
	// for the worst real chat, unattached to the request's cancellation.
	// attachThreadsWithoutWorkflowRow needs the same list.
	threads, threadsErr := s.deps.Repo.ListThreadsByConversation(ctx, req.ChatId)
	if threadsErr != nil {
		// Degrade to a tree with no thread metadata rather than failing the
		// read: a partial timeline beats an error page. Logged loudly because
		// the symptom (spawned threads rendering inline) is otherwise
		// unattributable; it explains every missing thread, so the tree does
		// not report them again.
		logging.ErrorContext(ctx, "[WorkflowTree] Failed to list threads; thread origin will be empty and threads without a workflow row will be missing from the timeline",
			"error", threadsErr, "chatID", req.ChatId)
	}
	threadsByID := make(map[string]*db.Thread, len(threads))
	var childThreadIDs []string
	for _, thread := range threads {
		if thread == nil {
			continue
		}
		threadsByID[thread.ID] = thread
		if thread.ParentThreadID != nil {
			childThreadIDs = append(childThreadIDs, thread.ID)
		}
	}
	// Which child threads are forks (as opposed to spawns): one batched query
	// instead of one per workflow.
	forkedThreads := make(map[string]bool)
	if len(childThreadIDs) > 0 {
		forkedIDs, err := s.deps.Repo.ListForkedThreadIDs(ctx, childThreadIDs)
		if err != nil {
			logging.ErrorContext(ctx, "[WorkflowTree] Failed to resolve forked threads; ForkedFromThread will be empty",
				"error", err, "chatID", req.ChatId)
		}
		for _, id := range forkedIDs {
			forkedThreads[id] = true
		}
	}

	// Index children by parent so the walk is linear, not a scan of every
	// workflow per node.
	childrenByParent := make(map[string][]*db.Workflow)
	for _, wf := range workflows {
		if wf.ParentID != nil {
			childrenByParent[*wf.ParentID] = append(childrenByParent[*wf.ParentID], wf)
		}
	}
	tree := &workflowTreeBuilder{
		threadsByID:      threadsByID,
		forkedThreads:    forkedThreads,
		childrenByParent: childrenByParent,
		stepsByWorkflow:  stepsByWorkflow,
	}

	// Build tree for each root workflow
	allRootProtos := make([]*reliantv1.WorkflowExecution, 0, len(roots))
	for _, root := range roots {
		allRootProtos = append(allRootProtos, tree.build(root))
	}
	tree.reportMissingThreads(ctx, req.ChatId, threadsErr)

	// The tree above can only describe threads that a workflow row points at,
	// and that is strictly fewer than the threads which exist. Fill the gap
	// from the table that owns thread identity.
	if threadsErr == nil {
		s.attachThreadsWithoutWorkflowRow(threads, allRootProtos)
	}
	if access.Viewer() {
		chatproto.ElideWorkflowsForViewer(allRootProtos)
	}

	return &reliantv1.GetWorkflowExecutionsResponse{
		AllRootWorkflows: allRootProtos,
	}, nil
}

// attachThreadsWithoutWorkflowRow adds a node for every thread in the chat that
// the workflow tree does not already describe.
//
// A `workflows` row carries ONE `thread`, but a thread does not always get a row
// of its own. An INLINE sub-workflow node reuses its parent's workflow ID (see
// inline_workflow_executor.go, "Inline workflows use parent's workflow ID"), and
// CreateWorkflow is ON CONFLICT (id) DO NOTHING — so the second thread under
// that ID silently gets no row, and the existing row keeps pointing at the
// parent's thread. Nothing is wrong with the thread itself: it is created
// correctly, with a NOT NULL origin. Only the projection is missing.
//
// The cost of that gap was total rather than cosmetic. InterleavedTimeline skips
// any thread it cannot classify, so every message on such a thread disappeared
// from the UI — measured on a real database, 185 threads holding real messages
// across 55 chats, including one where the invisible thread held 230 of the
// chat's 233 messages. Spawned sub-agents were unaffected (they get genuine
// child workflow rows), which is what made this read as "forks vanish" rather
// than as a general breakage.
//
// Every field here is READ from the thread row; nothing is inferred. That
// matters most for origin: an earlier fallback guessed it, could not produce
// "spawn", and so relabelled spawned sub-agents as node threads and dumped their
// whole transcripts inline into the parent chat.
func (s *Service) attachThreadsWithoutWorkflowRow(
	threads []*db.Thread,
	roots []*reliantv1.WorkflowExecution,
) {
	if len(roots) == 0 {
		return
	}

	// Index the nodes already in the tree, and remember which root each thread
	// belongs to so a synthesized node is attached under the same root.
	nodesByThread := make(map[string]*reliantv1.WorkflowExecution)
	rootByThread := make(map[string]*reliantv1.WorkflowExecution)
	var indexTree func(wf, root *reliantv1.WorkflowExecution)
	indexTree = func(wf, root *reliantv1.WorkflowExecution) {
		if _, seen := nodesByThread[wf.Thread]; !seen {
			nodesByThread[wf.Thread] = wf
			rootByThread[wf.Thread] = root
		}
		for _, child := range wf.Children {
			indexTree(child, root)
		}
	}
	for _, root := range roots {
		indexTree(root, root)
	}

	// Parent-before-child, so a synthesized node can attach to a parent that is
	// itself synthesized. Threads are created before their children, so
	// creation order is a valid topological order.
	missing := make([]*db.Thread, 0, len(threads))
	for _, thread := range threads {
		if thread == nil {
			continue
		}
		if _, exists := nodesByThread[thread.ID]; exists {
			continue
		}
		missing = append(missing, thread)
	}
	sort.SliceStable(missing, func(i, j int) bool {
		return missing[i].CreatedAt.Before(missing[j].CreatedAt)
	})

	for _, thread := range missing {
		// The thread's own workflow_id names the run it executed under, which
		// is what the UI needs to group handoffs. It is the parent's ID for an
		// inline fork — correct, and the reason no row of its own exists.
		workflowID := ""
		if thread.WorkflowID != nil {
			workflowID = *thread.WorkflowID
		}

		state, stopReason := threadStatusToWorkflowStatus(thread.Status)
		node := &reliantv1.WorkflowExecution{
			Id:         workflowID,
			Thread:     thread.ID,
			CreatedAt:  thread.CreatedAt.Format(time.RFC3339),
			Origin:     string(thread.Origin),
			State:      state,
			StopReason: stopReason,
		}
		if thread.Title != nil {
			node.ThreadTitle = thread.Title
		}
		if thread.ParentThreadID != nil {
			node.ParentThread = thread.ParentThreadID
		}
		if thread.OriginNodeID != nil {
			node.OriginNodeId = thread.OriginNodeID
		}
		if thread.CompletedAt != nil {
			completedAt := thread.CompletedAt.Format(time.RFC3339)
			node.CompletedAt = &completedAt
		}
		// A fork inherits its parent's context; ForkedFromThread is what the UI
		// reads to say so. Only forks may claim it — for a spawn or node thread
		// the parent link is provenance, not context inheritance.
		if thread.Origin == db.ThreadOriginFork && thread.ParentThreadID != nil {
			node.ForkedFromThread = thread.ParentThreadID
		}
		// Name the workflow after the run it executed under, so a synthesized
		// node reads the same as a real one in the UI's handoff labels.
		if owner, ok := nodesByThread[thread.ID]; ok && owner != nil {
			node.WorkflowName = owner.WorkflowName
		} else if workflowID != "" {
			if owner, ok := nodesByThread[workflowID]; ok && owner != nil {
				node.WorkflowName = owner.WorkflowName
			}
		}

		// Attach under the parent thread when it is known, otherwise under the
		// root that owns this thread's workflow. A thread whose parent is in
		// another root (or absent) still has to appear somewhere, so the newest
		// root is the last resort — dropping it would reintroduce this bug.
		parent := roots[0]
		if thread.ParentThreadID != nil {
			if parentNode, ok := nodesByThread[*thread.ParentThreadID]; ok {
				parent = parentNode
			}
		} else if workflowID != "" {
			if ownerRoot, ok := rootByThread[workflowID]; ok {
				parent = ownerRoot
			}
		}
		parent.Children = append(parent.Children, node)

		nodesByThread[thread.ID] = node
		if root, ok := rootByThread[parent.Thread]; ok {
			rootByThread[thread.ID] = root
		} else {
			rootByThread[thread.ID] = parent
		}
	}
}

// threadStatusToWorkflowStatus maps a thread's flat lifecycle column onto the
// (state, stop_reason) pair the UI reads, so a synthesized node reports itself
// the same way a real workflow row does instead of defaulting to "unspecified".
//
// This is the inverse of core.ThreadStatusForStopReason, which projects a
// workflow's stop reason onto its threads. Threads have no PENDING/ACTIVE
// distinction to make, so running maps to ACTIVE.
func threadStatusToWorkflowStatus(status db.ThreadStatus) (reliantv1.WorkflowState, reliantv1.WorkflowStopReason) {
	switch status {
	case db.ThreadStatusRunning:
		return reliantv1.WorkflowState_WORKFLOW_STATE_ACTIVE, reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_UNSPECIFIED
	case db.ThreadStatusCompleted:
		return reliantv1.WorkflowState_WORKFLOW_STATE_STOPPED, reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_COMPLETED
	case db.ThreadStatusFailed:
		return reliantv1.WorkflowState_WORKFLOW_STATE_STOPPED, reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_FAILED
	case db.ThreadStatusCancelled:
		return reliantv1.WorkflowState_WORKFLOW_STATE_STOPPED, reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_CANCELLED
	default:
		return reliantv1.WorkflowState_WORKFLOW_STATE_UNSPECIFIED, reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_UNSPECIFIED
	}
}
