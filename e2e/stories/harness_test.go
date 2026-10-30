// Copyright (c) 2025 Reliant Labs
//
//go:build e2e

package stories

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	temporalclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/configadapter"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/handlers/approval"
	"github.com/reliant-labs/reliant/internal/handlers/chat"
	"github.com/reliant-labs/reliant/internal/handlers/question"
	"github.com/reliant-labs/reliant/internal/handlers/trigger"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workersetup"
	"github.com/reliant-labs/reliant/internal/workflow"
	typedconfig "github.com/reliant-labs/reliant/pkg/config"
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

// storyTemporalClient keeps production's shared-queue starts on this story's
// worker. All other client operations and explicitly different queues pass through.
// Queue isolation belongs to the fixture, not the production chat handler's Deps.
type storyTemporalClient struct {
	temporalclient.Client
	taskQueue string
}

func (c storyTemporalClient) ExecuteWorkflow(ctx context.Context, options temporalclient.StartWorkflowOptions, wf interface{}, args ...interface{}) (temporalclient.WorkflowRun, error) {
	if options.TaskQueue == workflow.SharedTaskQueue {
		options.TaskQueue = c.taskQueue
	}
	return c.Client.ExecuteWorkflow(ctx, options, wf, args...)
}

// forStory copies the stack: parallel stories must never replace the shared
// client's routing or each other's worker queue.
func (s *Stack) forStory(taskQueue string) *Stack {
	story := *s
	story.Temporal = storyTemporalClient{Client: s.Temporal, taskQueue: taskQueue}
	return &story
}

type workflowStartRecorder struct {
	temporalclient.Client
	start func(context.Context, temporalclient.StartWorkflowOptions, interface{}, ...interface{}) (temporalclient.WorkflowRun, error)
}

func (c *workflowStartRecorder) ExecuteWorkflow(ctx context.Context, options temporalclient.StartWorkflowOptions, wf interface{}, args ...interface{}) (temporalclient.WorkflowRun, error) {
	return c.start(ctx, options, wf, args...)
}

func TestStoryTemporalClientQueueIsolation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wantRun := &struct{ temporalclient.WorkflowRun }{}
	wantErr := errors.New("start failed")
	baseClient := &workflowStartRecorder{}
	shared := &Stack{Repo: &db.Repo{}, Temporal: baseClient}
	story := shared.forStory("story-queue")
	otherStory := shared.forStory("other-story-queue")
	require.Same(t, baseClient, shared.Temporal, "shared stack must not be mutated")
	require.Same(t, shared.Repo, story.Repo)
	require.Same(t, shared.Repo, otherStory.Repo)

	for _, tc := range []struct {
		name, queue, wantQueue string
		stack                  *Stack
	}{
		{"shared queue", workflow.SharedTaskQueue, "story-queue", story},
		{"explicit queue", "explicit-queue", "explicit-queue", story},
		{"empty queue", "", "", story},
		{"other story", workflow.SharedTaskQueue, "other-story-queue", otherStory},
		{"shared stack", workflow.SharedTaskQueue, workflow.SharedTaskQueue, shared},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := temporalclient.StartWorkflowOptions{
				ID: "workflow-id", TaskQueue: tc.queue,
				WorkflowExecutionTimeout: time.Minute,
				Memo:                     map[string]interface{}{"story": "memo"},
			}
			wantOptions := options
			wantOptions.TaskQueue = tc.wantQueue
			called := false
			baseClient.start = func(gotCtx context.Context, got temporalclient.StartWorkflowOptions, wf interface{}, args ...interface{}) (temporalclient.WorkflowRun, error) {
				called = true
				require.Same(t, ctx, gotCtx)
				require.Equal(t, wantOptions, got, "only the shared queue may change")
				require.Equal(t, "test-workflow", wf)
				require.Equal(t, []interface{}{"input", 42}, args)
				return wantRun, wantErr
			}
			run, err := tc.stack.Temporal.ExecuteWorkflow(ctx, options, "test-workflow", "input", 42)
			require.True(t, called)
			require.Same(t, wantRun, run)
			require.ErrorIs(t, err, wantErr)
			require.Equal(t, tc.queue, options.TaskQueue, "caller options must not be mutated")
		})
	}
}

var (
	stackOnce sync.Once
	stack     *Stack
	stackErr  error
)

