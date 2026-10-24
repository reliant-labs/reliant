// Copyright (c) 2025 Reliant Labs
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools/names"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/version"
)

// fakeBugReportLookup answers the three reads report_bug makes to describe
// where a report came from.
type fakeBugReportLookup struct {
	chat    *db.Chat
	latest  *db.Message
	daemons map[string]*db.Daemon
}

func (f *fakeBugReportLookup) GetChat(_ context.Context, id string) (*db.Chat, error) {
	if f.chat == nil || f.chat.ID != id {
		return nil, errors.New("chat not found")
	}
	return f.chat, nil
}

func (f *fakeBugReportLookup) GetLatestMessageInThread(_ context.Context, _ string) (*db.Message, error) {
	if f.latest == nil {
		return nil, errors.New("no messages")
	}
	return f.latest, nil
}

func (f *fakeBugReportLookup) GetDaemon(_ context.Context, id string) (*db.Daemon, error) {
	if d, ok := f.daemons[id]; ok {
		return d, nil
	}
	return nil, errors.New("daemon not found")
}

func (f *fakeBugReportLookup) ListDaemonsByUserID(_ context.Context, userID string) ([]*db.Daemon, error) {
	var owned []*db.Daemon
	for _, d := range f.daemons {
		if d.UserID == userID {
			owned = append(owned, d)
		}
	}
	return owned, nil
}

// recordingCapture stands in for telemetry.FileBugReport.
type recordingCapture struct {
	mu      sync.Mutex
	reports []telemetry.BugReport
}

func (r *recordingCapture) capture(_ context.Context, report telemetry.BugReport) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reports = append(r.reports, report)
	return "rep" + string(rune('a'+len(r.reports)-1))
}

func (r *recordingCapture) filed() []telemetry.BugReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]telemetry.BugReport(nil), r.reports...)
}

type reportBugHarness struct {
	tool    Tool
	capture *recordingCapture
	clock   time.Time
}

func newReportBugHarness(lookup bugReportLookup) *reportBugHarness {
	h := &reportBugHarness{
		capture: &recordingCapture{},
		clock:   time.Date(2026, 10, 9, 19, 30, 0, 0, time.UTC),
	}
	h.tool = newReportBugTool(lookup, h.capture.capture, newBugReportLimiter(30*time.Minute, 3), func() time.Time { return h.clock })
	return h
}

func (h *reportBugHarness) call(t *testing.T, tc *rctx.ToolContext, input map[string]any) (ToolResponse, ReportBugMetadata) {
	t.Helper()
	raw, err := json.Marshal(input)
	require.NoError(t, err)
	resp, err := h.tool.Run(tc, ToolCall{ID: "toolu_report_1", Name: ToolReportBug, Input: string(raw)})
	require.NoError(t, err)
	var meta ReportBugMetadata
	if resp.Metadata != "" {
		require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	}
	return resp, meta
}

func validBugReportInput(title string) map[string]any {
	return map[string]any{
		"product":  "reliant",
		"severity": "high",
		"title":    title,
		"summary":  "The newest user message reached the model as [content trimmed].",
		"expected": "The message arrives intact.",
		"actual":   "The model was handed the literal [content trimmed].",
		"evidence": "chat 5ffd6bb4: token_count alternates 520k/696k",
	}
}

func reportBugContext(chatID string) *rctx.ToolContext {
	return rctx.NewToolContext(context.Background(), chatID, "thread-main", &db.Project{ID: "project-1"}, nil)
}

func TestReportBug_RejectsInvalidInputWithoutFiling(t *testing.T) {
	t.Parallel()

	cases := map[string]func(map[string]any){
		"missing product":   func(in map[string]any) { delete(in, "product") },
		"unknown product":   func(in map[string]any) { in["product"] = "acme" },
		"unknown severity":  func(in map[string]any) { in["severity"] = "urgent" },
		"missing title":     func(in map[string]any) { delete(in, "title") },
		"blank title":       func(in map[string]any) { in["title"] = "   " },
		"blank summary":     func(in map[string]any) { in["summary"] = "" },
		"blank expected":    func(in map[string]any) { in["expected"] = "\n" },
		"blank actual":      func(in map[string]any) { in["actual"] = " " },
		"unknown parameter": func(in map[string]any) { in["priority"] = "p0" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newReportBugHarness(nil)
			input := validBugReportInput("Message truncated")
			mutate(input)

			resp, _ := h.call(t, reportBugContext("chat-1"), input)

			assert.True(t, resp.IsError, "invalid input is refused: %s", resp.Content)
			assert.Empty(t, h.capture.filed(), "nothing is filed from a refused call")
		})
	}
}

