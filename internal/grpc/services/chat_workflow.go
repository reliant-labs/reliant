// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow"
)

// UpdateWorkflowParams updates workflow parameters for a running chat
// This signals the running workflow to update its inputs (e.g., mode, temperature)
func (s *ChatService) UpdateWorkflowParams(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateWorkflowParamsRequest],
) (*connect.Response[reliantv1.UpdateWorkflowParamsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}

	// Get chat to verify ownership
	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// Always signal the root workflow. Child workflows are inline goroutines
	// with synthetic DB IDs that Temporal doesn't know about.
	// Thread-scoped updates use the __thread key in the signal payload.
	workflowID := chat.MainThreadID()
	runID := ""
	if workflowID != "" {
		if chat.RunID != nil {
			runID = *chat.RunID
		}
	} else {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no active workflow for chat"))
	}

	if err := launch.ValidateWorkflowParamStructure(req.Msg.Params); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// If targeting a specific thread, verify it exists and is running
	if req.Msg.ThreadId != nil && *req.Msg.ThreadId != "" {
		wf, wfErr := s.database.GetWorkflowByThread(ctx, req.Msg.ChatId, *req.Msg.ThreadId)
		if wfErr != nil || wf == nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no workflow found for thread"))
		}
		if wf.Status != db.Active() {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("thread workflow is not running"))
		}
	}

	// Build state update through the same path as SendMessage —
	// this ensures schema defaults and validation are applied.
	workflowName := ""
	if chat.WorkflowName != nil {
		workflowName = *chat.WorkflowName
	}
	stateUpdate := s.launcher().BuildStateUpdateForActiveWorkflow(ctx, userID, chat, workflowName, nil, req.Msg.Params)

	// Validate model selectors in the updated params before signaling the workflow
	if validationErrors := s.launcher().ValidateWorkflowInputs(ctx, userID, workflowName, chat.ProjectID, stateUpdate); len(validationErrors) > 0 {
		errMsgs := make([]string, len(validationErrors))
		for i, e := range validationErrors {
			errMsgs[i] = e.Error()
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow input validation failed: %s", strings.Join(errMsgs, "; ")))
	}

	// Add thread scope to signal payload so the handler updates the correct thread's inputs
	if req.Msg.ThreadId != nil && *req.Msg.ThreadId != "" {
		stateUpdate["__thread"] = *req.Msg.ThreadId
	}

	// Signal the root workflow with the parameter updates
	err = s.tempClient.SignalWorkflow(
		ctx,
		workflowID,
		runID,
		"update_workflow_state",
		stateUpdate,
	)
	if err != nil {
		logging.Error("Failed to signal workflow for param update",
			"chatID", req.Msg.ChatId,
			"workflowID", workflowID,
			"threadID", req.Msg.ThreadId,
			"error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update workflow params"))
	}

	logging.Debug("Updated workflow params",
		"chatID", req.Msg.ChatId,
		"workflowID", workflowID,
		"threadID", req.Msg.ThreadId,
		"params", stateUpdate)

	return connect.NewResponse(&reliantv1.UpdateWorkflowParamsResponse{
		Success: true,
		Message: "Workflow parameters updated",
	}), nil
}

