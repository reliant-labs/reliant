// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"go.temporal.io/sdk/activity"
)

// CreateWorkflowWithThreadInput contains the input for creating a workflow and thread atomically
type CreateWorkflowWithThreadInput struct {
	// Workflow fields
	WorkflowID       string  `json:"workflow_id"`
	WorkflowName     string  `json:"workflow_name"`
	ParentWorkflowID *string `json:"parent_workflow_id,omitempty"`
	SpawnedByNodeID  *string `json:"spawned_by_node_id,omitempty"`
	LoopIteration    *int64  `json:"loop_iteration,omitempty"`

	// Thread fields
	ChatID       string  `json:"chat_id" reliant:"-"`
	ThreadID     string  `json:"thread_id"`
	ThreadTitle  *string `json:"thread_title,omitempty"`
	ParentThread *string `json:"parent_thread,omitempty"` // Parent thread for non-fork child threads (e.g., spawn)

	// Origin records HOW the thread was created ("spawn", "node", "fork",
	// "main"). This is the field readers use to tell a spawn thread from a
	// graph-node thread; SpawnedByNodeID above answers a different question
	// (which node produced the workflow) and must not be overloaded for it.
	Origin       *string `json:"origin,omitempty"`
	OriginNodeID *string `json:"origin_node_id,omitempty"`

	// Fork configuration (optional)
	ForkFromThread *string `json:"fork_from_thread,omitempty"`
}

// CreateWorkflowWithThreadOutput contains the IDs of the created workflow, thread, and context window
type CreateWorkflowWithThreadOutput struct {
	WorkflowID      string `json:"workflow_id"`
	ThreadID        string `json:"thread_id"`
	ContextWindowID string `json:"context_window_id"`
}

// CreateWorkflowWithThreadActivity creates a workflow and its associated thread atomically
type CreateWorkflowWithThreadActivity struct {
	threads *threads.Service
	repo    db.Repository
}

// NewCreateWorkflowWithThreadActivity creates a new CreateWorkflowWithThreadActivity.
//
// The repository is needed to resolve the run's owner. This activity creates
// every spawned run, so leaving the owner unset here would leave a large share
// of runs without one — and a reader that then fell back to the chat would hide
// that rather than surface it.
func NewCreateWorkflowWithThreadActivity(threadsService *threads.Service, repo db.Repository) *CreateWorkflowWithThreadActivity {
	return &CreateWorkflowWithThreadActivity{threads: threadsService, repo: repo}
}

// announceThread writes the "thread" chat update for a newly created (or newly
// re-linked) thread. Every identity field is read from the persisted thread
// row, which owns thread identity, rather than from the activity's input.
//
// It is keyed by workflow ID like every other thread announcement
// (WorkflowStatusActivity.emitThreadUpdate), so the stream's merge and the
// snapshot's per-thread dedup treat it and the child's later "started"
// announcement as one record. Status is "running": a workflow is being
// started on the thread, including when an existing thread is resumed.
func (a *CreateWorkflowWithThreadActivity) announceThread(ctx context.Context, chatID string, workflow *db.Workflow, thread *db.Thread) error {
	updateData := map[string]interface{}{
		"update_type":   "thread",
		"id":            workflow.ID,
		"chat_id":       chatID,
		"thread":        thread.ID,
		"workflow_id":   workflow.ID,
		"workflow_name": workflow.WorkflowName,
		"status":        "running",
		"origin":        thread.Origin,
		"created_at":    thread.CreatedAt.UTC().Format(time.RFC3339),
	}
	if thread.Title != nil && *thread.Title != "" {
		updateData["thread_title"] = *thread.Title
	}
	if thread.OriginNodeID != nil && *thread.OriginNodeID != "" {
		updateData["origin_node_id"] = *thread.OriginNodeID
	}

	updateDataJSON, err := json.Marshal(updateData)
	if err != nil {
		return fmt.Errorf("failed to marshal thread announcement: %w", err)
	}
	return a.repo.CreateChatUpdate(ctx, chatID, db.UpdateTypeThread, workflow.ID, string(updateDataJSON))
}

// Name returns the activity name for registration
func (a *CreateWorkflowWithThreadActivity) Name() string {
	return "CreateWorkflowWithThread"
}

// DisplayName returns human-readable name for UI
func (a *CreateWorkflowWithThreadActivity) DisplayName() string {
	return "Create Workflow With Thread"
}

// Description returns what the activity does
func (a *CreateWorkflowWithThreadActivity) Description() string {
	return "Creates a workflow record and its associated thread atomically in a single transaction"
}

// Category returns the activity category for UI grouping
func (a *CreateWorkflowWithThreadActivity) Category() schema.ActivityCategory {
	return schema.CategoryWorkflowManagement
}