func TestReportBug_NamesEveryBlankFieldAtOnce(t *testing.T) {
	t.Parallel()
	h := newReportBugHarness(nil)
	input := validBugReportInput(" ")
	input["expected"] = ""
	input["actual"] = ""

	resp, _ := h.call(t, reportBugContext("chat-1"), input)

	require.True(t, resp.IsError)
	for _, field := range []string{"title", "expected", "actual"} {
		assert.Contains(t, resp.Content, field, "one retry must be enough to fix every field")
	}
}

func TestReportBug_FilesReportDescribingWhereItCameFrom(t *testing.T) {
	t.Parallel()
	workflow, model, chatDaemon, worktreeDaemon, managed, pod := "builtin://agent", "gpt-5.6-terra", "daemon-chat", "daemon-worktree", "managed", "ws-ws-2aab1465"
	lookup := &fakeBugReportLookup{
		chat: &db.Chat{
			ID: "chat-1", UserID: "user-from-chat", ProjectID: "project-1",
			WorkflowName: &workflow, ActiveDaemonID: &chatDaemon,
		},
		latest:  &db.Message{Model: &model},
		daemons: map[string]*db.Daemon{worktreeDaemon: {ID: worktreeDaemon, DaemonType: &managed, Hostname: &pod}},
	}
	h := newReportBugHarness(lookup)
	tc := reportBugContext("chat-1")
	tc.Context = context.WithValue(tc.Context, auth.UserIDContextKey, "user-from-request")
	tc.Worktree = &rctx.WorktreeInfo{ID: "wt-1", DaemonID: worktreeDaemon}

	resp, meta := h.call(t, tc, validBugReportInput("User message delivered as [content trimmed]"))

	require.False(t, resp.IsError, resp.Content)
	filed := h.capture.filed()
	require.Len(t, filed, 1)
	assert.Equal(t, telemetry.BugReport{
		Product:    "reliant",
		Severity:   "high",
		Title:      "User message delivered as [content trimmed]",
		Summary:    "The newest user message reached the model as [content trimmed].",
		Expected:   "The message arrives intact.",
		Actual:     "The model was handed the literal [content trimmed].",
		Evidence:   "chat 5ffd6bb4: token_count alternates 520k/696k",
		ChatID:     "chat-1",
		ThreadID:   "thread-main",
		ToolCallID: "toolu_report_1",
		ProjectID:  "project-1",
		UserID:     "user-from-request",
		WorktreeID: "wt-1",
		Workflow:   workflow,
		Model:      model,
		DaemonID:   worktreeDaemon,
		DaemonType: managed,
		Pod:        pod,

		ReliantVersion: version.Version,
		ForgeVersion:   version.Forge(),
	}, filed[0], "the worktree's daemon is the one that ran the call, so it outranks the chat's")

	assert.Equal(t, "repa", meta.ReportID)
	assert.Contains(t, resp.Content, "repa", "the model is handed the report id")
}

// A run on the main checkout is pinned to no machine: its tools go wherever
// default resolution sends them, and the chat records no active daemon. The
// prod reports filed that way all said daemon_id="". A user with one machine
// has only one place those tools could have run.
func TestReportBug_UnpinnedRunNamesTheUsersOnlyMachine(t *testing.T) {
	t.Parallel()
	managed, pod := "managed", "ws-ws-2aab1465"
	lookup := &fakeBugReportLookup{
		chat: &db.Chat{ID: "chat-1", UserID: "user-1", ProjectID: "project-1"},
		daemons: map[string]*db.Daemon{
			"daemon-only": {ID: "daemon-only", UserID: "user-1", DaemonType: &managed, Hostname: &pod},
		},
	}
	h := newReportBugHarness(lookup)

	h.call(t, reportBugContext("chat-1"), validBugReportInput("Workspace volume detached"))

	filed := h.capture.filed()
	require.Len(t, filed, 1)
	assert.Equal(t, "daemon-only", filed[0].DaemonID)
	assert.Equal(t, managed, filed[0].DaemonType)
	assert.Equal(t, pod, filed[0].Pod, "a managed machine is named by its workspace pod")
}