// requireStack returns the shared stack, initializing it on first use.
func requireStack(t *testing.T) *Stack {
	t.Helper()

	if temporalDev.hostPort == "" {
		t.Fatal("no Temporal dev server: TestMain starts it before any story runs")
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

		// Global registries, mirroring the worker root (internal/app ProcessSettings).
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

	ChatSvc     *chat.Service
	QuestionSvc *question.Service
	ApprovalSvc *approval.Service
	// TriggerSvc is the production TriggerService over the story's Temporal:
	// a fire goes through the real fire workflow, Firer and launcher.
	TriggerSvc *trigger.Service
	Pause      *workflow.PauseService

	// Ctx carries the story's user identity, exactly as the auth interceptor
	// would have injected it.
	Ctx context.Context

	// progress counts the story worker's activity starts and finishes: what
	// the story's waits watch to tell a slow story from a stuck one.
	progress *atomic.Int64
}

type harnessConfig struct {
	toolExecutor toolexec.ToolExecutor
	mcpBinder    toolexec.MCPContextBinder
}

// WithMCPBinder swaps the MCP binder call_llm discovers MCP tools through.
// The default binds no MCP servers (noMCPServers).
func WithMCPBinder(binder toolexec.MCPContextBinder) HarnessOption {
	return func(c *harnessConfig) { c.mcpBinder = binder }
}

// HarnessOption customizes harness construction.
type HarnessOption func(*harnessConfig)

// WithToolExecutor swaps the tool executor the worker uses (default: real
// local execution via LocalToolExecutor + daemon.LocalClient).
func WithToolExecutor(exec toolexec.ToolExecutor) HarnessOption {
	return func(c *harnessConfig) { c.toolExecutor = exec }
}

// noMCPServers is the stories' default MCP binder: it binds none.
//
// The default used to be a real mcp.Manager, and a real manager connects its
// builtin servers: reliant-docs over HTTPS (https://reliantlabs.mintlify.app)
// and, wherever Chrome is installed, chrome-devtools — `npx -y
// chrome-devtools-mcp`, a node process that launches headless Chrome. So every
// story's first call_llm reached the internet and started a browser, and how
// long that took was the host's business: on the release-gate host, with
// sixteen stories at once, the chrome-devtools tools/list ran out its timeout
// (45s+) inside call_llm, and every story stalled at its seventh activity —
// "no progress for 45s" — with nothing wrong in the code under test. The
// stories test chats, tools and pauses, not MCP; MCP has its own tests.
var noMCPServers = toolexec.MCPContextBinderFunc(func(tc *rctx.ToolContext) *rctx.ToolContext { return tc })

// newHarness stands up a story-scoped environment. Everything registered here
// is torn down via t.Cleanup.
func newHarness(t *testing.T, llmScript *ScriptedLLM, opts ...HarnessOption) *Harness {
	t.Helper()
	taskQueueSuffix := shortID()
	taskQueue := workersetup.TaskQueueName(taskQueueSuffix)
	shared := requireStack(t)
	s := shared.forStory(taskQueue)

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

	mcpBinder := cfg.mcpBinder
	if mcpBinder == nil {
		mcpBinder = noMCPServers
	}

	// The launcher a trigger fire launches through, built the way the worker
	// builds it (internal/app NewRunLauncher). nil prober: triggers never ask for a
	// greenfield probe.
	pause := workflow.NewPauseService(s.Temporal, s.Repo)
	triggerLauncher := launch.NewLauncher(s.Repo, threads.NewService(s.Repo), s.Temporal,
		runs.NewService(s.Repo, s.Temporal, pause), taskQueue)

	progress := &atomic.Int64{}
	temporalWorker, _, err := workersetup.NewWorker(&workersetup.Config{
		// The SDK worker requires the concrete Dial client, not a wrapper.
		// Its own queue is isolated by TaskQueueSuffix below; handler starts
		// and the injected trigger launcher use the story-routed client.
		TemporalClient:  shared.Temporal,
		Database:        s.Repo,
		StreamingHub:    hub,
		ToolsFactory:    toolsFactory,
		ToolExecutor:    executor,
		DaemonRouter:    nil, // hermetic: no daemon transport; worktree ops unavailable
		MCPBinder:       mcpBinder,
		ConfigProvider:  config.NewStoredConfigProvider(configadapter.NewRepoConfigStore(s.Repo)),
		DriverResolver:  resolver,
		TaskQueueSuffix: taskQueueSuffix,
		TriggerLauncher: triggerLauncher,
		// Counts the story's progress for its waits (waitClock).
		WorkflowInterceptors: []interceptor.WorkerInterceptor{&progressInterceptor{n: progress}},
	})
	require.NoError(t, err, "create story worker")
	workerRun := make(chan error, 1)
	go func() { workerRun <- temporalWorker.Run(context.Background()) }()
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := temporalWorker.Drain(stopCtx); err != nil {
			t.Logf("worker did not stop: %v", err)
		}
		if err := <-workerRun; err != nil {
			t.Logf("worker stopped with: %v", err)
		}
	})

	// Wait until the worker's pollers are registered with Temporal. This both
	// avoids no-poller scheduling latency on fast stories and guarantees the
	// worker got past startup before a fast story's cleanup calls Stop
	// (worker.Run racing worker.Stop during startup trips the race detector).
	waitForWorkerPollers(t, s.Temporal, workersetup.TaskQueueName(taskQueueSuffix))

	// No Router: these stories run without a daemon, so there is no tool to
	// cancel on interrupt and no machine to wake for an attended turn.
	chatSvc, err := chat.New(chat.Deps{
		Logger:   slog.Default(),
		Config:   &typedconfig.Config{},
		Repo:     s.Repo,
		Temporal: s.Temporal,
		Pause:    pause,
	})
	require.NoError(t, err, "chat service")
	questionSvc, err := question.New(question.Deps{
		Logger:   slog.Default(),
		Config:   &typedconfig.Config{},
		Repo:     s.Repo,
		Signals:  pause,
		Messages: chatSvc,
	})
	require.NoError(t, err, "question service")
	approvalSvc, err := approval.New(approval.Deps{
		Logger:  slog.Default(),
		Config:  &typedconfig.Config{},
		Repo:    s.Repo,
		Signals: pause,
	})
	require.NoError(t, err, "approval service")
	triggerSyncer, triggerFires := trigger.TemporalBackend(s.Temporal, s.Repo, taskQueue, nil)
	triggerSvc, err := trigger.New(trigger.Deps{
		Logger: slog.Default(),
		Config: &typedconfig.Config{},
		Repo:   s.Repo,
		Syncer: triggerSyncer,
		Fires:  triggerFires,
	})
	require.NoError(t, err, "trigger service")

	return &Harness{
		TriggerSvc:  triggerSvc,
		T:           t,
		Stack:       s,
		UserID:      userID,
		ProjectID:   projectID,
		ProjectPath: projectPath,
		LLM:         llmScript,
		ChatSvc:     chatSvc,
		QuestionSvc: questionSvc,
		ApprovalSvc: approvalSvc,
		Pause:       pause,
		Ctx:         ctx,
		progress:    progress,
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

// StartChat drives the production StartChat handler. workflowRef is e.g.
// "builtin://agent". params are workflow inputs; a {"model": {"id": "mock"}}
// entry is added when the caller didn't specify one.
func (h *Harness) StartChat(workflowRef, prompt string, params map[string]any) *reliantv1.StartChatResponse {
	h.T.Helper()
	resp, err := h.TryStartChat(workflowRef, prompt, params)
	require.NoError(h.T, err, "StartChat")
	require.NotNil(h.T, resp.Msg.Chat)
	return resp.Msg
}

// TryStartChat is StartChat without asserting success (for validation-error
// stories).
func (h *Harness) TryStartChat(workflowRef, prompt string, params map[string]any) (*connect.Response[reliantv1.StartChatResponse], error) {
	h.T.Helper()
	return h.tryStartChat(workflowRef, prompt, params, false)
}

// StartNoMachineChat starts a chat with no machine by design
// (StartChatRequest.no_machine).
func (h *Harness) StartNoMachineChat(workflowRef, prompt string, params map[string]any) *reliantv1.StartChatResponse {
	h.T.Helper()
	resp, err := h.tryStartChat(workflowRef, prompt, params, true)
	require.NoError(h.T, err, "StartChat(no_machine)")
	return resp.Msg
}

func (h *Harness) tryStartChat(workflowRef, prompt string, params map[string]any, noMachine bool) (*connect.Response[reliantv1.StartChatResponse], error) {
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

	req := connect.NewRequest(&reliantv1.StartChatRequest{
		ProjectId: h.ProjectID,
		Workflow:  workflowRef,
		Messages: []*reliantv1.InputMessage{
			{Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Content: prompt},
		},
		WorkflowParams: protoParams,
	})
	if noMachine {
		req.Msg.NoMachine = &noMachine
	}
	return h.ChatSvc.StartChat(h.Ctx, req)
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
//     (internal/handlers/question/question.go) to persist the feedback as a user
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

// A story's waits fail when the story STOPS MAKING PROGRESS, not when the
// host is slow.
//
// They used to be a fixed 45s each. On a loaded host (the release gate at load
// 65-140, beside the unit and integration lanes) sixteen parallel stories on
// one Temporal dev server and one Postgres are each tens of times slower, and
// a story whose workflow was still running activities — just slowly — failed
// "timed out after 45s" with its workflow ACTIVE. That measured the host.
//
// Progress is the story worker's activity executions (progressInterceptor):
// every step of a story's workflow is an activity, and each start and finish
// counts. A wait fails once stallTimeout passes with none — a hang, at the
// same 45s it always was — or at storyWaitCap however busy the story is, so a
// story that loops forever still fails, inside the package's 10m timeout.
const (
	stallTimeout = 45 * time.Second
	storyWaitCap = 5 * time.Minute
	pollInterval = 100 * time.Millisecond
)

// progressInterceptor counts a story worker's activity starts and finishes.
type progressInterceptor struct {
	interceptor.WorkerInterceptorBase
	n *atomic.Int64
}

func (p *progressInterceptor) InterceptActivity(_ context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	i := &progressActivityInbound{n: p.n}
	i.Next = next
	return i
}

type progressActivityInbound struct {
	interceptor.ActivityInboundInterceptorBase
	n *atomic.Int64
}

func (a *progressActivityInbound) ExecuteActivity(ctx context.Context, in *interceptor.ExecuteActivityInput) (interface{}, error) {
	a.n.Add(1)
	defer a.n.Add(1)
	return a.Next.ExecuteActivity(ctx, in)
}

// waitClock decides when a wait gives up: after stallTimeout with no progress,
// or storyWaitCap in all.
type waitClock struct {
	progress   *atomic.Int64
	start      time.Time
	seen       int64
	lastChange time.Time
}

func (h *Harness) newWaitClock() *waitClock {
	now := time.Now()
	return &waitClock{progress: h.progress, start: now, seen: h.progress.Load(), lastChange: now}
}

// expired reports why the wait is over, or "" while the story is progressing.
func (c *waitClock) expired() string {
	now := time.Now()
	if n := c.progress.Load(); n != c.seen {
		c.seen, c.lastChange = n, now
	}
	switch {
	case now.Sub(c.lastChange) >= stallTimeout:
		return fmt.Sprintf("no progress for %s (%d activity starts and finishes in %s)",
			stallTimeout, c.seen, now.Sub(c.start).Round(time.Second))
	case now.Sub(c.start) >= storyWaitCap:
		return fmt.Sprintf("still not done after %s, though the story kept running activities (%d starts and finishes)",
			storyWaitCap, c.seen)
	}
	return ""
}

// eventually polls cond until it returns true or the story stops making
// progress (waitClock). The last detail string is included in the failure
// message.
func (h *Harness) eventually(what string, cond func() (bool, string)) {
	h.T.Helper()
	clock := h.newWaitClock()
	var last string
	for {
		ok, detail := cond()
		if ok {
			return
		}
		last = detail
		if why := clock.expired(); why != "" {
			h.T.Fatalf("gave up waiting for %s: %s (last state: %s)", what, why, last)
		}
		time.Sleep(pollInterval)
	}
}

// awaitWorkflow waits for the Temporal execution workflowID to finish, into
// valuePtr, giving up as eventually does. An execution not started yet is
// waited for too: a trigger records its launched run (the chat, its workflow
// id) before the launcher has started it in Temporal, and on a loaded host a
// story reads the id in between.
func (h *Harness) awaitWorkflow(workflowID string, valuePtr interface{}) error {
	h.T.Helper()
	ctx, cancel := context.WithCancel(h.Ctx)
	defer cancel()
	get := func() <-chan error {
		done := make(chan error, 1)
		go func() { done <- h.Stack.Temporal.GetWorkflow(ctx, workflowID, "").Get(ctx, valuePtr) }()
		return done
	}
	done := get()
	clock := h.newWaitClock()
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			var notFound *serviceerror.NotFound
			if !errors.As(err, &notFound) {
				return err
			}
			if why := clock.expired(); why != "" {
				return fmt.Errorf("gave up waiting for temporal workflow %s to start: %s: %w", workflowID, why, err)
			}
			time.Sleep(pollInterval)
			done = get()
		case <-tick.C:
			if why := clock.expired(); why != "" {
				cancel()
				<-done
				return fmt.Errorf("gave up waiting for temporal workflow %s: %s", workflowID, why)
			}
		}
	}
}

