// Copyright (c) 2025 Reliant Labs
//
//go:build e2e

package stories

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonoffline"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/toolexec"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// storyDaemonOfflinePauseGrace stands in for runtime.DaemonOfflinePauseGrace
// (two minutes) for this test binary; TestMain installs it. The stories run on
// a real Temporal dev server, whose workflow clock cannot be skipped.
const storyDaemonOfflinePauseGrace = time.Second

// daemonOfflineAttempt is how long each offline tool call takes. Three of
// them span more than storyDaemonOfflinePauseGrace by construction, so the
// third strike is always past the grace however fast the machine runs: the
// pause lands on exactly the third iteration.
const daemonOfflineAttempt = 600 * time.Millisecond

// daemonOfflineExecutor simulates the exact shape RemoteExecutor produces
// when the user's machine is not connected: a NON-error activity result whose
// tool results are error-shaped and carry the "no daemon connected"
// substring. (Go-level activity errors are deliberately neutral for the
// breaker — see DaemonOfflineCircuitBreaker docs.)
type daemonOfflineExecutor struct{}

func (daemonOfflineExecutor) ExecuteTool(ctx context.Context, req *toolexec.ToolRequest) (*toolexec.ToolResult, error) {
	select {
	case <-time.After(daemonOfflineAttempt):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	now := time.Now()
	return &toolexec.ToolResult{
		Success:      false,
		IsError:      true,
		Content:      "Failed to execute tool on daemon: unavailable: " + daemonoffline.ErrorSubstring + " for user",
		ErrorCode:    "DAEMON_EXECUTION_ERROR",
		ErrorMessage: daemonoffline.ErrorSubstring,
		StartTime:    now,
		EndTime:      now,
	}, nil
}

func (daemonOfflineExecutor) Close() error { return nil }

// Story 05: the user's machine goes away mid-conversation. Three consecutive
// agent-loop iterations see every daemon-targeted tool result fail with "no
// daemon connected", over longer than the breaker's grace; the circuit breaker
// (internal/workflow/runtime/daemon_offline_tracker.go, threshold 3 plus the
// grace 2e295eb6 added so a gateway rollout cannot force a manual resume) must
// pause the workflow with DaemonOfflinePauseMessage instead of burning LLM
// turns forever. Sending a message resumes the workflow and it completes.
func TestStory05_DaemonOfflineCircuitBreakerPausesAndResumes(t *testing.T) {
	t.Parallel()

	toolTurn := func(id string) Turn {
		return Turn{
			Text: "Trying to run a command.",
			ToolCalls: []message.ToolCall{
				ToolCall(id, tools.ShellToolName, `{"command":"echo hello"}`),
			},
		}
	}

	script := NewScriptedLLM(
		// Three consecutive iterations of daemon-offline tool failures.
		toolTurn("call-off-1"),
		toolTurn("call-off-2"),
		toolTurn("call-off-3"),
		// Played only AFTER the user resumes: finish without tools.
		Turn{Text: "Welcome back — wrapping up."},
	)

	h := newHarness(t, script, WithToolExecutor(daemonOfflineExecutor{}))

	created := h.StartChat("builtin://agent", "Run a command for me", map[string]any{
		"mode": "auto",
	})
	chatID := created.Chat.Id
	workflowID := created.WorkflowId

	// 1. Breaker trips after 3 strikes: workflow self-pauses.
	h.WaitWorkflowStatus(workflowID, db.Paused())

	// The user-facing pause message is delivered as an error chat_update
	// (WorkflowError activity → chat_updates → per-chat websocket). Read it
	// through the production GetChatUpdates handler.
	updatesResp, err := h.ChatSvc.GetChatUpdates(h.Ctx, connect.NewRequest(&reliantv1.GetChatUpdatesRequest{
		ChatId: chatID,
	}))
	require.NoError(t, err)
	var pauseMsgSeen bool
	for _, u := range updatesResp.Msg.Updates {
		// UpdateType is the proto enum string (CHAT_UPDATE_TYPE_ERROR).
		if strings.Contains(u.UpdateType, "ERROR") && strings.Contains(u.Data, v2.DaemonOfflinePauseMessage) {
			pauseMsgSeen = true
		}
	}
	require.True(t, pauseMsgSeen, `chat updates must contain the daemon-offline pause notice; got %d updates`, len(updatesResp.Msg.Updates))

	// Exactly 3 LLM turns were burned before pausing — the breaker's whole
	// point is bounding this.
	require.Len(t, h.LLM.StreamCalls(), 3, "breaker must pause after exactly 3 offline iterations")

	// 2. The user "restarts their machine" and sends a message → SendMessage
	// routes paused chats through PauseService.ResumeWorkflow.
	h.SendMessage(chatID, "My machine is back, please continue")

	h.WaitTemporalWorkflowDone(workflowID)
	h.WaitWorkflowStatus(workflowID, db.Completed())

	// The post-resume turn ran and the chat is healthy.
	require.Len(t, h.LLM.StreamCalls(), 4, "exactly one more LLM turn after resume")
	assert.False(t, h.LLM.Exhausted())
	assert.Equal(t, db.ChatStateIdle, h.Chat(chatID).State)

	var sawFinal bool
	for _, m := range h.Messages(chatID, workflowID) {
		if strings.Contains(TextOf(m), "Welcome back — wrapping up.") {
			sawFinal = true
		}
	}
	assert.True(t, sawFinal, "final assistant message after resume must be persisted")
}