// With several machines and nothing pinning the run, any one named would be a
// guess, and a wrong machine is worse than none. A self-hosted machine's
// hostname is the user's own computer's name, never reported as a pod.
func TestReportBug_DoesNotGuessAMachineOrNameADesktopsHost(t *testing.T) {
	t.Parallel()
	managed, selfHosted, laptop, pod := "managed", "self_hosted", "my-laptop", "ws-ws-1"
	daemons := map[string]*db.Daemon{
		"daemon-cloud":   {ID: "daemon-cloud", UserID: "user-1", DaemonType: &managed, Hostname: &pod},
		"daemon-desktop": {ID: "daemon-desktop", UserID: "user-1", DaemonType: &selfHosted, Hostname: &laptop},
	}

	t.Run("several machines, none pinned", func(t *testing.T) {
		t.Parallel()
		h := newReportBugHarness(&fakeBugReportLookup{
			chat:    &db.Chat{ID: "chat-1", UserID: "user-1"},
			daemons: daemons,
		})
		h.call(t, reportBugContext("chat-1"), validBugReportInput("Shell hangs"))
		require.Len(t, h.capture.filed(), 1)
		assert.Empty(t, h.capture.filed()[0].DaemonID)
		assert.Empty(t, h.capture.filed()[0].Pod)
	})

	t.Run("pinned to the desktop", func(t *testing.T) {
		t.Parallel()
		h := newReportBugHarness(&fakeBugReportLookup{
			chat:    &db.Chat{ID: "chat-1", UserID: "user-1"},
			daemons: daemons,
		})
		tc := reportBugContext("chat-1")
		tc.Worktree = &rctx.WorktreeInfo{ID: "wt-1", DaemonID: "daemon-desktop"}
		h.call(t, tc, validBugReportInput("Shell hangs"))
		require.Len(t, h.capture.filed(), 1)
		assert.Equal(t, "daemon-desktop", h.capture.filed()[0].DaemonID)
		assert.Equal(t, selfHosted, h.capture.filed()[0].DaemonType)
		assert.Empty(t, h.capture.filed()[0].Pod, "a desktop's hostname is not a pod, and not ours to send")
	})
}

// The lookups only enrich a report. None of them failing may cost the report.
func TestReportBug_FilesEvenWhenNothingAboutTheCallerResolves(t *testing.T) {
	t.Parallel()
	h := newReportBugHarness(&fakeBugReportLookup{})

	resp, meta := h.call(t, reportBugContext("chat-unknown"), validBugReportInput("Daemon lost its workspace mount"))

	require.False(t, resp.IsError, resp.Content)
	require.Len(t, h.capture.filed(), 1)
	assert.Equal(t, "chat-unknown", h.capture.filed()[0].ChatID)
	assert.NotEmpty(t, meta.ReportID)
}

func TestReportBug_RepeatInTheSameChatReturnsTheFirstReport(t *testing.T) {
	t.Parallel()
	h := newReportBugHarness(nil)
	tc := reportBugContext("chat-1")

	_, first := h.call(t, tc, validBugReportInput("Message truncated in chat 5ffd6bb4"))
	h.clock = h.clock.Add(10 * time.Minute)
	resp, again := h.call(t, tc, validBugReportInput("message TRUNCATED in chat d221a691"))

	assert.Len(t, h.capture.filed(), 1, "a looping agent files one report, not one per turn")
	assert.False(t, resp.IsError, "a repeat is answered, not refused")
	assert.True(t, again.Duplicate)
	assert.Equal(t, first.ReportID, again.ReportID)
	assert.Contains(t, resp.Content, first.ReportID)

	// Another chat hitting the same defect is a second report worth filing:
	// its own issue, for the person triaging to deduplicate.
	h.call(t, reportBugContext("chat-2"), validBugReportInput("Message truncated in chat 5ffd6bb4"))
	assert.Len(t, h.capture.filed(), 2)

	// Once the window has passed, the defect is reported again.
	h.clock = h.clock.Add(31 * time.Minute)
	_, later := h.call(t, tc, validBugReportInput("Message truncated in chat 5ffd6bb4"))
	assert.False(t, later.Duplicate)
	assert.Len(t, h.capture.filed(), 3)
}

