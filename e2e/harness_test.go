// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/grpc/services"
	"github.com/reliant-labs/reliant/internal/integration"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// TestHarness provides a complete e2e testing environment with:
// - Real embedded Temporal server
// - Real integration server with all workers
// - Real activities
// - Mock LLM driver (via drivers.Override)
// - Optional mock tool executor, run executor, and approval responder
//
// Usage:
//
//	func TestMyWorkflow(t *testing.T) {
//	    h := e2e.NewTestHarness(t)
//	    defer h.Cleanup()
//
//	    // Configure mock LLM
//	    h.MockLLM.SetResponse("Hello!")
//
//	    // Start workflow via gRPC (creates project, chat, and workflow)
//	    chatID := h.StartAgentWorkflowViaGRPC(t, "Hello")
//
//	    // Assertions with < 3s timeout
//	    h.WaitForMessages(t, chatID, 2)
//	}
//
// Advanced usage with mocks:
//
//	func TestAdaptiveRetry(t *testing.T) {
//	    h := e2e.NewTestHarness(t)
//	    defer h.Cleanup()
//
//	    // Configure run mock to fail first, then succeed
//	    h.MockRun.OnSequence("go test ./...",
//	        MockRunResponse{ExitCode: 1, Stderr: "FAIL"},
//	        MockRunResponse{ExitCode: 0, Stdout: "PASS"},
//	    )
//
//	    h.StartWorkflowFromFile(".reliant/workflows/retry.yaml", map[string]any{
//	        "command": "go test ./...",
//	    })
//
//	    h.WaitForWorkflowComplete(t, chatID)
//	    require.Equal(t, 2, h.MockRun.CallCount())
//	}
type TestHarness struct {
	t *testing.T

	// Real components
	Server         *integration.Server
	TemporalClient client.Client
	DB             db.Repository

	// gRPC services (uses real production code paths)
	chatService  *services.ChatService
	yieldService *services.YieldService

	// Mock LLM driver (always active)
	MockLLM *MockLLMDriver

	// Optional mocks (created lazily or via options)
	MockRun      *MockRunExecutor
	MockTools    *MockToolExecutor
	MockApproval *MockApprovalResponder

	// Test state
	tmpDir  string
	userID  string
	cleanup []func()
	mu      sync.Mutex

	// Tracking for loop iterations and workflow state
	loopIterations map[string]int // nodeID -> iteration count
}

// getProjectRoot finds the project root by looking for go.mod
func getProjectRoot() string {
	// Start from current working directory and walk up
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

// copyWorkflowFiles copies .reliant/workflows from project root to temp dir
func copyWorkflowFiles(t *testing.T, projectRoot, tmpDir string) error {
	t.Helper()

	srcDir := filepath.Join(projectRoot, ".reliant", "workflows")
	dstDir := filepath.Join(tmpDir, ".reliant", "workflows")

	// Create destination directory
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return fmt.Errorf("failed to create workflow dir: %w", err)
	}

	// Copy all yaml files
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		// Not an error if directory doesn't exist - workflows may all be builtin
		return nil
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		srcPath := filepath.Join(srcDir, entry.Name())
		dstPath := filepath.Join(dstDir, entry.Name())

		data, err := os.ReadFile(srcPath)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", srcPath, err)
		}

		if err := os.WriteFile(dstPath, data, 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", dstPath, err)
		}
	}

	return nil
}

// NewTestHarness creates a new test harness with real Temporal and mock LLM.
// The harness starts all components and is ready to use immediately.
func NewTestHarness(t *testing.T) *TestHarness {
	t.Helper()

	projectRoot := getProjectRoot()
	tmpDir := t.TempDir()

	// Copy workflow files from project to temp directory
	if err := copyWorkflowFiles(t, projectRoot, tmpDir); err != nil {
		t.Logf("Warning: failed to copy workflow files: %v", err)
	}

	// Change to temp directory so workflow files can be found
	// The workflow loader uses relative paths from the current working directory
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get current directory: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}

	// Generate a unique task queue suffix for this test harness
	// This ensures test isolation when sharing a Temporal server
	taskQueueSuffix := uuid.New().String()[:8]

	h := &TestHarness{
		t:              t,
		tmpDir:         tmpDir,
		userID:         "test-user-" + uuid.New().String()[:8],
		MockLLM:        NewMockLLMDriver(),
		MockRun:        NewMockRunExecutor(),
		MockTools:      NewMockToolExecutor(),
		loopIterations: make(map[string]int),
	}

	// Note: Mock model is now defined in models.yaml with visibility: dev

	// Set mock LLM as global override BEFORE creating server
	drivers.Override = h.MockLLM

	// Use shared Temporal server from TestMain to avoid port conflicts
	// and speed up tests (no need to start/stop Temporal for each test)
	sharedTemporalServer := GetSharedTemporalServer()

	// Create integration server config with mock executors
	// Pass the shared Temporal server to avoid creating a new one
	// Use unique task queue suffix for test isolation
	serverConfig := &integration.Config{
		DatabasePath:           tmpDir,
		ExternalTemporalServer: sharedTemporalServer,
		TaskQueueSuffix:        taskQueueSuffix,
		AnthropicAPIKey:        "test-api-key", // Not used due to mock
		WorktreeBaseDir:        tmpDir + "/worktrees",
		ToolExecutorOverride:   h.MockTools, // Inject mock tool executor
		RunExecutorOverride:    h.MockRun,   // Inject mock run executor
	}

	// Create and start integration server
	server, err := integration.NewServer(serverConfig)
	require.NoError(t, err, "failed to create integration server")
	h.Server = server

	// Start server (includes Temporal + workers)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err = server.Start(ctx)
	require.NoError(t, err, "failed to start integration server")

	// Get components
	h.TemporalClient = server.Client()
	h.DB = server.Database()

	// Seed one provider API key so model selector/provider validation passes in gRPC paths.
	// The e2e harness always uses a mock LLM driver override, but some validation still checks
	// whether at least one provider is configured for the user.
	err = h.DB.SetProviderAPIKey(context.Background(), h.userID, "anthropic", "e2e-test-key")
	require.NoError(t, err, "failed to seed test API key")

	// Initialize ChatService with real production code
	h.chatService = services.NewChatService(
		server.Database(),
		server.Client(),
		server.PauseService(),
		server.SharedTaskQueueName(),
	)

	// Initialize YieldService with real production code
	h.yieldService = services.NewYieldService(
		server.Database().(*db.Repo),
		server.PauseService(),
	)

	// Initialize MockApproval with the repo (after DB is available)
	h.MockApproval = NewMockApprovalResponder(h.DB)

	// Wait for workers to be fully registered with Temporal
	// This prevents "no poller" errors when tests start immediately after server.Start()
	h.waitForWorkersReady(t)

	// Register cleanup
	h.cleanup = append(h.cleanup, func() {
		drivers.Override = nil
	})
	h.cleanup = append(h.cleanup, func() {
		h.MockApproval.Stop()
	})
	h.cleanup = append(h.cleanup, func() {
		_ = server.Stop()
	})
	// Restore original working directory
	h.cleanup = append(h.cleanup, func() {
		_ = os.Chdir(originalDir)
	})

	return h
}