// Execute creates a workflow and thread atomically using the threads.Service
func (a *CreateWorkflowWithThreadActivity) Execute(ctx context.Context, input CreateWorkflowWithThreadInput) (CreateWorkflowWithThreadOutput, error) {
	logger := activity.GetLogger(ctx)
	logger.Debug("[CreateWorkflowWithThread] Creating workflow and thread",
		"workflowID", input.WorkflowID,
		"workflowName", input.WorkflowName,
		"chatID", input.ChatID,
		"threadID", input.ThreadID,
		"parentWorkflowID", input.ParentWorkflowID,
		"forkFromThread", input.ForkFromThread)

	// Validate required fields
	if input.WorkflowID == "" {
		return CreateWorkflowWithThreadOutput{}, fmt.Errorf("workflow_id is required")
	}
	if input.WorkflowName == "" {
		return CreateWorkflowWithThreadOutput{}, fmt.Errorf("workflow_name is required")
	}
	if input.ChatID == "" {
		return CreateWorkflowWithThreadOutput{}, fmt.Errorf("chat_id is required")
	}

	// Build the db.Workflow struct
	// Thread field will be set to the ThreadID (or generated if empty)
	threadID := input.ThreadID
	if threadID == "" {
		threadID = input.WorkflowID // Use workflow ID as default thread ID
	}

	workflow := &db.Workflow{
		ID:              input.WorkflowID,
		ParentID:        input.ParentWorkflowID,
		ChatID:          input.ChatID,
		WorkflowName:    input.WorkflowName,
		Thread:          threadID,
		Status:          db.Active(),
		SpawnedByNodeID: input.SpawnedByNodeID,
		LoopIteration:   input.LoopIteration,
		CreatedAt:       time.Now().UTC(),
		OwnerUserID:     resolveRunOwner(ctx, a.repo, input.ParentWorkflowID, input.ChatID),
	}

	// Build the options for CreateWorkflowWithThread
	opts := threads.CreateWorkflowWithThreadOpts{
		Workflow:       workflow,
		ThreadID:       input.ThreadID,
		ChatID:         input.ChatID,
		ThreadTitle:    input.ThreadTitle,
		ParentThread:   input.ParentThread,
		ForkFromThread: input.ForkFromThread,
		OriginNodeID:   input.OriginNodeID,
	}
	if input.Origin != nil {
		opts.Origin = *input.Origin
	}

	// Call the threads service to create workflow and thread atomically
	createdWorkflow, createdThread, contextWindow, err := a.threads.CreateWorkflowWithThread(ctx, opts)
	if err != nil {
		logger.Error("[CreateWorkflowWithThread] Failed to create workflow and thread",
			"workflowID", input.WorkflowID,
			"error", err)
		return CreateWorkflowWithThreadOutput{}, fmt.Errorf("failed to create workflow and thread: %w", err)
	}

	logger.Info("[CreateWorkflowWithThread] Successfully created workflow and thread",
		"workflowID", createdWorkflow.ID,
		"threadID", createdThread.ID,
		"contextWindowID", contextWindow.ID)

	// Announce a child thread NOW, while nothing has been written to it yet.
	//
	// The caller (initChildWorkflow) saves the child's seed message as its
	// very next step, and the child only announces itself later, from its own
	// WorkflowStatus("started"). So the first message on every spawned thread
	// reached the client before anything that classifies the thread: no
	// thread record, and no execution-tree row until the next refetch.
	// InterleavedTimeline drops messages on a thread it cannot classify, so a
	// new sub-agent's opening messages were hidden. Measured on the dev
	// database: the message preceded the announcement for all 40 of the
	// newest spawns.
	//
	// The main thread is the chat itself, classified by identity, and has its
	// own announcement path — that covers the root run and an inline workflow
	// that inherits the main thread.
	if input.ParentWorkflowID != nil && createdThread.Origin != db.ThreadOriginMain {
		if err := a.announceThread(ctx, input.ChatID, createdWorkflow, createdThread); err != nil {
			// The run is created correctly either way; the child's own
			// "started" announcement follows. Logged at error because the
			// symptom — a spawn's first messages missing from the timeline —
			// is otherwise unattributable.
			logger.Error("[CreateWorkflowWithThread] Failed to announce thread; its first messages may not render until the execution tree refetches",
				"workflowID", createdWorkflow.ID,
				"threadID", createdThread.ID,
				"error", err)
		}
	}

	return CreateWorkflowWithThreadOutput{
		WorkflowID:      createdWorkflow.ID,
		ThreadID:        createdThread.ID,
		ContextWindowID: contextWindow.ID,
	}, nil
}