func TestReportBug_CapsDistinctReportsPerChat(t *testing.T) {
	t.Parallel()
	h := newReportBugHarness(nil)
	tc := reportBugContext("chat-1")

	for _, title := range []string{"First defect", "Second defect", "Third defect"} {
		_, meta := h.call(t, tc, validBugReportInput(title))
		require.NotEmpty(t, meta.ReportID, title)
	}
	resp, meta := h.call(t, tc, validBugReportInput("Fourth defect"))

	assert.Len(t, h.capture.filed(), 3, "the cap stops an agent cycling through titles")
	assert.True(t, meta.RateLimited)
	assert.Empty(t, meta.ReportID)
	assert.False(t, resp.IsError, "being capped is not the agent's error to retry")
	assert.Contains(t, resp.Content, "tell the user")
}

// The production tool files through the process logger: one WARN issue
// event, redacted, carrying the issue source the issues worker groups on.
//
// Not parallel: it swaps the process's default slog handler to read the log.
func TestReportBug_FilesAnIssueEventThroughTheProcessLogger(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	tool := newReportBugTool(nil, func(ctx context.Context, report telemetry.BugReport) string {
		return telemetry.FileBugReport(ctx, nil, report)
	}, newBugReportLimiter(30*time.Minute, 3), time.Now)
	input := validBugReportInput("Workspace volume detached")
	input["evidence"] = "ANTHROPIC_API_KEY=sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\nls: /home/workspace: No such file"
	raw, err := json.Marshal(input)
	require.NoError(t, err)

	resp, err := tool.Run(reportBugContext("chat-1"), ToolCall{ID: "toolu_report_1", Name: ToolReportBug, Input: string(raw)})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	var meta ReportBugMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))

	var record map[string]any
	require.NoError(t, json.Unmarshal(logs.Bytes(), &record), "one report is one record: %s", logs.String())
	assert.Equal(t, "WARN", record["level"])
	assert.Equal(t, telemetry.BugReportSource, record[telemetry.AttrIssueSource])
	assert.Equal(t, meta.ReportID, record[telemetry.AttrBugReportID], "the id the model is handed finds the record")
	assert.Contains(t, resp.Content, meta.ReportID)
	assert.Equal(t, "[reliant] Workspace volume detached", record["msg"])
	assert.Equal(t, "chat-1", record["chat_id"])
	assert.Equal(t, "toolu_report_1", record["tool_call_id"])
	assert.Contains(t, record[telemetry.AttrBugReportEvidence], "No such file")
	assert.NotContains(t, logs.String(), "sk-ant-api03-AAAA", "the record is redacted")
}

func TestReportBug_IsRegisteredForEveryCodingAgent(t *testing.T) {
	t.Parallel()

	def, ok := registryIndex()[ToolReportBug]
	require.True(t, ok, "report_bug is in the registry")
	assert.Equal(t, PlacementServer, def.Placement, "it needs no filesystem: it runs in the worker, whose logs are ours")
	assert.True(t, def.hasTag(TagCodingDefault), "preloaded for the coding agent")
	assert.True(t, def.hasTag(TagCodingPlan), "plan mode hits defects too")
	assert.Contains(t, names.AllToolNames, names.ToolReportBug)
	assert.Equal(t, ToolReportBug, names.ToolReportBug)

	assert.False(t, NeedsMachine(ToolReportBug), "a run with no machine can report a defect")
	assert.Equal(t, PermissionMutating, MinimumPermissionForTool(ToolReportBug))
	assert.Empty(t, UnattendedWithholding(ToolReportBug), "an unattended run reports what breaks it too")

	// Loadable everywhere load_tool reaches the registry, so an agent whose
	// preset never preloaded it still has a one-step route.
	caps := ResolveCapabilities(CapabilityInputs{
		Access:     ToolAccess{Preloaded: []string{ToolView}, LoadableAll: true},
		Permission: PermissionMutating,
		NoMachine:  true,
	})
	assert.True(t, caps.CanLoad(ToolReportBug))
}