// waitForWorkersReady waits for Temporal workers to be fully registered and ready
// by attempting a simple workflow describe operation that requires worker connectivity
func (h *TestHarness) waitForWorkersReady(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Poll until we can successfully interact with the Temporal cluster
	// The workers need time to register their pollers after server.Start() returns
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for i := 0; i < 100; i++ {
		select {
		case <-ctx.Done():
			t.Fatal("timeout waiting for Temporal workers to be ready")
			return
		case <-ticker.C:
			// Try to list workflows - this validates that Temporal is fully operational
			// and workers are registered (even if we get empty results, no error means ready)
			_, err := h.TemporalClient.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{
				Namespace: "reliant",
				PageSize:  1,
			})
			if err == nil {
				// Small additional delay to ensure workers are fully polling
				time.Sleep(50 * time.Millisecond)
				return
			}
			// Not ready yet, continue polling
		}
	}

	t.Fatal("Temporal workers did not become ready after 100 attempts")
}

// Cleanup cleans up all test resources. Always defer this.
func (h *TestHarness) Cleanup() {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Run cleanup functions in reverse order
	for i := len(h.cleanup) - 1; i >= 0; i-- {
		h.cleanup[i]()
	}
}

// UserID returns the test user ID
func (h *TestHarness) UserID() string {
	return h.userID
}

// TmpDir returns the temporary directory for this test harness
func (h *TestHarness) TmpDir() string {
	return h.tmpDir
}

// GetSharedTaskQueue returns the shared task queue name (with test suffix if applicable).
// All workflows use the same shared task queue.
func (h *TestHarness) GetSharedTaskQueue() string {
	return h.Server.SharedTaskQueueName()
}

// StartWorkerForWorkflow returns the task queue name for a workflow.
// All workflows use the global "chat-workflows" task queue.
func (h *TestHarness) StartWorkerForWorkflow(t *testing.T, workflowID string) string {
	t.Helper()
	return "chat-workflows"
}

// ============================================================================
// FIXTURE CREATION
// ============================================================================

// CreateProject creates a test project
func (h *TestHarness) CreateProject(t *testing.T, name string) *db.Project {
	t.Helper()

	ctx := context.Background()
	project := &db.Project{
		ID:         uuid.New().String(),
		Name:       name,
		Path:       h.tmpDir + "/" + name,
		UserID:     h.userID,
		IsGitRepo:  false,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
		LastActive: time.Now(),
	}

	// Create the directory
	_ = os.MkdirAll(project.Path, 0755)

	err := h.DB.CreateProject(ctx, project)
	require.NoError(t, err, "failed to create project")

	return project
}

// CreateChatWithGitRepo creates a test chat with a project that has a real git repo.
// This is needed for tests that use worktree operations.
func (h *TestHarness) CreateChatWithGitRepo(t *testing.T, projectName, title string) *db.Chat {
	t.Helper()

	ctx := context.Background()

	// Create project directory
	projectPath := h.tmpDir + "/" + projectName
	err := os.MkdirAll(projectPath, 0755)
	require.NoError(t, err, "failed to create project directory")

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = projectPath
	err = cmd.Run()
	require.NoError(t, err, "failed to init git repo")

	// Configure git user for commits
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = projectPath
	_ = cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = projectPath
	_ = cmd.Run()

	// Create initial commit (required for worktrees)
	readmePath := filepath.Join(projectPath, "README.md")
	err = os.WriteFile(readmePath, []byte("# Test Project\n"), 0644)
	require.NoError(t, err, "failed to create README")

	cmd = exec.Command("git", "add", ".")
	cmd.Dir = projectPath
	err = cmd.Run()
	require.NoError(t, err, "failed to git add")

	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = projectPath
	err = cmd.Run()
	require.NoError(t, err, "failed to git commit")

	// Create project in database
	project := &db.Project{
		ID:         uuid.New().String(),
		Name:       projectName,
		Path:       projectPath,
		UserID:     h.userID,
		IsGitRepo:  true,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
		LastActive: time.Now(),
	}

	err = h.DB.CreateProject(ctx, project)
	require.NoError(t, err, "failed to create project")

	return h.CreateChatInProject(t, project.ID, title)
}

