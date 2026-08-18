// Copyright (c) 2025 Reliant Labs
//
//go:build e2e

package stories

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"
	temporalclient "go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/configadapter"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/grpc/services"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workersetup"
	"github.com/reliant-labs/reliant/internal/workflow"
)

// ---------------------------------------------------------------------------
// Shared per-binary stack: Postgres repo + Temporal client + global registries
// ---------------------------------------------------------------------------

// Stack holds the resources shared by all stories in this test binary.
// Stories NEVER truncate the database (that happens once at init) — isolation
// comes from unique user/project/chat IDs, which is what makes the stories
// parallel-safe.
type Stack struct {
	Repo     *db.Repo
	Temporal temporalclient.Client
}

var (
	stackOnce sync.Once
	stack     *Stack
	stackErr  error
)

// requireStack returns the shared stack, initializing it on first use.
// Skips the test when DATABASE_URL is unset (same convention as internal/db).
func requireStack(t *testing.T) *Stack {
	t.Helper()

	if temporalDev.hostPort == "" {
		t.Skip("DATABASE_URL not set, skipping e2e story")
	}

	stackOnce.Do(func() {
		// One database per test binary, migrated + truncated once. Cleanup is
		// process-exit: the connection lives for the whole run.
		repo, _ := db.SetupTestDB(t)

		host, portStr, err := net.SplitHostPort(temporalDev.hostPort)
		if err != nil {
			stackErr = fmt.Errorf("bad temporal dev server address %q: %w", temporalDev.hostPort, err)
			return
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			stackErr = fmt.Errorf("bad temporal dev server port %q: %w", portStr, err)
			return
		}

		// Same client construction as the production api-server / worker
		// (FlexibleDataConverter, keepalive).
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		tc, err := temporal.NewExternalClient(ctx, temporal.ExternalClientConfig{
			Host:      host,
			Port:      port,
			Namespace: temporalNamespace,
			LogLevel:  "silent",
		})
		if err != nil {
			stackErr = fmt.Errorf("dial temporal dev server: %w", err)
			return
		}

		// Global registries, mirroring serverworker.Run.
		if err := models.InitGlobalRegistryWithUserConfig(nil); err != nil {
			stackErr = fmt.Errorf("init model registry: %w", err)
			return
		}
		drivers.InitializeAPIKeyProvider(repo)

		stack = &Stack{Repo: repo, Temporal: tc}
	})

	if stackErr != nil {
		t.Fatalf("shared e2e stack failed to initialize: %v", stackErr)
	}
	if stack == nil {
		t.Fatal("shared e2e stack failed to initialize in an earlier test")
	}
	return stack
}

// ---------------------------------------------------------------------------
// Per-story harness
// ---------------------------------------------------------------------------

// Harness is one story's isolated slice of the stack: its own user, project,
// scripted LLM, Temporal worker (unique task queue), and service handlers.
type Harness struct {
	T     *testing.T
	Stack *Stack

	UserID      string
	ProjectID   string
	ProjectPath string

	LLM *ScriptedLLM

	ChatSvc     *services.ChatService
	QuestionSvc *services.QuestionService
	Pause       *workflow.PauseService

	// Ctx carries the story's user identity, exactly as the auth interceptor
	// would have injected it.
	Ctx context.Context
}

type harnessConfig struct {
	toolExecutor toolexec.ToolExecutor
}

// HarnessOption customizes harness construction.
type HarnessOption func(*harnessConfig)

// WithToolExecutor swaps the tool executor the worker uses (default: real
// local execution via LocalToolExecutor + daemon.LocalClient).
func WithToolExecutor(exec toolexec.ToolExecutor) HarnessOption {
	return func(c *harnessConfig) { c.toolExecutor = exec }
}

