// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"go.temporal.io/sdk/activity"

	"github.com/reliant-labs/reliant/internal/daemonoffline"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// The tool calls' usage ledger (tool_call_usage, agent-analytics.md §3.5):
// one count-only row per tool call a run executes, written beside the call's
// execute_tool span by the two activities that open one (execute_tools and
// invoke_tool, through meteredToolCall). It feeds the tool and daemon error
// rates of Agents › Insights, which outlive the spans' seven days.
//
// What a row says about how the call ended comes from the path that answered
// it, never from its text: each path that knows why a call failed says so on
// the call's toolCallNote (noteToolError), and a result no path classified is
// ToolErrorUnknown.

// ToolUsageRecorder is what a tool-running activity needs of the usage
// ledger: one idempotent write per tool call.
type ToolUsageRecorder interface {
	RecordToolCallUsage(ctx context.Context, rec store.ToolCallUsageRecord) (bool, error)
}

// toolUsageWriteTimeout bounds the ledger write. It is a service record, so a
// slow database must not hold up the call it describes.
const toolUsageWriteTimeout = 5 * time.Second

// toolCallNote is what the paths that answered one tool call learned about
// how it ended. The paths run on the call's own goroutine, but a note is
// safe to write from any.
type toolCallNote struct {
	mu         sync.Mutex
	errorClass string
	cancelled  bool
	// replayed is a call answered from the result an earlier dispatch of it
	// recorded (checkPriorTerminalResult): it did not run, so it writes no
	// row; its first execution wrote one.
	replayed bool
}

type toolCallNoteKey struct{}

// withToolCallNote returns ctx carrying a fresh note for one call.
func withToolCallNote(ctx context.Context) (context.Context, *toolCallNote) {
	n := &toolCallNote{}
	return context.WithValue(ctx, toolCallNoteKey{}, n), n
}

func toolCallNoteFrom(ctx context.Context) *toolCallNote {
	n, _ := ctx.Value(toolCallNoteKey{}).(*toolCallNote)
	return n
}

// noteToolError says why the call ctx carries failed: one of the store's
// ToolError* classes. The last word wins; a ctx with no note ignores it.
func noteToolError(ctx context.Context, class string) {
	if n := toolCallNoteFrom(ctx); n != nil {
		n.mu.Lock()
		n.errorClass = class
		n.mu.Unlock()
	}
}

// noteToolCancelled says the call ctx carries was cancelled, not failed.
func noteToolCancelled(ctx context.Context) {
	if n := toolCallNoteFrom(ctx); n != nil {
		n.mu.Lock()
		n.cancelled = true
		n.mu.Unlock()
	}
}

// noteToolReplayed says the call ctx carries was answered from a recorded
// result, without running.
func noteToolReplayed(ctx context.Context) {
	if n := toolCallNoteFrom(ctx); n != nil {
		n.mu.Lock()
		n.replayed = true
		n.mu.Unlock()
	}
}

// outcome is the call's outcome and error class, given its result.
func (n *toolCallNote) outcome(result message.ToolResult) (outcome, errorClass string, record bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	switch {
	case n.replayed:
		return "", "", false
	case n.cancelled:
		return store.ToolOutcomeCancelled, "", true
	case !result.IsError:
		return store.ToolOutcomeSuccess, "", true
	case n.errorClass != "":
		return store.ToolOutcomeError, n.errorClass, true
	}
	return store.ToolOutcomeError, store.ToolErrorUnknown, true
}

// noteExecError notes the executor's failure to run the call: a cancellation
// is the caller hanging up (the model calls' ledger counts it the same way),
// anything else is classified by execErrorClass.
func noteExecError(ctx context.Context, err error) {
	if errors.Is(err, context.Canceled) {
		noteToolCancelled(ctx)
		return
	}
	noteToolError(ctx, execErrorClass(err))
}

// execErrorClass classifies a failure to run a tool at all (the executor's
// error): its machine was not there, it ran out of time, or it could not be
// delivered or answered.
func execErrorClass(err error) string {
	switch {
	case toolexec.IsDaemonPending(err), daemonoffline.IsError(err):
		return store.ToolErrorDaemonOffline
	case errors.Is(err, context.DeadlineExceeded):
		return store.ToolErrorTimeout
	}
	return store.ToolErrorDispatch
}

// ranErrorClass classifies a tool that ran and answered with an error. The
// one answer that is not the tool's own is the machine-offline result the
// daemon wrapper writes when no daemon took the call (daemonoffline).
func ranErrorClass(content string) string {
	if daemonoffline.IsToolResultContent(content) {
		return store.ToolErrorDaemonOffline
	}
	return store.ToolErrorTool
}

// shippedTools is every tool Reliant ships, by name: true for an integration
// action. Built once: the registry and the integration catalog are static.
var shippedTools = sync.OnceValue(func() map[string]bool {
	out := map[string]bool{}
	for _, def := range tools.GetToolRegistry() {
		integration := false
		for _, tag := range def.Tags {
			if tag == tools.TagIntegration {
				integration = true
			}
		}
		out[def.Name] = integration
	}
	return out
})

// toolUsageKind is what kind of tool name is, and the name the ledger may
// record: only a tool Reliant ships is named. An MCP tool's name and a
// response tool's are the user's own, and a name outside the registry is
// whatever a model wrote. A feature-gated tool the call's capability set
// (caps; nil entitles nothing) does not entitle is recorded as a name outside
// the registry: to that caller it does not exist.
func toolUsageKind(name string, response bool, caps *tools.Capabilities) (kind, recordedName string) {
	switch {
	case response:
		return store.ToolKindResponse, ""
	case strings.HasPrefix(name, tools.MCPToolPrefix):
		return store.ToolKindMCP, ""
	}
	integration, ok := shippedTools()[name]
	switch {
	case !ok || caps.HidesTool(name):
		return store.ToolKindUnknown, ""
	case integration:
		return store.ToolKindIntegration, name
	}
	return store.ToolKindBuiltin, name
}