// CreateChatInProject creates a chat in an existing project
func (h *TestHarness) CreateChatInProject(t *testing.T, projectID, title string) *db.Chat {
	t.Helper()

	ctx := context.Background()
	chatID := uuid.New().String()
	workflowName := "builtin://agent" // Default workflow

	chat := &db.Chat{
		ID:           chatID,
		Title:        title,
		ProjectID:    projectID,
		UserID:       h.userID,
		State:        db.ChatStateIdle,
		WorkflowID:   &chatID, // Root workflow ID = chat ID
		WorkflowName: &workflowName,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		LastActive:   time.Now(),
	}

	err := h.DB.CreateChat(ctx, chat)
	require.NoError(t, err, "failed to create chat")

	return chat
}

// ============================================================================
// MESSAGE HELPERS
// ============================================================================

// SaveTriggerMessage saves a user message to the database before starting a workflow.
// This matches production behavior where the trigger message is saved before workflow start.
// Returns the message ID for reference.
func (h *TestHarness) SaveTriggerMessage(t *testing.T, chatID, threadID, prompt string) string {
	t.Helper()
	ctx := context.Background()

	nextOrdinal, err := h.DB.GetNextOrdinal(ctx, threadID)
	require.NoError(t, err, "failed to get next ordinal")

	// Get or create thread and context window
	thread, _ := h.DB.GetThread(ctx, threadID)
	if thread == nil {
		_, err := h.DB.CreateThread(ctx, &db.Thread{
			ID:             threadID,
			ConversationID: chatID,
			CreatedAt:      time.Now(),
		})
		require.NoError(t, err, "failed to create thread")
	}

	contextWindowID := fmt.Sprintf("%s:%s:0", chatID, threadID)
	cw, _ := h.DB.GetContextWindow(ctx, contextWindowID)
	if cw == nil {
		_, err := h.DB.CreateContextWindow(ctx, &db.ContextWindow{
			ID:        contextWindowID,
			ThreadID:  threadID,
			Sequence:  0,
			CreatedAt: time.Now(),
		})
		require.NoError(t, err, "failed to create context window")
	}

	msgID := uuid.New().String()
	msg := &db.Message{
		ID:              msgID,
		ChatID:          chatID,
		ThreadID:        threadID,
		ContextWindowID: contextWindowID,
		Ordinal:         nextOrdinal,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_USER,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	err = h.DB.CreateMessage(ctx, msg)
	require.NoError(t, err, "failed to save trigger message")

	block := &db.MessageContentBlock{
		ID:        uuid.New().String(),
		MessageID: msgID,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
		Content:   &prompt,
		Position:  0,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	err = h.DB.CreateContentBlock(ctx, block)
	require.NoError(t, err, "failed to save content block")

	return msgID
}

// ============================================================================
// WORKFLOW EXECUTION
// ============================================================================

// WaitForWorkflowComplete waits for the workflow to complete (max 3s).
// If the workflow ends in a failed/terminated/timed-out state the test is
// immediately failed with the workflow error so failures are visible.
func (h *TestHarness) WaitForWorkflowComplete(t *testing.T, chatID string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), MaxWaitTime)
	defer cancel()

	chat, err := h.DB.GetChat(ctx, chatID)
	require.NoError(t, err)

	workflowID := chatID
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		workflowID = *chat.WorkflowID
	}

	// Poll for workflow completion
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			h.LogWorkflowDiagnostics(t, chatID)
			t.Fatalf("timeout waiting for workflow to complete (chatID: %s)", chatID)
		case <-ticker.C:
			desc, err := h.TemporalClient.DescribeWorkflowExecution(ctx, workflowID, "")
			if err != nil {
				continue
			}
			status := desc.WorkflowExecutionInfo.Status
			if status != enums.WORKFLOW_EXECUTION_STATUS_RUNNING {
				if status == enums.WORKFLOW_EXECUTION_STATUS_FAILED ||
					status == enums.WORKFLOW_EXECUTION_STATUS_TERMINATED ||
					status == enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT {
					run := h.TemporalClient.GetWorkflow(context.Background(), workflowID, "")
					var result interface{}
					if runErr := run.Get(context.Background(), &result); runErr != nil {
						t.Fatalf("workflow %s (chatID=%s): status=%s error=%v", workflowID, chatID, status, runErr)
					}
				}
				return
			}
		}
	}
}

// WaitForWorkflowCompleteWithError waits for workflow completion and logs any errors
func (h *TestHarness) WaitForWorkflowCompleteWithError(t *testing.T, chatID, runID string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	chat, err := h.DB.GetChat(ctx, chatID)
	require.NoError(t, err)

	workflowID := chatID
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		workflowID = *chat.WorkflowID
	}

	// Get workflow run and wait for result
	run := h.TemporalClient.GetWorkflow(ctx, workflowID, runID)
	var result interface{}
	err = run.Get(ctx, &result)
	if err != nil {
		t.Logf("WORKFLOW ERROR: %v", err)
		// Get history to understand what happened
		history := h.GetWorkflowHistory(t, workflowID)
		t.Logf("ALL ACTIVITIES (%d):", len(history.GetActivities()))
		for _, a := range history.GetActivities() {
			if a.Failed {
				t.Logf("  FAILED: type=%s id=%s error=%s", a.ActivityType, a.ActivityID, a.FailureMessage)
			} else if a.Completed {
				t.Logf("  OK: type=%s id=%s", a.ActivityType, a.ActivityID)
			} else {
				t.Logf("  ???: type=%s id=%s", a.ActivityType, a.ActivityID)
			}
		}
		// Also print ALL events for debugging
		t.Logf("ALL EVENTS (%d):", len(history.events))
		for _, e := range history.events {
			t.Logf("  Event %d: %s", e.EventId, e.EventType.String())
		}

		// Dump debug traces from chat_updates
		h.DumpDebugTraces(t, chatID)

		t.Fatalf("workflow failed: %v", err)
	}
	t.Logf("Workflow completed successfully: %+v", result)
}