// newHarness stands up a story-scoped environment. Everything registered here
// is torn down via t.Cleanup.
func newHarness(t *testing.T, llmScript *ScriptedLLM, opts ...HarnessOption) *Harness {
	t.Helper()
	s := requireStack(t)

	cfg := &harnessConfig{}
	for _, o := range opts {
		o(cfg)
	}

	userID := "e2e-user-" + shortID()
	projectID := uuid.New().String()
	projectPath := t.TempDir()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	now := time.Now().UTC()
	require.NoError(t, s.Repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		Name:       "e2e-" + t.Name(),
		Path:       projectPath,
		UserID:     userID,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}), "create story project")
	require.NoError(t, s.Repo.CreateWorktree(ctx, &db.Worktree{
		ID:         uuid.New().String(),
		Name:       "main",
		Path:       projectPath,
		Branch:     "main",
		BaseBranch: "main",
		ProjectID:  projectID,
		IsMain:     true,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}), "create story project's main worktree")

	toolsFactory := tools.NewToolsFactory(&tools.ToolsOptions{Repo: s.Repo})

	executor := cfg.toolExecutor
	if executor == nil {
		executor = newLocalDaemonExecutor(toolsFactory)
	}

	resolver := func(ctx context.Context, userID string, prefs models.Preferences, o ...llm.DriverOption) (llm.Driver, error) {
		return llmScript, nil
	}

	hub := noopStreamingHub{}
	taskQueueSuffix := shortID()

	handle, _, err := workersetup.StartWorker(&workersetup.Config{
		TemporalClient:  s.Temporal,
		Database:        s.Repo,
		StreamingHub:    hub,
		ToolsFactory:    toolsFactory,
		ToolExecutor:    executor,
		DaemonRouter:    nil, // hermetic: no daemon transport; worktree ops unavailable
		MCPBinder:       toolexec.NewLocalMCPContextBinder(mcp.NewManager()),
		ConfigProvider:  config.NewStoredConfigProvider(configadapter.NewRepoConfigStore(s.Repo)),
		DriverResolver:  resolver,
		TaskQueueSuffix: taskQueueSuffix,
	})
	require.NoError(t, err, "start story worker")
	t.Cleanup(func() {
		handle.Worker.Stop()
		select {
		case <-handle.Done:
		case <-time.After(10 * time.Second):
			t.Log("worker did not stop within 10s")
		}
	})

	// Wait until the worker's pollers are registered with Temporal. This both
	// avoids no-poller scheduling latency on fast stories and guarantees the
	// worker got past startup before a fast story's cleanup calls Stop
	// (worker.Run racing worker.Stop during startup trips the race detector).
	waitForWorkerPollers(t, s.Temporal, workersetup.TaskQueueName(taskQueueSuffix))

	pause := workflow.NewPauseService(s.Temporal, s.Repo)
	// nil daemonRouter: these stories run without a daemon, and the router is
	// only used for the greenfield code-presence probe, which skips itself when
	// it is nil.
	chatSvc := services.NewChatService(s.Repo, s.Temporal, pause, workersetup.TaskQueueName(taskQueueSuffix), hub, nil)
	questionSvc := services.NewQuestionService(s.Repo, pause)

	return &Harness{
		T:           t,
		Stack:       s,
		UserID:      userID,
		ProjectID:   projectID,
		ProjectPath: projectPath,
		LLM:         llmScript,
		ChatSvc:     chatSvc,
		QuestionSvc: questionSvc,
		Pause:       pause,
		Ctx:         ctx,
	}
}