// WaitToolExecuting blocks until the scripted tool call scriptID is actually
// RUNNING — its tool_calls row is EXECUTING, which ExecuteTools writes as the
// tool starts.
//
// This, not "the assistant's tool_use is persisted", is the point at which a
// kill lands INSIDE execute_tools. CallLLM persists the tool_use before the
// workflow task that schedules ExecuteTools has completed, so a terminate in
// that gap resets to BEFORE CallLLM: the replay re-asks the model and the
// first tool_use is left unanswered (the "interrupted — outcome unknown"
// repair). Resume stories that mean "kill it mid-tool" must wait for this;
// waiting only for the tool_use made story 08 fail ~1 run in 15 under load.
func (h *Harness) WaitToolExecuting(scriptID string) {
	h.T.Helper()
	id := h.LLM.CallID(scriptID)
	h.eventually(fmt.Sprintf("tool call %s to be executing", scriptID), func() (bool, string) {
		call, err := h.Stack.Repo.GetToolCall(h.Ctx, id)
		if err != nil || call == nil {
			return false, fmt.Sprintf("get tool call: %v", err)
		}
		if call.Status != core.ToolCallStatusExecuting {
			return false, fmt.Sprintf("status=%d", call.Status)
		}
		return true, ""
	})
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
	require.NoError(h.T, h.awaitWorkflow(workflowID, nil), "temporal workflow %s should complete cleanly", workflowID)
}