// WaitForWorkflowCompleteWithTimeout waits for workflow completion with a custom timeout
func (h *TestHarness) WaitForWorkflowCompleteWithTimeout(t *testing.T, workflowID string, timeout time.Duration) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			t.Fatalf("timeout waiting for workflow to complete (workflowID: %s)", workflowID)
		case <-ticker.C:
			desc, err := h.TemporalClient.DescribeWorkflowExecution(ctx, workflowID, "")
			if err != nil {
				continue
			}
			if desc.WorkflowExecutionInfo.Status != enums.WORKFLOW_EXECUTION_STATUS_RUNNING {
				return
			}
		}
	}
}

// DumpDebugTraces dumps all debug traces from chat_updates for debugging
func (h *TestHarness) DumpDebugTraces(t *testing.T, chatID string) {
	t.Helper()

	ctx := context.Background()
	updates, err := h.DB.GetUpdatesSince(ctx, chatID, 0, 1000)
	if err != nil {
		t.Logf("Failed to get chat_updates: %v", err)
		return
	}

	t.Logf("DEBUG TRACES (from chat_updates, type=execution_log):")
	for _, update := range updates {
		if update.UpdateType == reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_EXECUTION_LOG {
			t.Logf("  [%d] %s: %s", update.SequenceNumber, update.EntityID, update.Data)
		}
	}
}

func (h *TestHarness) LogWorkflowDiagnostics(t *testing.T, chatID string) {
	t.Helper()

	ctx := context.Background()
	chat, err := h.DB.GetChat(ctx, chatID)
	if err != nil {
		t.Logf("diagnostics: failed to load chat %s: %v", chatID, err)
		return
	}

	workflowID := chatID
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		workflowID = *chat.WorkflowID
	}

	desc, err := h.TemporalClient.DescribeWorkflowExecution(ctx, workflowID, "")
	if err == nil && desc != nil && desc.WorkflowExecutionInfo != nil {
		t.Logf("diagnostics: workflowID=%s status=%s", workflowID, desc.WorkflowExecutionInfo.Status.String())
	}

	history := h.GetWorkflowHistory(t, workflowID)
	activities := history.GetActivities()
	t.Logf("diagnostics: activities=%d", len(activities))
	for _, a := range activities {
		switch {
		case a.Failed:
			t.Logf("  FAILED activity type=%s id=%s error=%s", a.ActivityType, a.ActivityID, a.FailureMessage)
		case a.Completed:
			t.Logf("  OK activity type=%s id=%s", a.ActivityType, a.ActivityID)
			if a.ActivityType == "WorkflowError" || a.ActivityType == "CallLLM" {
				if output, parseErr := a.ParseOutput(); parseErr == nil {
					t.Logf("    %s output=%v", a.ActivityType, output)
				}
			}
		default:
			t.Logf("  PENDING activity type=%s id=%s", a.ActivityType, a.ActivityID)
		}
	}

	h.DumpDebugTraces(t, chatID)

	updates, err := h.DB.GetUpdatesSince(ctx, chatID, 0, 200)
	if err == nil {
		for _, update := range updates {
			if update.UpdateType == reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_ERROR {
				t.Logf("diagnostics: error update seq=%d entity=%s data=%s", update.SequenceNumber, update.EntityID, update.Data)
			}
		}
	}
}

// CreateChatWithWorkflow creates a test chat with a specific workflow name
func (h *TestHarness) CreateChatWithWorkflow(t *testing.T, projectName, title, workflowName string) *db.Chat {
	t.Helper()

	project := h.CreateProject(t, projectName)

	ctx := context.Background()
	chatID := uuid.New().String()

	chat := &db.Chat{
		ID:           chatID,
		Title:        title,
		ProjectID:    project.ID,
		UserID:       h.userID,
		State:        db.ChatStateIdle,
		WorkflowID:   &chatID,
		WorkflowName: &workflowName,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		LastActive:   time.Now(),
	}

	err := h.DB.CreateChat(ctx, chat)
	require.NoError(t, err, "failed to create chat")

	return chat
}

// StartCustomWorkflow starts a custom workflow with the given name and inputs
func (h *TestHarness) StartCustomWorkflow(t *testing.T, chatID, workflowName string, inputs map[string]interface{}) string {
	t.Helper()

	ctx := context.Background()

	workflowID := chatID
	taskQueue := h.GetSharedTaskQueue()

	workflowOptions := client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                taskQueue,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
	}

	// Merge default inputs with provided inputs
	allInputs := map[string]interface{}{
		"chat": map[string]interface{}{
			"auto_approve": true,
			"id":           chatID,
		},
		"current_branch": "main", // Default for tests - matches real chat flow
	}
	for k, v := range inputs {
		allInputs[k] = v
	}

	workflowInput := v2.WorkflowInput{
		ChatID:       chatID,
		WorkflowName: workflowName,
		Inputs:       allInputs,
	}

	run, err := h.TemporalClient.ExecuteWorkflow(ctx, workflowOptions, v2.DynamicWorkflow, workflowInput)
	require.NoError(t, err, "failed to start custom workflow")

	return run.GetRunID()
}

// ============================================================================
// ASSERTIONS
// ============================================================================

// WaitForMessages waits for a specific number of messages.
func (h *TestHarness) WaitForMessages(t *testing.T, chatID string, count int) []*db.Message {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), MaxWaitTime)
	defer cancel()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			messages, _ := h.DB.ListMessages(context.Background(), chatID, db.MessageListOptions{})
			h.LogWorkflowDiagnostics(t, chatID)
			t.Fatalf("timeout waiting for %d messages, got %d (chatID: %s)", count, len(messages), chatID)
			return nil
		case <-ticker.C:
			messages, err := h.DB.ListMessages(ctx, chatID, db.MessageListOptions{})
			if err != nil {
				continue
			}
			if len(messages) >= count {
				return messages
			}
		}
	}
}