// toolUsageChats is what the meter reads of a call's chat: its owner, org
// and project.
type toolUsageChats interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
}

// toolUsageMeter records the tool calls of one activity execution, all of
// one chat. The chat is read on the first call that records, and kept once
// read: a failed read is tried again by the next call.
type toolUsageMeter struct {
	recorder ToolUsageRecorder
	chats    toolUsageChats
	chatID   string
	thread   string

	mu   sync.Mutex
	chat *db.Chat
}

// newToolUsageMeter meters one execution's tool calls in chatID's thread. A
// nil recorder records nothing.
func newToolUsageMeter(recorder ToolUsageRecorder, chats toolUsageChats, chatID, thread string) *toolUsageMeter {
	return &toolUsageMeter{recorder: recorder, chats: chats, chatID: chatID, thread: thread}
}

// chatRow is the meter's chat, read with ctx, which must outlive the
// activity's cancellation (record's write context): a cancelled call is
// still recorded, and a read on a cancelled context would fail.
func (m *toolUsageMeter) chatRow(ctx context.Context) *db.Chat {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.chat != nil || m.chats == nil || m.chatID == "" {
		return m.chat
	}
	chat, err := m.chats.GetChat(ctx, m.chatID)
	if err != nil {
		logging.WarnContext(ctx, "could not read the chat to record tool usage", "chatID", m.chatID, "error", err)
		return nil
	}
	m.chat = chat
	return chat
}

// meteredToolCall is one tool call as the span and the ledger see it.
type meteredToolCall struct {
	name       string
	callID     string
	location   string
	response   bool
	turnParent string
}

// run runs one tool call under its execute_tool span and records it: run is
// handed the call's ctx (the span and the call's note on it) and answers the
// call. The row's duration is the span's. A run that panics records the call
// as an internal failure, ends its span, and panics on.
func (m *toolUsageMeter) run(ctx context.Context, call meteredToolCall, run func(ctx context.Context) message.ToolResult) (result message.ToolResult) {
	callCtx, span := telemetry.StartToolCall(ctx, call.name, call.callID, call.location, call.turnParent)
	callCtx, note := withToolCallNote(callCtx)
	defer func() {
		if r := recover(); r != nil {
			noteToolError(callCtx, store.ToolErrorInternal)
			span.End(true, nil)
			m.record(ctx, call, note, message.ToolResult{IsError: true}, span.Duration())
			panic(r)
		}
	}()
	result = run(callCtx)
	span.End(result.IsError, nil)
	m.record(ctx, call, note, result, span.Duration())
	return result
}

// record writes the call's row. A call answered from a recorded result
// writes none, and nor does a call with no chat owner to file it under. A
// failed write is logged and dropped: usage accounting never fails the work
// it describes.
func (m *toolUsageMeter) record(ctx context.Context, call meteredToolCall, note *toolCallNote, result message.ToolResult, d time.Duration) {
	if m == nil || m.recorder == nil || !activity.IsActivity(ctx) {
		return
	}
	outcome, errorClass, ok := note.outcome(result)
	if !ok {
		return
	}
	// Detached and bounded: a cancelled call is still one to record, so
	// neither the chat's read nor the write may see the activity's
	// cancellation, and a slow database must not hold up the call.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), toolUsageWriteTimeout)
	defer cancel()
	chat := m.chatRow(wctx)
	if chat == nil || chat.UserID == "" {
		return
	}
	info := activity.GetInfo(ctx)
	run := usageRunOf(ctx)
	if run.ThreadID == "" {
		run.ThreadID = m.thread
	}
	kind, name := toolUsageKind(call.name, call.response, tools.CapabilitiesFrom(ctx))
	rec := store.ToolCallUsageRecord{
		ID:           toolUsageID(info.WorkflowExecution.ID, info.WorkflowExecution.RunID, info.ActivityID, call.callID),
		Attempt:      info.Attempt,
		UserID:       chat.UserID,
		OrgID:        chatOrgID(chat),
		ChatID:       chat.ID,
		ProjectID:    chat.ProjectID,
		WorkflowID:   run.WorkflowID,
		ThreadID:     run.ThreadID,
		WorkflowName: run.WorkflowName,
		NodeID:       run.NodeID,
		Preset:       run.Preset,
		AIRunID:      run.AIRunID,
		AIRootRunID:  run.AIRootRunID,
		ToolName:     name,
		ToolKind:     kind,
		Location:     call.location,
		Outcome:      outcome,
		ErrorClass:   errorClass,
		DurationMs:   d.Milliseconds(),
		CreatedAt:    time.Now(),
	}
	if _, err := m.recorder.RecordToolCallUsage(wctx, rec); err != nil {
		logging.WarnContext(ctx, "could not record tool usage", "chatID", chat.ID, "toolKind", kind, "error", err)
	}
}

// toolUsageID is a call's key within its activity: the activity (workflow id,
// run id, activity id) and the call's tool_call_id, which is unique within
// the batch the activity runs.
func toolUsageID(workflowID, runID, activityID, toolCallID string) string {
	return workflowID + "/" + runID + "/" + activityID + "/" + toolCallID
}
