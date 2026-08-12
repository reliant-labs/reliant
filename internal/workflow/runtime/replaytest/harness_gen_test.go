// Copyright (c) 2025 Reliant Labs
//
//go:build replayfixtures

package replaytest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	enums "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	temporalclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"
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
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workersetup"
	"github.com/reliant-labs/reliant/internal/workflow"
)

// This harness is a trimmed replica of the e2e story harness
// (e2e/stories/main_test.go + harness_test.go). That package is test-only and
// build-tagged, so it is not importable; the minimal pieces needed to drive
// real workflow executions through the PRODUCTION paths (CreateChat handler →
// workersetup.StartWorker worker → scripted LLM → local tool execution) are
// replicated here. Fixture generation must run the production registration
// path so the captured histories are representative of production runs.

func init() { generatorMode = true }

// temporalDev holds the shared ephemeral Temporal dev server for the whole
// generator binary.
var temporalDev struct {
	server   *testsuite.DevServer
	hostPort string
}

const temporalNamespace = "reliant"

func setupQuietLogging() {
	if os.Getenv("E2E_VERBOSE") == "1" {
		return
	}
	log.SetOutput(io.Discard)
	silent := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelError + 10,
	}))
	slog.SetDefault(silent)
	logging.DefaultOutput = io.Discard
	logging.Setup(slog.LevelError + 10)
}

func TestMain(m *testing.M) {
	setupQuietLogging()

	// Fixture generation is a deliberate act (make replay-fixtures), not part
	// of the normal test suite — fail loudly instead of skipping.
	if os.Getenv("DATABASE_URL") == "" {
		fmt.Fprintln(os.Stderr, "replaytest generator: DATABASE_URL is required.")
		fmt.Fprintln(os.Stderr, "Run via `make replay-fixtures` (brings up Postgres through docker compose), or set")
		fmt.Fprintln(os.Stderr, "DATABASE_URL=postgres://postgres:postgres@localhost:5433/reliant?sslmode=disable")
		os.Exit(1)
	}

	if err := startTemporalDevServer(); err != nil {
		fmt.Fprintf(os.Stderr, "replaytest generator: failed to start Temporal dev server: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	if temporalDev.server != nil {
		_ = temporalDev.server.Stop()
	}
	os.Exit(code)
}

// startTemporalDevServer boots an ephemeral (in-memory) Temporal dev server,
// same pattern as e2e/stories.
func startTemporalDevServer() error {
	opts := testsuite.DevServerOptions{
		LogLevel: "never",
		ExtraArgs: []string{
			"--dynamic-config-value", "system.forceSearchAttributesCacheRefreshOnRead=true",
		},
	}
	if path, err := exec.LookPath("temporal"); err == nil {
		opts.ExistingPath = path
	}
	opts.ClientOptions = &temporalclient.Options{Namespace: temporalNamespace}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	server, err := testsuite.StartDevServer(ctx, opts)
	if err != nil {
		return err
	}
	temporalDev.server = server
	temporalDev.hostPort = server.FrontendHostPort()

	conn, err := net.DialTimeout("tcp", temporalDev.hostPort, 10*time.Second)
	if err != nil {
		_ = server.Stop()
		return fmt.Errorf("temporal dev server not reachable at %s: %w", temporalDev.hostPort, err)
	}
	_ = conn.Close()
	return nil
}

// ---------------------------------------------------------------------------
// Shared per-binary stack
// ---------------------------------------------------------------------------

type Stack struct {
	Repo     *db.Repo
	Temporal temporalclient.Client
}

var (
	stackOnce sync.Once
	stack     *Stack
	stackErr  error
)

func requireStack(t *testing.T) *Stack {
	t.Helper()

	stackOnce.Do(func() {
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
		t.Fatalf("shared generator stack failed to initialize: %v", stackErr)
	}
	if stack == nil {
		t.Fatal("shared generator stack failed to initialize in an earlier test")
	}
	return stack
}

// ---------------------------------------------------------------------------
// Per-scenario harness
// ---------------------------------------------------------------------------

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

	Ctx context.Context
}

func newHarness(t *testing.T, llmScript *ScriptedLLM) *Harness {
	t.Helper()
	s := requireStack(t)

	userID := "replayfix-user-" + shortID()
	projectID := uuid.New().String()
	projectPath := t.TempDir()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	now := time.Now().UTC()
	require.NoError(t, s.Repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		Name:       "replayfix-" + t.Name(),
		Path:       projectPath,
		UserID:     userID,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}), "create scenario project")

	// CreateChat requires every project to have a main worktree
	// (resolveChatWorktreeID in chat_helpers.go) — an omitted worktree id on
	// CreateChat resolves to it. Production project creation provisions this
	// automatically; this harness must do the same or every CreateChat below
	// fails FailedPrecondition.
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
	}), "create scenario project's main worktree")

	toolsFactory := tools.NewToolsFactory(&tools.ToolsOptions{Repo: s.Repo})
	executor := newLocalDaemonExecutor(toolsFactory)

	resolver := func(ctx context.Context, userID string, prefs models.Preferences, o ...llm.DriverOption) (llm.Driver, error) {
		return llmScript, nil
	}

	hub := noopStreamingHub{}
	taskQueueSuffix := shortID()

	// Production worker registration path — the same code the real worker
	// binary runs. This is what makes the captured histories representative.
	handle, _, err := workersetup.StartWorker(&workersetup.Config{
		TemporalClient:  s.Temporal,
		Database:        s.Repo,
		StreamingHub:    hub,
		ToolsFactory:    toolsFactory,
		ToolExecutor:    executor,
		DaemonRouter:    nil, // hermetic: no daemon transport
		MCPBinder:       toolexec.NewLocalMCPContextBinder(mcp.NewManager()),
		ConfigProvider:  config.NewStoredConfigProvider(configadapter.NewRepoConfigStore(s.Repo)),
		DriverResolver:  resolver,
		TaskQueueSuffix: taskQueueSuffix,
	})
	require.NoError(t, err, "start scenario worker")
	t.Cleanup(func() {
		handle.Worker.Stop()
		select {
		case <-handle.Done:
		case <-time.After(10 * time.Second):
			t.Log("worker did not stop within 10s")
		}
	})

	waitForWorkerPollers(t, s.Temporal, workersetup.TaskQueueName(taskQueueSuffix))

	pause := workflow.NewPauseService(s.Temporal, s.Repo)
	chatSvc := services.NewChatService(s.Repo, s.Temporal, pause, workersetup.TaskQueueName(taskQueueSuffix), hub)
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
// Scenario actions
// ---------------------------------------------------------------------------

