// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// pathDriver answers every request with a fixed reply and token split, as a
// tool call when the request offers a tool (title and draft requests are
// pinned to one) and as text otherwise (a compaction summary). With failFirst
// set, the first request of the test errors the way a dropped connection does.
type pathDriver struct {
	mu        sync.Mutex
	name      string
	failFirst bool
	requests  int
	toolInput map[string]string
}

func (d *pathDriver) Name() string                          { return d.name }
func (d *pathDriver) Model() models.Model                   { return models.Model{ID: "mock-model"} }
func (d *pathDriver) ValidateKey(ctx context.Context) error { return nil }

func (d *pathDriver) SendMessages(context.Context, []string, []message.Message, []tools.Tool) (*llm.DriverResponse, error) {
	return nil, errors.New("not used")
}

func (d *pathDriver) StreamResponse(_ context.Context, _ []string, _ []message.Message, offered []tools.Tool) <-chan llm.DriverEvent {
	d.mu.Lock()
	d.requests++
	fail := d.failFirst && d.requests == 1
	d.mu.Unlock()

	ch := make(chan llm.DriverEvent, 1)
	defer close(ch)
	if fail {
		ch <- llm.DriverEvent{Type: llm.EventError, Error: errors.New("http2: client connection lost; a secret prompt sk-live-9")}
		return ch
	}
	resp := &llm.DriverResponse{
		FinishReason: message.FinishReasonEndTurn,
		Usage: llm.TokenUsage{
			InputTokens: 700, OutputTokens: 90, CacheReadInputTokens: 400, CacheCreationInputTokens: 30,
		},
	}
	if len(offered) == 1 {
		input := d.toolInput[offered[0].Name()]
		resp.ToolCalls = []message.ToolCall{{ID: "tc1", Name: offered[0].Name(), Input: input}}
	} else {
		resp.Content = "Summary of the conversation."
	}
	ch <- llm.DriverEvent{Type: llm.EventComplete, Response: resp}
	return ch
}

func (d *pathDriver) resolver() drivers.DriverResolver {
	return func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
		return d, nil
	}
}

func newPathDriver(provider string, failFirst bool) *pathDriver {
	return &pathDriver{
		name: provider, failFirst: failFirst,
		toolInput: map[string]string{
			tools.SetTitleToolName: `{"title":"Debugging Workflows"}`,
			tools.ToolDraftAutomation: `{"when":{"kind":"schedule","cron":["0 9 * * 1-5"]},` +
				`"prompt":"Summarise failed CI runs.","summary":"Weekdays at 9:00, summarise failed CI runs"}`,
		},
	}
}

// retried runs one activity on a real Temporal retry ladder, so a failure
// really is followed by attempt 2.
func retried(ctx workflow.Context, name string, input any) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: time.Millisecond, BackoffCoefficient: 1, MaximumAttempts: 3,
		},
	})
	return workflow.ExecuteActivity(ctx, name, input).Get(ctx, nil)
}

func runRetried(t *testing.T, name string, fn any, input any) {
	t.Helper()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	wf := func(ctx workflow.Context) error { return retried(ctx, name, input) }
	env.RegisterWorkflowWithOptions(wf, workflow.RegisterOptions{Name: "retried-" + name})
	env.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: name})
	env.ExecuteWorkflow("retried-" + name)
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
}

type ledgerPathRow struct {
	Purpose, Provider, AuthMode, Outcome, ErrorClass, ChatID, ID string
	In, Out, CacheRead, CacheWrite                               int64
	Attempt                                                      int
}