// WaitForCondition waits for a condition to be true.
func (h *TestHarness) WaitForCondition(t *testing.T, condition func() bool, msg string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), MaxWaitTime)
	defer cancel()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			t.Fatalf("timeout waiting for condition: %s", msg)
		case <-ticker.C:
			if condition() {
				return
			}
		}
	}
}

// AssertMessageRoles asserts messages have specific roles in order
func (h *TestHarness) AssertMessageRoles(t *testing.T, messages []*db.Message, expectedRoles ...reliantv1.MessageRole) {
	t.Helper()

	require.Len(t, messages, len(expectedRoles), "message count mismatch")

	for i, role := range expectedRoles {
		require.Equal(t, role, messages[i].Role,
			fmt.Sprintf("message %d: expected role %d, got %d", i, role, messages[i].Role))
	}
}

// AssertMessageContent checks that a message contains expected text
func (h *TestHarness) AssertMessageContent(t *testing.T, messageID string, expectedContent string) {
	t.Helper()

	ctx := context.Background()
	blocks, err := h.DB.ListContentBlocks(ctx, messageID)
	require.NoError(t, err, "failed to list content blocks")

	for _, block := range blocks {
		if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT && block.Content != nil {
			require.Contains(t, *block.Content, expectedContent)
			return
		}
	}

	t.Fatalf("no text block found with expected content: %s", expectedContent)
}

// AssertHasToolCall checks that a message has a tool call with the given name
func (h *TestHarness) AssertHasToolCall(t *testing.T, messageID string, toolName string) {
	t.Helper()

	ctx := context.Background()
	blocks, err := h.DB.ListContentBlocks(ctx, messageID)
	require.NoError(t, err, "failed to list content blocks")

	for _, block := range blocks {
		if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL && block.ToolName != nil && *block.ToolName == toolName {
			return
		}
	}

	t.Fatalf("no tool call found with name: %s", toolName)
}

// GetMessages returns all messages for a chat
func (h *TestHarness) GetMessages(t *testing.T, chatID string) []*db.Message {
	t.Helper()

	ctx := context.Background()
	messages, err := h.DB.ListMessages(ctx, chatID, db.MessageListOptions{})
	require.NoError(t, err, "failed to list messages")

	return messages
}

// GetContentBlocks returns all content blocks for a message
func (h *TestHarness) GetContentBlocks(t *testing.T, messageID string) []*db.MessageContentBlock {
	t.Helper()

	ctx := context.Background()
	blocks, err := h.DB.ListContentBlocks(ctx, messageID)
	require.NoError(t, err, "failed to list content blocks")

	return blocks
}

// GetMessageText returns the text content of a message by concatenating all text blocks.
// Returns empty string if the message has no text content.
func (h *TestHarness) GetMessageText(t *testing.T, messageID string) string {
	t.Helper()

	blocks := h.GetContentBlocks(t, messageID)
	var text string
	for _, block := range blocks {
		if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT && block.Content != nil {
			text += *block.Content
		}
	}
	return text
}

// ============================================================================
// WORKFLOW FROM FILE
// ============================================================================

// StartWorkflowFromFile starts a workflow loaded from a YAML file
// The path can be relative to the project root or absolute
// Example: h.StartWorkflowFromFile(t, chatID, ".reliant/workflows/adaptive-retry.yaml", map[string]any{"command": "go test"})
func (h *TestHarness) StartWorkflowFromFile(t *testing.T, chatID, path string, inputs map[string]interface{}) string {
	t.Helper()

	ctx := context.Background()

	// Determine workflow name from path
	// For .reliant/workflows/my-workflow.yaml -> "my-workflow"
	// For builtin:// paths, use as-is
	workflowName := path
	if !strings.HasPrefix(path, "builtin://") {
		// Extract filename without extension
		base := filepath.Base(path)
		ext := filepath.Ext(base)
		workflowName = strings.TrimSuffix(base, ext)
	}

	workflowID := chatID
	taskQueue := h.GetSharedTaskQueue()

	workflowOptions := client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                taskQueue,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
	}

	// Merge default inputs with provided inputs
	allInputs := map[string]interface{}{
		"chat": map[string]interface{}{
			"auto_approve": true,
			"id":           chatID,
		},
	}
	for k, v := range inputs {
		allInputs[k] = v
	}

	workflowInput := v2.WorkflowInput{
		ChatID:       chatID,
		WorkflowName: workflowName,
		Inputs:       allInputs,
	}

	run, err := h.TemporalClient.ExecuteWorkflow(ctx, workflowOptions, v2.DynamicWorkflow, workflowInput)
	require.NoError(t, err, "failed to start workflow from file")

	return run.GetRunID()
}

// ============================================================================
// MOCK CONFIGURATION HELPERS
// ============================================================================

// WithMockRun enables and configures the mock run executor
// Use this to intercept shell command execution in workflows
func (h *TestHarness) WithMockRun() *MockRunExecutor {
	return h.MockRun
}

// WithMockTools enables and configures the mock tool executor
// Use this to intercept tool calls in workflows
func (h *TestHarness) WithMockTools() *MockToolExecutor {
	return h.MockTools
}

// WithMockApproval enables and configures the mock approval responder
// Use this to automatically respond to approval requests in workflows
func (h *TestHarness) WithMockApproval() *MockApprovalResponder {
	return h.MockApproval
}

// EnableAutoApproval starts the mock approval responder with auto-approve
func (h *TestHarness) EnableAutoApproval() {
	h.MockApproval.AutoApprove()
	h.MockApproval.Start(context.Background())
}

// EnableAutoDeny starts the mock approval responder with auto-deny
func (h *TestHarness) EnableAutoDeny(reason string) {
	h.MockApproval.DenyAll(reason)
	h.MockApproval.Start(context.Background())
}

// ============================================================================
// ASSERTION HELPERS FOR MOCKS
// ============================================================================