// CreateChat drives the production CreateChat handler.
func (h *Harness) CreateChat(workflowRef, prompt string, params map[string]any) *reliantv1.CreateChatResponse {
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
	resp, err := h.ChatSvc.CreateChat(h.Ctx, req)
	require.NoError(h.T, err, "CreateChat")
	require.NotNil(h.T, resp.Msg.Chat)
	return resp.Msg
}

// ResolveQuestion answers a pending ask_question through the production
// QuestionService handler (same double-shape payload as the e2e stories —
// "answers" for the workflow, "reply" for thread persistence).
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
// Waiting
// ---------------------------------------------------------------------------

const (
	waitTimeout  = 60 * time.Second
	pollInterval = 100 * time.Millisecond
)

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

// WaitTemporalWorkflowDone blocks until the Temporal execution finishes
// cleanly.
func (h *Harness) WaitTemporalWorkflowDone(workflowID string) {
	h.T.Helper()
	ctx, cancel := context.WithTimeout(h.Ctx, waitTimeout)
	defer cancel()
	run := h.Stack.Temporal.GetWorkflow(ctx, workflowID, "")
	require.NoError(h.T, run.Get(ctx, nil), "temporal workflow %s should complete cleanly", workflowID)
}

// WaitWorkflowStatus polls the workflows table until the wanted status.
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

// ---------------------------------------------------------------------------
// History export
// ---------------------------------------------------------------------------

// ExportHistory fetches the full event history of the workflow's latest run
// and writes it as protojson (the `temporal workflow show --output json`
// compatible format that worker.ReplayWorkflowHistoryFromJSONFile consumes)
// to fixtures/<name>.json.
//
// Determinism note: protojson deliberately randomizes whitespace between
// runs, so the output is compact-marshaled and re-indented with
// encoding/json to keep the file layout stable. Event CONTENT (timestamps,
// run IDs, DB-generated IDs inside payloads) necessarily differs between
// generation runs — see fixtures/README.md.
func (h *Harness) ExportHistory(workflowID, name string) {
	h.T.Helper()

	ctx, cancel := context.WithTimeout(h.Ctx, 30*time.Second)
	defer cancel()

	iter := h.Stack.Temporal.GetWorkflowHistory(ctx, workflowID, "", false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	var events []*historypb.HistoryEvent
	for iter.HasNext() {
		event, err := iter.Next()
		require.NoError(h.T, err, "iterate history for %s", workflowID)
		events = append(events, event)
	}
	require.NotEmpty(h.T, events, "workflow %s must have history events", workflowID)

	hist := &historypb.History{Events: events}
	raw, err := protojson.Marshal(hist)
	require.NoError(h.T, err, "marshal history for %s", workflowID)

	// Normalize whitespace: protojson injects non-deterministic spacing.
	var compact bytes.Buffer
	require.NoError(h.T, json.Compact(&compact, raw))
	var indented bytes.Buffer
	require.NoError(h.T, json.Indent(&indented, compact.Bytes(), "", "  "))
	indented.WriteByte('\n')

	path := filepath.Join("fixtures", name+".json")
	require.NoError(h.T, os.WriteFile(path, indented.Bytes(), 0o644), "write fixture %s", path)

	h.T.Logf("fixture %s: %d history events, %d bytes", path, len(events), indented.Len())
}

// ---------------------------------------------------------------------------
// Local tool execution (hermetic "daemon"), replica of e2e/stories
// ---------------------------------------------------------------------------

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
// No-op streaming hub
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