func ledgerRows(t *testing.T, h *IdempotencyTestHelper, purpose string) []ledgerPathRow {
	t.Helper()
	rows, err := h.DB().Query(`SELECT purpose, provider, auth_mode, outcome, error_class, chat_id, id,
		input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, attempt
		FROM llm_call_usage WHERE purpose = $1 ORDER BY id`, purpose)
	require.NoError(t, err)
	defer rows.Close()
	var out []ledgerPathRow
	for rows.Next() {
		var r ledgerPathRow
		require.NoError(t, rows.Scan(&r.Purpose, &r.Provider, &r.AuthMode, &r.Outcome, &r.ErrorClass, &r.ChatID, &r.ID,
			&r.In, &r.Out, &r.CacheRead, &r.CacheWrite, &r.Attempt))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

func dailyCalls(t *testing.T, h *IdempotencyTestHelper) (calls, errCalls, in int64) {
	t.Helper()
	require.NoError(t, h.DB().QueryRow(`SELECT COALESCE(sum(calls),0), COALESCE(sum(error_calls),0), COALESCE(sum(input_tokens),0)
		FROM llm_call_usage_daily`).Scan(&calls, &errCalls, &in))
	return
}

// Compaction makes its own model request. It must reach the ledger and the
// rollup, once, however many times Temporal runs the activity.
func TestCompaction_RequestReachesTheLedgerOncePerRetry(t *testing.T) {
	t.Parallel()
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	chatID, thread, svc := setupChatWithWorkflowAndThread(t, h.Repo())
	_, err := h.Repo().SaveMessageToThread(ctx, chatID, thread, roleUser, "Summarize where we are.", &thread, nil, nil)
	require.NoError(t, err)
	saveTurn(t, svc, chatID, thread, "call_1", 20_000, 80_000)

	driver := newPathDriver("claude", true)
	compact := NewCompactActivity(h.Repo(), driver.resolver())
	input := CompactInput{ChatID: chatID, Thread: thread}.V3()
	input.Node.GetCompact().Force = &reliantv1.CelBool{Value: &reliantv1.CelBool_Literal{Literal: true}}
	runRetried(t, "Compact", compact.Execute, input)
	require.Equal(t, 2, driver.requests, "attempt 1 fails, attempt 2 answers")

	rows := ledgerRows(t, h, "compaction")
	require.Len(t, rows, 1, "one logical summary request, however many attempts: %+v", rows)
	r := rows[0]
	assert.Equal(t, chatID, r.ChatID)
	assert.Equal(t, "claude", r.Provider)
	assert.Equal(t, "subscription", r.AuthMode)
	assert.Equal(t, "success", r.Outcome)
	assert.Equal(t, [4]int64{700, 90, 400, 30}, [4]int64{r.In, r.Out, r.CacheRead, r.CacheWrite})
	assert.Equal(t, 2, r.Attempt)

	calls, errCalls, in := dailyCalls(t, h)
	assert.EqualValues(t, 1, calls)
	assert.EqualValues(t, 0, errCalls, "the failed first attempt is not in the rollup")
	assert.EqualValues(t, 700, in)
}

// A title is requested through requestTitle, which retries on another account:
// a title is one or several logical requests, each its own row, and a
// Temporal retry of the activity replaces them rather than adding.
func TestTitle_RequestReachesTheLedgerOncePerRetry(t *testing.T) {
	t.Parallel()
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-title-ledger", "user-title-ledger")
	chat := h.CreateTestChat(ctx, "chat-title-ledger", project.ID, project.UserID)

	driver := newPathDriver("openai", true)
	title := NewGenerateTitleActivity(h.Repo(), driver.resolver())
	runRetried(t, "GenerateTitle", title.Execute, GenerateTitleInput{ChatID: chat.ID, FirstMessage: "how do I debug a workflow?"})
	require.Equal(t, 2, driver.requests)

	rows := ledgerRows(t, h, "title")
	require.Len(t, rows, 1, "%+v", rows)
	assert.Equal(t, chat.ID, rows[0].ChatID)
	assert.Equal(t, "byok", rows[0].AuthMode, "a pasted OpenAI key is the user's own")
	assert.Equal(t, "success", rows[0].Outcome)
	assert.Equal(t, int64(700), rows[0].In)
	assert.Equal(t, 2, rows[0].Attempt)
	calls, _, _ := dailyCalls(t, h)
	assert.EqualValues(t, 1, calls)
}

// A draft belongs to no chat, so its row has none; it still reaches the
// ledger under the user who asked.
func TestDraftAutomation_RequestReachesTheLedgerOncePerRetry(t *testing.T) {
	t.Parallel()
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	driver := newPathDriver("reliant", true)
	draft := NewDraftAutomationActivity(driver.resolver()).WithUsageRecorder(h.Repo())
	runRetried(t, "DraftAutomation", draft.Execute, DraftAutomationInput{UserID: "user-draft-ledger", Description: "every weekday at 9 summarise failed CI"})
	require.Equal(t, 2, driver.requests)

	rows := ledgerRows(t, h, "draft_automation")
	require.Len(t, rows, 1, "%+v", rows)
	assert.Equal(t, "", rows[0].ChatID)
	assert.Equal(t, "managed", rows[0].AuthMode)
	assert.Equal(t, int64(700), rows[0].In)
	var userID string
	require.NoError(t, h.DB().QueryRow(`SELECT user_id FROM llm_call_usage WHERE purpose = 'draft_automation'`).Scan(&userID))
	assert.Equal(t, "user-draft-ledger", userID)
}

// Several requests inside one activity are distinct logical requests, each
// with its own row, and none collides with the chat turn's row.
func TestUsageMeter_RequestsInOneActivityAreDistinctAndNeverCollideWithChatTurns(t *testing.T) {
	t.Parallel()
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-meter", "user-meter")
	chat := h.CreateTestChat(ctx, "chat-meter", project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	driver := newPathDriver("anthropic", false)
	resolved := &resolvedLLMCall{Driver: driver, Model: models.Model{ID: "mock-model"}}

	// Three requests in one activity: two compaction attempts and a title.
	exec := func(ctx context.Context, _ string) (string, error) {
		compaction := newUsageMeter(h.Repo(), project.UserID, chat.ID, "compaction")
		title := newUsageMeter(h.Repo(), project.UserID, chat.ID, "title")
		for range 2 {
			if _, err := compaction.streamAndAccumulate(ctx, resolved, nil, nil, nil); err != nil {
				return "", err
			}
		}
		_, err := title.streamAndAccumulate(ctx, resolved, nil, nil, nil)
		return "", err
	}
	// A chat turn from the same activity id is a fourth, separate row.
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivityWithOptions(exec, activity.RegisterOptions{Name: "Many"})
	_, err := env.ExecuteActivity("Many", "x")
	require.NoError(t, err)

	var ids []string
	rows, err := h.DB().Query(`SELECT id FROM llm_call_usage ORDER BY id`)
	require.NoError(t, err)
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	rows.Close()
	require.Len(t, ids, 3, "two compaction requests and one title request: %v", ids)
	seen := map[string]bool{}
	for _, id := range ids {
		assert.False(t, seen[id])
		seen[id] = true
	}
	assert.True(t, strings.HasSuffix(ids[0], "/compaction/1"), ids[0])
	assert.True(t, strings.HasSuffix(ids[1], "/compaction/2"), ids[1])
	assert.True(t, strings.HasSuffix(ids[2], "/title/1"), ids[2])
	assert.NotEqual(t, llmUsageID("w", "r", "a", "chat", 1), llmUsageID("w", "r", "a", "compaction", 1))
	assert.NotEqual(t, llmUsageID("w", "r", "a", "title", 1), llmUsageID("w", "r", "a", "title", 2))
}

// A request that fails for good is recorded as a failed call with a class and
// no error text.
func TestUsageMeter_FailureIsRecordedAsClassOnly(t *testing.T) {
	t.Parallel()
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-meter-fail", "user-meter-fail")
	chat := h.CreateTestChat(ctx, "chat-meter-fail", project.ID, project.UserID)

	driver := newPathDriver("gemini", true)
	resolved := &resolvedLLMCall{Driver: driver, Model: models.Model{ID: "mock-model"}}
	exec := func(ctx context.Context, _ string) (string, error) {
		_, err := newUsageMeter(h.Repo(), project.UserID, chat.ID, "title").streamAndAccumulate(ctx, resolved, nil, nil, nil)
		return "", err
	}
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivityWithOptions(exec, activity.RegisterOptions{Name: "Fails"})
	_, err := env.ExecuteActivity("Fails", "x")
	require.Error(t, err)

	rows := ledgerRows(t, h, "title")
	require.Len(t, rows, 1)
	assert.Equal(t, "error", rows[0].Outcome)
	assert.Equal(t, "network", rows[0].ErrorClass)
	var dump string
	require.NoError(t, h.DB().QueryRow(`SELECT l::text FROM llm_call_usage l`).Scan(&dump))
	assert.NotContains(t, dump, "sk-live-9")
	assert.NotContains(t, dump, "http2")
	calls, errCalls, _ := dailyCalls(t, h)
	assert.EqualValues(t, 1, calls)
	assert.EqualValues(t, 1, errCalls)
}

// A new place that streams a model request without going through the ledger
// would silently understate usage and make the privacy page's "every model
// call" false. This parses every non-test Go file in the module and fails when
// one calls a driver-level request entry point from anywhere that is not on the
// allow-list, so the next call site is a test failure, not an audit finding.
//
// Allowed, and why:
//   - internal/llm/**: the drivers and the accumulator implement the entry
//     points; they are not callers of a request.
//   - handlers/call_llm.go: the chat turn, which records itself
//     (recordLLMCallUsage, asserted below) beside the stream it reads.
//   - handlers/llm_usage_record.go: usageMeter, the seam every other request
//     goes through.
//   - handlers/llm_probe.go and tools/reliant-dev: an operator's probe of a
//     model, run from the dev CLI and never as part of a user's usage.
func TestModelRequestsGoThroughTheUsageLedger(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "..", "..", "..")
	handlers := filepath.ToSlash(filepath.Join("internal", "workflow", "runtime", "activities", "handlers"))
	allowedFile := map[string]bool{
		handlers + "/call_llm.go":         true,
		handlers + "/llm_usage_record.go": true,
		handlers + "/llm_probe.go":        true,
	}
	allowedDir := []string{"internal/llm/", "tools/reliant-dev/", "gen/", "node_modules/", ".git/"}
	entryPoints := map[string]bool{"StreamResponse": true, "SendMessages": true, "StreamAndAccumulate": true}

	var offenders []string
	scanned := 0
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "frontends", "electron", "docs", "research", "proto-vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") || strings.HasSuffix(rel, "_gen.go") {
			return nil
		}
		for _, dir := range allowedDir {
			if strings.HasPrefix(rel, dir) {
				return nil
			}
		}
		if allowedFile[rel] {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return nil
		}
		scanned++
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var fn string
			switch f := call.Fun.(type) {
			case *ast.SelectorExpr:
				fn = f.Sel.Name
			case *ast.Ident:
				fn = f.Name
			}
			if entryPoints[fn] {
				offenders = append(offenders, fmt.Sprintf("%s calls %s", rel, fn))
			}
			return true
		})
		return nil
	}))
	require.Greater(t, scanned, 500, "the scan must actually walk the module (found %d files)", scanned)
	sort.Strings(offenders)
	assert.Empty(t, offenders,
		"a model request must go through usageMeter.streamAndAccumulate (llm_usage_record.go) so the usage ledger and the privacy page's \"every model request\" stay true; offenders:\n%s",
		strings.Join(offenders, "\n"))

	src, err := os.ReadFile("call_llm.go")
	require.NoError(t, err)
	assert.Contains(t, string(src), "a.recordLLMCallUsage(", "CallLLM's turn must write its ledger row")
}