// WorkflowResult waits for the Temporal execution to finish and returns the
// DynamicWorkflow outputs.
func (h *Harness) WorkflowResult(workflowID string) map[string]interface{} {
	h.T.Helper()
	var result struct {
		Outputs map[string]interface{} `json:"outputs"`
	}
	require.NoError(h.T, h.awaitWorkflow(workflowID, &result), "temporal workflow %s should complete cleanly", workflowID)
	return result.Outputs
}

// WaitPendingQuestion polls until the chat has a pending ask_question.
func (h *Harness) WaitPendingQuestion(chatID string) *db.Question {
	h.T.Helper()
	var q *db.Question
	h.eventually("pending question on chat "+chatID, func() (bool, string) {
		got, err := h.NewestPendingQuestion(chatID)
		if err != nil || got == nil {
			return false, fmt.Sprintf("pending question: %v", err)
		}
		q = got
		return true, ""
	})
	return q
}

// NewestPendingQuestion returns the chat's newest pending question, or nil.
func (h *Harness) NewestPendingQuestion(chatID string) (*db.Question, error) {
	// Oldest first: the newest pending question is the last.
	pending, err := h.Stack.Repo.ListPendingQuestionsByChat(h.Ctx, chatID)
	if err != nil || len(pending) == 0 {
		return nil, err
	}
	return pending[len(pending)-1], nil
}

// WaitPendingApproval polls until the chat has exactly one pending approval.
func (h *Harness) WaitPendingApproval(chatID string) *db.Approval {
	h.T.Helper()
	var found *db.Approval
	h.eventually("pending approval on chat "+chatID, func() (bool, string) {
		pending, err := h.Stack.Repo.ListPendingApprovalsByChat(h.Ctx, chatID)
		if err != nil {
			return false, fmt.Sprintf("list pending approvals: %v", err)
		}
		if len(pending) != 1 {
			return false, fmt.Sprintf("%d pending approvals", len(pending))
		}
		found = pending[0]
		return true, ""
	})
	return found
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
	row, err := h.Stack.Repo.GetChat(h.Ctx, chatID)
	require.NoError(h.T, err, "get chat")
	return row
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