// waitForWorkerPollers blocks until both workflow and activity pollers for
// the task queue are visible to the Temporal server.
func waitForWorkerPollers(t *testing.T, c temporalclient.Client, taskQueue string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, tqType := range []enums.TaskQueueType{
		enums.TASK_QUEUE_TYPE_WORKFLOW,
		enums.TASK_QUEUE_TYPE_ACTIVITY,
	} {
		for {
			resp, err := c.DescribeTaskQueue(ctx, taskQueue, tqType)
			if err == nil && len(resp.GetPollers()) > 0 {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatalf("worker pollers for %s (%s) never registered: %v", taskQueue, tqType, err)
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Story actions
// ---------------------------------------------------------------------------

// CreateChat drives the production CreateChat handler. workflowRef is e.g.
// "builtin://agent". params are workflow inputs; a {"model": {"id": "mock"}}
// entry is added when the caller didn't specify one.
func (h *Harness) CreateChat(workflowRef, prompt string, params map[string]any) *reliantv1.CreateChatResponse {
	h.T.Helper()
	resp, err := h.TryCreateChat(workflowRef, prompt, params)
	require.NoError(h.T, err, "CreateChat")
	require.NotNil(h.T, resp.Msg.Chat)
	return resp.Msg
}

// TryCreateChat is CreateChat without asserting success (for validation-error
// stories).
func (h *Harness) TryCreateChat(workflowRef, prompt string, params map[string]any) (*connect.Response[reliantv1.CreateChatResponse], error) {
	h.T.Helper()

	if params == nil {
		params = map[string]any{}
	}
	if _, ok := params["model"]; !ok {
		params["model"] = map[string]any{"id": "mock"}
	}

	protoParams := make(map[string]*structpb.Value, len(params))
	for k, v := range params {
		pv, err := structpb.NewValue(v)
		require.NoError(h.T, err, "workflow param %q must be structpb-encodable", k)
		protoParams[k] = pv
	}

	req := connect.NewRequest(&reliantv1.CreateChatRequest{
		ProjectId: h.ProjectID,
		Workflow:  workflowRef,
		Messages: []*reliantv1.InputMessage{
			{Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Content: prompt},
		},
		WorkflowParams: protoParams,
	})
	return h.ChatSvc.CreateChat(h.Ctx, req)
}

// SendMessage drives the production SendMessage handler (used to resume
// paused workflows and continue conversations).
func (h *Harness) SendMessage(chatID, prompt string) *reliantv1.SendMessageResponse {
	h.T.Helper()
	resp, err := h.ChatSvc.SendMessage(h.Ctx, connect.NewRequest(&reliantv1.SendMessageRequest{
		ChatId: chatID,
		Messages: []*reliantv1.InputMessage{
			{Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Content: prompt},
		},
	}))
	require.NoError(h.T, err, "SendMessage")
	return resp.Msg
}

// ResolveQuestion answers a pending ask_question through the production
// QuestionService handler. Passing freetext != "" (or a selection other than
// "Continue") means "has feedback" and the loop continues.
//
// NOTE / BUG FOUND BY THIS SUITE: the response payload needs BOTH shapes today:
//   - "answers": parsed by the workflow (parseQuestionResponse in
//     internal/workflow/runtime/inline_workflow_executor.go) to compute
//     has_feedback / response.
//   - "reply": parsed by QuestionService.saveUserReplyMessage
//     (internal/grpc/services/question.go) to persist the feedback as a user
//     message in the thread.
//
// The web client (ChatInput.handleQuestionSubmit) only sends "answers", so
// freetext feedback is NEVER saved to the thread in production; the next
// CallLLM then fails "conversation history ends with assistant message" and
// the workflow self-pauses after retry exhaustion. TODO: fix
// saveUserReplyMessage to extract answers[0].freetext (or teach the client to
// send "reply"), then drop the duplicated "reply" key here to pin the fix.
func (h *Harness) ResolveQuestion(questionID string, selected []string, freetext string) {
	h.T.Helper()
	answer := map[string]any{
		"answers": []any{
			map[string]any{
				"question": "The workflow is ready to continue. What would you like to do?",
				"selected": selected,
				"freetext": freetext,
			},
		},
	}
	if freetext != "" {
		answer["reply"] = freetext
	}
	data, err := json.Marshal(answer)
	require.NoError(h.T, err)
	dataStr := string(data)

	_, err = h.QuestionSvc.ResolveQuestion(h.Ctx, connect.NewRequest(&reliantv1.ResolveQuestionRequest{
		QuestionId:   questionID,
		Action:       "reply",
		ResponseData: &dataStr,
	}))
	require.NoError(h.T, err, "ResolveQuestion")
}

// ---------------------------------------------------------------------------
// Assertions / waiting
// ---------------------------------------------------------------------------

const (
	waitTimeout  = 45 * time.Second
	pollInterval = 100 * time.Millisecond
)

// eventually polls cond until it returns true or the timeout elapses. The
// last detail string is included in the failure message.
func (h *Harness) eventually(what string, cond func() (bool, string)) {
	h.T.Helper()
	deadline := time.Now().Add(waitTimeout)
	var last string
	for time.Now().Before(deadline) {
		ok, detail := cond()
		if ok {
			return
		}
		last = detail
		time.Sleep(pollInterval)
	}
	h.T.Fatalf("timed out after %s waiting for %s (last state: %s)", waitTimeout, what, last)
}

// WaitWorkflowStatus polls the workflows table (written by the production
// WorkflowStatus activity) until the workflow reaches the wanted status.
func (h *Harness) WaitWorkflowStatus(workflowID string, want db.WorkflowStatus) {
	h.T.Helper()
	h.eventually(fmt.Sprintf("workflow %s to reach status %s", workflowID, want), func() (bool, string) {
		wf, err := h.Stack.Repo.GetWorkflow(h.Ctx, workflowID)
		if err != nil || wf == nil {
			return false, fmt.Sprintf("get workflow: %v", err)
		}
		return wf.Status == want, fmt.Sprintf("status=%s", wf.Status)
	})
}

// WaitTemporalWorkflowDone blocks until the Temporal execution finishes and
// returns nil error. Fails the story if the workflow errored.
func (h *Harness) WaitTemporalWorkflowDone(workflowID string) {
	h.T.Helper()
	ctx, cancel := context.WithTimeout(h.Ctx, waitTimeout)
	defer cancel()
	run := h.Stack.Temporal.GetWorkflow(ctx, workflowID, "")
	require.NoError(h.T, run.Get(ctx, nil), "temporal workflow %s should complete cleanly", workflowID)
}

// WorkflowResult waits for the Temporal execution to finish and returns the
// DynamicWorkflow outputs.
func (h *Harness) WorkflowResult(workflowID string) map[string]interface{} {
	h.T.Helper()
	ctx, cancel := context.WithTimeout(h.Ctx, waitTimeout)
	defer cancel()
	var result struct {
		Outputs map[string]interface{} `json:"outputs"`
	}
	run := h.Stack.Temporal.GetWorkflow(ctx, workflowID, "")
	require.NoError(h.T, run.Get(ctx, &result), "temporal workflow %s should complete cleanly", workflowID)
	return result.Outputs
}

// WaitPendingQuestion polls until the chat has a pending ask_question.
func (h *Harness) WaitPendingQuestion(chatID string) *db.Question {
	h.T.Helper()
	var q *db.Question
	h.eventually("pending question on chat "+chatID, func() (bool, string) {
		got, err := h.Stack.Repo.GetPendingQuestionByChatID(h.Ctx, chatID)
		if err != nil || got == nil {
			return false, fmt.Sprintf("pending question: %v", err)
		}
		q = got
		return true, ""
	})
	return q
}

// Messages returns the chat's messages on the root thread, ordinal-ordered,
// with content blocks attached.
type MessageWithBlocks struct {
	*db.Message
	Blocks []*db.MessageContentBlock
}

func (h *Harness) Messages(chatID, thread string) []MessageWithBlocks {
	h.T.Helper()
	msgs, err := h.Stack.Repo.ListMessages(h.Ctx, chatID, db.MessageListOptions{Thread: &thread})
	require.NoError(h.T, err, "list messages")
	out := make([]MessageWithBlocks, 0, len(msgs))
	for _, m := range msgs {
		blocks, err := h.Stack.Repo.ListContentBlocks(h.Ctx, m.ID)
		require.NoError(h.T, err, "list content blocks for %s", m.ID)
		out = append(out, MessageWithBlocks{Message: m, Blocks: blocks})
	}
	return out
}

// TextOf concatenates the text blocks of a message.
func TextOf(m MessageWithBlocks) string {
	var parts []string
	for _, b := range m.Blocks {
		if b.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT && b.Content != nil {
			parts = append(parts, *b.Content)
		}
	}
	return strings.Join(parts, "\n")
}

// Chat fetches the chat row.
func (h *Harness) Chat(chatID string) *db.Chat {
	h.T.Helper()
	chat, err := h.Stack.Repo.GetChat(h.Ctx, chatID)
	require.NoError(h.T, err, "get chat")
	return chat
}

// ---------------------------------------------------------------------------
// Local tool execution (the hermetic "daemon")
// ---------------------------------------------------------------------------

// localDaemonExecutor implements toolexec.ToolExecutor by executing every
// tool in-process through the SAME code path the daemon runtime uses:
// LocalToolExecutor with a daemon.LocalClient (see
// internal/toolexec/daemonruntime/runtime.go). This makes bash/fs tools run
// for real against the story's temp project directory — hermetic, but not
// mocked.
type localDaemonExecutor struct {
	inner  *toolexec.LocalToolExecutor
	client daemon.Client
}

func newLocalDaemonExecutor(factory *tools.ToolsFactory) *localDaemonExecutor {
	inner := toolexec.NewLocalToolExecutor(factory)
	client := daemon.NewLocalClient()
	inner.SetDaemonClient(client)
	return &localDaemonExecutor{inner: inner, client: client}
}

func (e *localDaemonExecutor) ExecuteTool(ctx context.Context, req *toolexec.ToolRequest) (*toolexec.ToolResult, error) {
	start := time.Now()
	timeoutMs := 0
	if req.Timeout > 0 {
		timeoutMs = int(req.Timeout.Milliseconds())
	}

	// Mirrors RemoteExecutor.executeOnServer's context construction.
	contextMap := map[string]interface{}{
		"user_id":    req.UserID,
		"chat_id":    req.ChatID,
		"thread":     req.Thread,
		"message_id": req.MessageID,
		"project": map[string]interface{}{
			"id":   req.ProjectID,
			"path": req.ProjectPath,
			"name": req.ProjectName,
		},
	}
	if req.WorktreePath != "" {
		contextMap["worktree"] = map[string]interface{}{
			"id":   req.WorktreeID,
			"path": req.WorktreePath,
		}
	}

	res := e.inner.ExecuteToolWithDaemon(ctx, req.ToolName, req.ToolInput, req.ToolCallID, timeoutMs, contextMap, e.client)
	return &toolexec.ToolResult{
		Success:      res.Success,
		IsError:      res.IsError,
		Backgrounded: res.Backgrounded,
		Content:      res.Content,
		Metadata:     res.Metadata,
		BinaryParts:  res.BinaryParts,
		StartTime:    start,
		EndTime:      time.Now(),
		ErrorMessage: res.ErrorMessage,
		ErrorCode:    res.ErrorCode,
	}, nil
}

func (e *localDaemonExecutor) Close() error { return nil }

// ---------------------------------------------------------------------------
// No-op streaming hub (stories assert on persisted state, not deltas)
// ---------------------------------------------------------------------------

type noopStreamingHub struct{}

func (noopStreamingHub) Publish(ctx context.Context, chatID string, delta streaming.StreamingDelta) {
}
func (noopStreamingHub) PublishEvent(ctx context.Context, event streaming.ChatEvent) {}
func (noopStreamingHub) Subscribe(ctx context.Context, chatID string) streaming.Subscription {
	return noopSubscription{}
}
func (noopStreamingHub) SubscriberCount(chatID string) int { return 0 }
func (noopStreamingHub) TotalSubscriberCount() int         { return 0 }
func (noopStreamingHub) Stats() streaming.HubStats         { return streaming.HubStats{} }
func (noopStreamingHub) IsConnected() bool                 { return true }
func (noopStreamingHub) Close() error                      { return nil }

type noopSubscription struct{}

func (noopSubscription) Events() <-chan streaming.StreamingDelta { return nil }
func (noopSubscription) Unsubscribe()                            {}

// ---------------------------------------------------------------------------
// Misc
// ---------------------------------------------------------------------------

func shortID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