// AssertRunCommandCalled asserts that a command matching the pattern was executed
func (h *TestHarness) AssertRunCommandCalled(t *testing.T, pattern string) {
	t.Helper()

	if !h.MockRun.WasCalled(pattern) {
		calls := h.MockRun.GetCalls()
		var cmds []string
		for _, call := range calls {
			cmds = append(cmds, call.Command)
		}
		t.Fatalf("expected run command matching %q to be called, but it was not. Called commands: %v", pattern, cmds)
	}
}

// AssertRunCommandNotCalled asserts that no command matching the pattern was executed
func (h *TestHarness) AssertRunCommandNotCalled(t *testing.T, pattern string) {
	t.Helper()

	if h.MockRun.WasCalled(pattern) {
		t.Fatalf("expected run command matching %q to NOT be called, but it was called %d times", pattern, h.MockRun.CallCountFor(pattern))
	}
}

// AssertRunCommandCallCount asserts the number of times a command was executed
func (h *TestHarness) AssertRunCommandCallCount(t *testing.T, pattern string, expected int) {
	t.Helper()

	actual := h.MockRun.CallCountFor(pattern)
	if actual != expected {
		t.Fatalf("expected run command matching %q to be called %d times, but it was called %d times", pattern, expected, actual)
	}
}

// AssertToolCalled asserts that a tool was called
// Optionally provide an argument matcher to check specific arguments
func (h *TestHarness) AssertToolCalled(t *testing.T, toolName string, argMatcher func(map[string]interface{}) bool) {
	t.Helper()

	if !h.MockTools.WasCalled(toolName) {
		t.Fatalf("expected tool %q to be called, but it was not. Called tools: %v", toolName, h.getCalledToolNames())
		return
	}

	// If argMatcher provided, check that at least one call matches
	if argMatcher != nil {
		calls := h.MockTools.GetCallsFor(toolName)
		for _, call := range calls {
			if argMatcher(call.Arguments) {
				return
			}
		}
		t.Fatalf("tool %q was called but no call matched the argument matcher", toolName)
	}
}

// AssertToolNotCalled asserts that a tool was not called
func (h *TestHarness) AssertToolNotCalled(t *testing.T, toolName string) {
	t.Helper()
	h.MockTools.AssertNotCalled(t, toolName)
}

// AssertToolCallCount asserts the number of times a tool was called
func (h *TestHarness) AssertToolCallCount(t *testing.T, toolName string, expected int) {
	t.Helper()
	h.MockTools.AssertCallCount(t, toolName, expected)
}

// getCalledToolNames returns a list of unique tool names that were called
func (h *TestHarness) getCalledToolNames() []string {
	calls := h.MockTools.GetCalls()
	seen := make(map[string]bool)
	var names []string
	for _, call := range calls {
		if !seen[call.Name] {
			seen[call.Name] = true
			names = append(names, call.Name)
		}
	}
	return names
}

// AssertApprovalHandled asserts that an approval with the given title was handled
func (h *TestHarness) AssertApprovalHandled(t *testing.T, titlePattern string) {
	t.Helper()
	h.MockApproval.AssertApprovalHandled(t, titlePattern)
}

// AssertApprovalApproved asserts that an approval was approved
func (h *TestHarness) AssertApprovalApproved(t *testing.T, titlePattern string) {
	t.Helper()

	if !h.MockApproval.WasApproved(titlePattern) {
		t.Fatalf("expected approval matching %q to be approved, but it was not", titlePattern)
	}
}

// AssertApprovalDenied asserts that an approval was denied
func (h *TestHarness) AssertApprovalDenied(t *testing.T, titlePattern string) {
	t.Helper()

	if !h.MockApproval.WasDenied(titlePattern) {
		t.Fatalf("expected approval matching %q to be denied, but it was not", titlePattern)
	}
}

// ============================================================================
// LOOP ITERATION TRACKING
// ============================================================================

// AssertLoopIterations asserts the number of loop iterations for a node
func (h *TestHarness) AssertLoopIterations(t *testing.T, nodeID string, expected int) {
	t.Helper()

	h.mu.Lock()
	actual := h.loopIterations[nodeID]
	h.mu.Unlock()

	if actual != expected {
		t.Fatalf("expected %d loop iterations for node %q, got %d", expected, nodeID, actual)
	}
}

// TrackLoopIteration records a loop iteration for a node
// This should be called by the test when it detects a loop iteration
func (h *TestHarness) TrackLoopIteration(nodeID string) {
	h.mu.Lock()
	h.loopIterations[nodeID]++
	h.mu.Unlock()
}

// GetLoopIterations returns the current iteration count for a node
func (h *TestHarness) GetLoopIterations(nodeID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.loopIterations[nodeID]
}

// ResetLoopIterations resets all loop iteration counters
func (h *TestHarness) ResetLoopIterations() {
	h.mu.Lock()
	h.loopIterations = make(map[string]int)
	h.mu.Unlock()
}

// ============================================================================
// WORKFLOW STATE HELPERS
// ============================================================================

// WaitForApproval waits for an approval to be created and returns it
func (h *TestHarness) WaitForApproval(t *testing.T, chatID string, timeout time.Duration) *db.Approval {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			t.Fatalf("timeout waiting for approval in chat %s", chatID)
			return nil
		case <-ticker.C:
			approvals, err := h.DB.ListPendingApprovalsByChat(ctx, chatID)
			if err != nil {
				continue
			}
			if len(approvals) > 0 {
				return approvals[0]
			}
		}
	}
}

// RespondToApproval manually responds to an approval
func (h *TestHarness) RespondToApproval(t *testing.T, approvalID string, approved bool, message string) {
	t.Helper()

	err := h.MockApproval.RespondManually(context.Background(), approvalID, approved, message)
	require.NoError(t, err, "failed to respond to approval")
}