// GetWorkflowExecutions returns the workflow execution tree for a chat
func (s *ChatService) GetWorkflowExecutions(
	ctx context.Context,
	req *connect.Request[reliantv1.GetWorkflowExecutionsRequest],
) (*connect.Response[reliantv1.GetWorkflowExecutionsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}

	// Verify ownership
	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// Get all workflows for this chat
	workflows, err := s.database.ListWorkflowsByChat(ctx, req.Msg.ChatId)
	if err != nil {
		logging.Error("Failed to list workflows", "error", err, "chatID", req.Msg.ChatId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list workflows"))
	}

	if len(workflows) == 0 {
		return connect.NewResponse(&reliantv1.GetWorkflowExecutionsResponse{}), nil
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
	if req.Msg.View == reliantv1.WorkflowExecutionView_WORKFLOW_EXECUTION_VIEW_FULL {
		steps, err = s.database.GetStepExecutionsForChat(ctx, req.Msg.ChatId)
	} else {
		steps, err = s.database.GetBasicStepExecutionsForChat(ctx, req.Msg.ChatId)
	}
	if err != nil {
		logging.Error("Failed to get step executions", "error", err, "chatID", req.Msg.ChatId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get step executions"))
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
		return connect.NewResponse(&reliantv1.GetWorkflowExecutionsResponse{}), nil
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
	threads, threadsErr := s.database.ListThreadsByConversation(ctx, req.Msg.ChatId)
	if threadsErr != nil {
		// Degrade to a tree with no thread metadata rather than failing the
		// read: a partial timeline beats an error page. Logged loudly because
		// the symptom (spawned threads rendering inline) is otherwise
		// unattributable; each workflow also logs its own missing thread.
		logging.Error("[WorkflowTree] Failed to list threads; thread origin will be empty and threads without a workflow row will be missing from the timeline",
			"error", threadsErr, "chatID", req.Msg.ChatId)
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
		forkedIDs, err := s.database.ListForkedThreadIDs(ctx, childThreadIDs)
		if err != nil {
			logging.Error("[WorkflowTree] Failed to resolve forked threads; ForkedFromThread will be empty",
				"error", err, "chatID", req.Msg.ChatId)
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

	// The tree above can only describe threads that a workflow row points at,
	// and that is strictly fewer than the threads which exist. Fill the gap
	// from the table that owns thread identity.
	if threadsErr == nil {
		s.attachThreadsWithoutWorkflowRow(threads, allRootProtos)
	}

	// The most recent root is first (for backwards compat)
	var latestRootProto *reliantv1.WorkflowExecution
	if len(allRootProtos) > 0 {
		latestRootProto = allRootProtos[0]
	}

	return connect.NewResponse(&reliantv1.GetWorkflowExecutionsResponse{
		RootWorkflow:     latestRootProto,
		AllRootWorkflows: allRootProtos,
	}), nil
}

// GetThreadWorkflowInputs returns the workflow inputs for a specific thread.
// It looks up the workflow record for the thread, queries Temporal for current inputs,
// and falls back to empty inputs for completed workflows.
func (s *ChatService) GetThreadWorkflowInputs(
	ctx context.Context,
	req *connect.Request[reliantv1.GetThreadWorkflowInputsRequest],
) (*connect.Response[reliantv1.GetThreadWorkflowInputsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" || req.Msg.ThreadId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id and thread_id are required"))
	}

	// Verify chat ownership
	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// Look up the workflow record for this thread
	wf, err := s.database.GetWorkflowByThread(ctx, req.Msg.ChatId, req.Msg.ThreadId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no workflow found for thread"))
	}
	if wf == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no workflow found for thread"))
	}

	isRunning := wf.Status == db.Active()

	// Resolve to root workflow ID for Temporal queries.
	// Child workflows are inline goroutines with synthetic DB IDs that Temporal doesn't know.
	// All queries must target the root workflow (the only real Temporal workflow).
	rootWorkflowID := chat.MainThreadID()
	rootRunID := ""
	if rootWorkflowID != "" {
		if chat.RunID != nil {
			rootRunID = *chat.RunID
		}
	}

	// Try to query Temporal for thread-specific inputs (only works for running workflows)
	inputsMap := make(map[string]*structpb.Value)
	if isRunning && rootWorkflowID != "" {
		// Use get_thread_inputs query which returns the thread's subInputs map
		queryResp, err := s.tempClient.QueryWorkflow(ctx, rootWorkflowID, rootRunID, "get_thread_inputs", req.Msg.ThreadId)
		if err == nil {
			var currentInputs map[string]interface{}
			if err := queryResp.Get(&currentInputs); err == nil {
				// Convert to protobuf Values, filtering out runtime-injected inputs
				for key, value := range currentInputs {
					if workflow.RuntimeInjectedInputs[key] {
						continue
					}
					pbVal, err := structpb.NewValue(value)
					if err == nil {
						inputsMap[key] = pbVal
					}
				}
			}
		} else {
			logging.Debug("Failed to query thread inputs", "error", err, "rootWorkflowID", rootWorkflowID, "thread", req.Msg.ThreadId)
		}
	}

	return connect.NewResponse(&reliantv1.GetThreadWorkflowInputsResponse{
		WorkflowName: wf.WorkflowName,
		Inputs:       inputsMap,
		IsRunning:    isRunning,
	}), nil
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
func (s *ChatService) attachThreadsWithoutWorkflowRow(
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
func threadStatusToWorkflowStatus(status int32) (reliantv1.WorkflowState, reliantv1.WorkflowStopReason) {
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

// workflowTreeBuilder holds everything buildWorkflowExecutionTree needs, all
// loaded once per request so the walk itself issues no queries.
type workflowTreeBuilder struct {
	threadsByID      map[string]*db.Thread
	forkedThreads    map[string]bool
	childrenByParent map[string][]*db.Workflow
	stepsByWorkflow  map[string][]*db.ChatStepExecution
}

// build recursively builds the workflow execution tree
func (b *workflowTreeBuilder) build(wf *db.Workflow) *reliantv1.WorkflowExecution {
	proto := &reliantv1.WorkflowExecution{
		Id:           wf.ID,
		WorkflowName: wf.WorkflowName,
		Thread:       wf.Thread,
		State:        wf.Status.State,
		StopReason:   wf.Status.StopReason,
		CreatedAt:    wf.CreatedAt.Format(time.RFC3339),
		MessageCount: 0, // TODO: count messages by thread
	}

	if wf.ParentID != nil {
		proto.ParentId = wf.ParentID
	}
	if wf.SpawnedByNodeID != nil {
		proto.SpawnedByNodeId = wf.SpawnedByNodeID
	}
	// The run's verdict, beside its lifecycle status: a run that ran to its
	// `failed` terminal node is Status=COMPLETED, Outcome=failure, and a
	// supervisor must be able to tell that from a run that built the app.
	if wf.Outcome != nil && *wf.Outcome != "" {
		proto.Outcome = wf.Outcome
	}
	// Populate Origin, ForkedFromThread, ParentThread, and ThreadTitle from the
	// Thread table (single source of truth for thread identity).
	//
	// A failure here is NOT cosmetic: Origin is what tells the UI a thread is a
	// spawned sub-agent, and a spawn whose origin is missing renders its entire
	// transcript inline in the parent chat. Swallowing the error left that
	// looking like a frontend bug for a long time, so it is logged loudly.
	thread := b.threadsByID[wf.Thread]
	if thread == nil {
		logging.Error("[WorkflowTree] Failed to load thread for workflow; Origin will be empty and spawned threads will render inline",
			"error", fmt.Errorf("thread %q not found in chat", wf.Thread),
			"workflowID", wf.ID,
			"thread", wf.Thread,
			"chatID", wf.ChatID)
	}
	if thread != nil {
		if thread.ParentThreadID != nil {
			// ForkedFromThread: only for actual forks, not plain child threads
			// (e.g. spawn). ForkThread always links the thread's initial (sequence
			// 0) context window to the parent's via ParentContextWindowID, even
			// when ForkAtMessageID is nil (forking an empty parent thread) --
			// createThreadInternal never sets that link. That link, not
			// ForkAtMessageID, is what "is this a fork" needs to test.
			if b.forkedThreads[wf.Thread] {
				proto.ForkedFromThread = thread.ParentThreadID
			}
			// ParentThread: always set when parent exists (both fork and new)
			proto.ParentThread = thread.ParentThreadID
		}
		if thread.Title != nil {
			proto.ThreadTitle = thread.Title
		}
		proto.Origin = thread.Origin
		proto.OriginNodeId = thread.OriginNodeID
	}
	if wf.LoopIteration != nil {
		iteration := int32(*wf.LoopIteration)
		proto.Iteration = &iteration
	}
	if wf.CompletedAt != nil {
		completedAt := wf.CompletedAt.Format(time.RFC3339)
		proto.CompletedAt = &completedAt
	}

	// Add step executions.
	//
	// output_json is never sent on this path. The only thing a client read out
	// of it here was the saved message id of a "-save" step, which now arrives
	// as its own scalar field — so the response carries one UUID per save step
	// instead of that step's entire output (37 MB for the worst real chat).
	//
	// output_json still arrives for user-facing activities, which ActivityIndicator
	// and the workflow viewer's Output panel render. The query withholds it only
	// for internal plumbing (model.InternalActivities) — and that is where all
	// the weight is: 32,013 of that chat's 32,023 steps, against 23 kB for the
	// ten that remain.
	if steps, ok := b.stepsByWorkflow[wf.ID]; ok {
		proto.Steps = make([]*reliantv1.StepExecution, len(steps))
		for i, step := range steps {
			proto.Steps[i] = &reliantv1.StepExecution{
				Id:           step.ID,
				WorkflowId:   step.WorkflowID,
				StepId:       step.StepID,
				ActivityName: step.ActivityName,
				CreatedAt:    step.CreatedAt.Format(time.RFC3339),
			}
			if step.SavedMessageID.Valid {
				proto.Steps[i].SavedMessageId = &step.SavedMessageID.String
			}
			proto.Steps[i].OutputJson = step.OutputJSON
			if step.ExitCode.Valid {
				exitCode := int32(step.ExitCode.Int64)
				proto.Steps[i].ExitCode = &exitCode
			}
			if step.Success.Valid {
				proto.Steps[i].Success = &step.Success.Bool
			}
			if step.DurationMs.Valid {
				proto.Steps[i].DurationMs = &step.DurationMs.Int64
			}
			if step.LoopNodeID.Valid {
				proto.Steps[i].LoopNodeId = &step.LoopNodeID.String
			}
			if step.LoopIteration.Valid {
				loopIter := int32(step.LoopIteration.Int64)
				proto.Steps[i].LoopIteration = &loopIter
			}
		}
	}

	// Find and add children
	for _, child := range b.childrenByParent[wf.ID] {
		proto.Children = append(proto.Children, b.build(child))
	}

	return proto
}