// WaitForChatState waits for the chat to reach a specific state
func (h *TestHarness) WaitForChatState(t *testing.T, chatID string, state db.ChatState, timeout time.Duration) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			chat, _ := h.DB.GetChat(context.Background(), chatID)
			t.Fatalf("timeout waiting for chat state %q, current state: %q", state, chat.State)
		case <-ticker.C:
			chat, err := h.DB.GetChat(ctx, chatID)
			if err != nil {
				continue
			}
			if chat.State == state {
				return
			}
		}
	}
}

// WaitForPendingYield waits for a pending yield to appear for the given chat.
// This replaces WaitForChatState(NeedsAttention) since yield activity is now
// tracked via the yields table / activity view, not the chat state column.
func (h *TestHarness) WaitForPendingYield(t *testing.T, chatID string, timeout time.Duration) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			t.Fatalf("timeout waiting for pending yield on chat %s", chatID)
		case <-ticker.C:
			yield, err := h.DB.GetPendingYieldByChatID(ctx, chatID)
			if err != nil {
				continue
			}
			if yield != nil {
				return
			}
		}
	}
}

// ============================================================================
// GRPC SERVICE HELPERS
// These methods use the real production gRPC services instead of reimplementing
// behavior. This ensures tests exercise the same code paths as production.
// ============================================================================

// CreateChatOption configures CreateChatViaGRPC behavior
type CreateChatOption func(*createChatConfig)

type createChatConfig struct {
	workflow       string
	workflowParams map[string]*structpb.Value
	title          string
	attachments    []string
	noAutoModel    bool // Don't auto-inject model parameter
}

// WithWorkflow sets the workflow to use (default: "builtin://agent")
func WithWorkflow(name string) CreateChatOption {
	return func(c *createChatConfig) {
		c.workflow = name
	}
}

// WithNoAutoModel disables automatic model parameter injection
func WithNoAutoModel() CreateChatOption {
	return func(c *createChatConfig) {
		c.noAutoModel = true
	}
}

// WithWorkflowParam sets a workflow parameter
// Note: []string values are automatically converted to []interface{} for structpb compatibility
func WithWorkflowParam(key string, value interface{}) CreateChatOption {
	return func(c *createChatConfig) {
		// Convert []string to []interface{} - structpb.NewValue doesn't support []string
		if strSlice, ok := value.([]string); ok {
			iSlice := make([]interface{}, len(strSlice))
			for i, s := range strSlice {
				iSlice[i] = s
			}
			value = iSlice
		}
		v, err := structpb.NewValue(value)
		if err != nil {
			panic(fmt.Sprintf("WithWorkflowParam(%q): failed to convert value to structpb: %v", key, err))
		}
		c.workflowParams[key] = v
	}
}

// WithTitle sets the chat title
func WithTitle(title string) CreateChatOption {
	return func(c *createChatConfig) {
		c.title = title
	}
}

// WithAttachments sets file attachments for the initial message
func WithAttachments(attachments []string) CreateChatOption {
	return func(c *createChatConfig) {
		c.attachments = attachments
	}
}

// CreateChatViaGRPC creates a chat using the real ChatService.CreateChat.
// This exercises the same code path as production, ensuring tests catch
// any regressions in the gRPC layer.
//
// Returns the chat ID and workflow ID for further assertions.
func (h *TestHarness) CreateChatViaGRPC(t *testing.T, projectID, prompt string, opts ...CreateChatOption) (chatID, workflowID string) {
	t.Helper()

	// Apply options with defaults
	config := &createChatConfig{
		workflow:       "builtin://agent",
		workflowParams: make(map[string]*structpb.Value),
		title:          "E2E Test Chat",
	}
	for _, opt := range opts {
		opt(config)
	}

	// Always use mock model for tests (unless disabled)
	if !config.noAutoModel {
		config.workflowParams["model"] = structpb.NewStructValue(&structpb.Struct{
			Fields: map[string]*structpb.Value{
				"id": structpb.NewStringValue("mock"),
			},
		})
	}

	// Auto-approve mode for faster tests
	if _, ok := config.workflowParams["mode"]; !ok {
		config.workflowParams["mode"] = structpb.NewStringValue("auto")
	}

	// Always set a title to prevent GenerateTitle from consuming mock LLM responses
	if config.title == "" {
		config.title = "Test Chat"
	}

	// Build request
	req := &reliantv1.CreateChatRequest{
		ProjectId:      projectID,
		Workflow:       config.workflow,
		WorkflowParams: config.workflowParams,
		Messages: []*reliantv1.InputMessage{
			{Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Content: prompt},
		},
		Attachments: config.attachments,
	}

	if config.title != "" {
		req.Title = &config.title
	}

	// Inject user ID into context (required by auth.MustGetUserID)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, h.userID)

	// Call the real ChatService.CreateChat
	resp, err := h.chatService.CreateChat(ctx, connect.NewRequest(req))
	require.NoError(t, err, "ChatService.CreateChat failed")

	return resp.Msg.Chat.Id, resp.Msg.WorkflowId
}

// CreateProjectOnly creates a project without creating a chat.
// Use this when you need a project for CreateChatViaGRPC.
func (h *TestHarness) CreateProjectOnly(t *testing.T, name string) *db.Project {
	t.Helper()

	project := &db.Project{
		ID:     uuid.New().String(),
		Name:   name,
		Path:   h.tmpDir,
		UserID: h.userID,
	}

	err := h.DB.CreateProject(context.Background(), project)
	require.NoError(t, err, "failed to create project")

	return project
}

// StartAgentWorkflowViaGRPC is a convenience method that creates a project,
// creates a chat via the real gRPC service, and returns the chat ID.
// This is the preferred method for new tests.
func (h *TestHarness) StartAgentWorkflowViaGRPC(t *testing.T, prompt string, opts ...CreateChatOption) string {
	t.Helper()

	// Create a project for this test
	project := h.CreateProjectOnly(t, "test-project-"+uuid.New().String()[:8])

	// Create chat via real gRPC service
	chatID, _ := h.CreateChatViaGRPC(t, project.ID, prompt, opts...)

	return chatID
}

// StartWorkflowViaGRPC creates a project, creates a chat with a specific workflow,
// and returns the chat ID. Use this to test custom workflows.
// Note: Does not auto-inject model parameter - caller must provide if needed.
func (h *TestHarness) StartWorkflowViaGRPC(t *testing.T, workflowName string, workflowParams map[string]interface{}, prompt string) string {
	t.Helper()

	// Create a project for this test
	project := h.CreateProjectOnly(t, "test-project-"+uuid.New().String()[:8])

	// Build workflow params - start with provided params
	opts := []CreateChatOption{
		WithWorkflow(workflowName),
		WithNoAutoModel(), // Don't auto-inject model for custom workflows
	}
	for k, v := range workflowParams {
		opts = append(opts, WithWorkflowParam(k, v))
	}

	// Create chat via real gRPC service
	chatID, _ := h.CreateChatViaGRPC(t, project.ID, prompt, opts...)

	return chatID
}

// RunAgentWorkflowViaGRPCAndGetHistory starts the agent workflow via gRPC and returns its history.
// This is the preferred method for tests that need workflow history.
func (h *TestHarness) RunAgentWorkflowViaGRPCAndGetHistory(t *testing.T, prompt string, opts ...CreateChatOption) *WorkflowHistory {
	t.Helper()

	chatID := h.StartAgentWorkflowViaGRPC(t, prompt, opts...)
	h.WaitForWorkflowComplete(t, chatID)

	return h.GetWorkflowHistory(t, chatID)
}

// ============================================================================
// SEND MESSAGE VIA GRPC
// ============================================================================

// SendMessageOption configures SendMessageViaGRPC behavior
type SendMessageOption func(*sendMessageConfig)

type sendMessageConfig struct {
	workflowParams  map[string]*structpb.Value
	selectedPresets map[string]string
	attachments     []string
	targetThread    string
	yieldID         string
}

// WithSendWorkflowParam sets a workflow parameter for SendMessage
func WithSendWorkflowParam(key string, value interface{}) SendMessageOption {
	return func(c *sendMessageConfig) {
		v, _ := structpb.NewValue(value)
		c.workflowParams[key] = v
	}
}

// WithSendAttachments sets file attachments for the message
func WithSendAttachments(attachments []string) SendMessageOption {
	return func(c *sendMessageConfig) {
		c.attachments = attachments
	}
}

// WithTargetThread sets the target thread for the message
func WithTargetThread(thread string) SendMessageOption {
	return func(c *sendMessageConfig) {
		c.targetThread = thread
	}
}

// WithSendYieldID sets the yield ID to resolve when sending the message.
func WithSendYieldID(yieldID string) SendMessageOption {
	return func(c *sendMessageConfig) {
		c.yieldID = yieldID
	}
}

// WithSendSelectedPreset sets a selected preset update for SendMessage.
func WithSendSelectedPreset(target, presetName string) SendMessageOption {
	return func(c *sendMessageConfig) {
		if c.selectedPresets == nil {
			c.selectedPresets = make(map[string]string)
		}
		c.selectedPresets[target] = presetName
	}
}

// SendMessageViaGRPC sends a follow-up message to an existing chat via gRPC.
// This simulates the production SendMessage flow for multi-turn conversations.
// Returns the workflow ID from the response.
func (h *TestHarness) SendMessageViaGRPC(t *testing.T, chatID, prompt string, opts ...SendMessageOption) string {
	t.Helper()

	// Apply options with defaults
	config := &sendMessageConfig{
		workflowParams:  make(map[string]*structpb.Value),
		selectedPresets: make(map[string]string),
	}
	for _, opt := range opts {
		opt(config)
	}

	// Always use mock model for tests
	config.workflowParams["model"] = structpb.NewStructValue(&structpb.Struct{
		Fields: map[string]*structpb.Value{
			"id": structpb.NewStringValue("mock"),
		},
	})

	// Auto-approve mode for faster tests
	if _, ok := config.workflowParams["mode"]; !ok {
		config.workflowParams["mode"] = structpb.NewStringValue("auto")
	}

	// Build request
	req := &reliantv1.SendMessageRequest{
		ChatId:          chatID,
		WorkflowParams:  config.workflowParams,
		SelectedPresets: config.selectedPresets,
		Messages: []*reliantv1.InputMessage{
			{Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Content: prompt},
		},
		Attachments: config.attachments,
	}

	if config.targetThread != "" {
		req.TargetThread = &config.targetThread
	}
	if config.yieldID != "" {
		req.YieldId = &config.yieldID
	}

	// Inject user ID into context (required by auth.MustGetUserID)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, h.userID)

	// Call the real ChatService.SendMessage
	resp, err := h.chatService.SendMessage(ctx, connect.NewRequest(req))
	require.NoError(t, err, "ChatService.SendMessage failed")

	return resp.Msg.WorkflowId
}

// ResolveYieldViaGRPC resolves a pending yield using the YieldService RPC.
// This simulates the "Continue" button flow where the user clicks Continue
// without sending a message (as opposed to SendMessageViaGRPC with WithSendYieldID
// which simulates the reply path).
func (h *TestHarness) ResolveYieldViaGRPC(t *testing.T, yieldID, action string) {
	t.Helper()

	resp, err := h.yieldService.ResolveYield(context.Background(), connect.NewRequest(&reliantv1.ResolveYieldRequest{
		YieldId: yieldID,
		Action:  action,
	}))
	require.NoError(t, err, "YieldService.ResolveYield failed")
	require.True(t, resp.Msg.Success, "ResolveYield should return success=true")
}
