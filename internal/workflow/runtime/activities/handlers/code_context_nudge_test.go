// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/tools/names"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func shellInput(t *testing.T, command string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"command": command})
	require.NoError(t, err)
	return string(b)
}

// The trigger is validated against a REAL transcript, not invented examples.
// These are verbatim commands from a sub-agent that ran 76 tool calls while
// orienting in unfamiliar Go code and used code_context zero times.
func TestNudge_FiresOnRealSymbolSearches(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    string
	}{
		{`rg -n 'UpdateWorkflowName' --glob '!gen/**' . | head`, "UpdateWorkflowName"},
		{`f=$(rg -l 'TransitionChatOnCompletion' --glob '!gen/**' .); echo "$f"`, "TransitionChatOnCompletion"},
		{`rg -n 'CancelChatToolCalls' -B5 -A45 internal/threads/*.go`, "CancelChatToolCalls"},
		{`rg -n 'ResumeInput' --glob '!gen/**' internal/workflow/runtime/*.go`, "ResumeInput"},
		// Spellings of one concept.
		{`rg -n 'workflow_name|WorkflowName' --glob '!*_test.go' -l .`, "WorkflowName"},
		{`rg -n 'ThreadModeNew|ThreadModeInherit' --glob '!*_test.go' internal/`, "ThreadModeInherit"},
		// Call sites.
		{`rg -n 'CreateWorkflow\(ctx' --glob '!*_test.go' internal/`, "CreateWorkflow"},
		// A declaration search.
		{`rg -n 'func ValidateInputs' -A 50 internal/workflow/validation/*.go`, "ValidateInputs"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			assert.Equal(t, tc.want, symbolFromShellCommand(tc.command))
		})
	}
}

// A false positive trains the reader to skip the note, which costs more than
// the hint buys. These are also verbatim from the same transcript.
func TestNudge_SilentOnNonSymbolSearches(t *testing.T) {
	for name, command := range map[string]string{
		"structural outline":  `grep -n '^func \|^// ' internal/grpc/services/chat_send.go | head -60`,
		"proto grep":          `grep -n 'rpc \|^message \|workflow' proto/reliant/v1/chat.proto`,
		"package declaration": `rg -n 'package runs' -l .`,
		"file read":           `sed -n '960,1140p' internal/grpc/services/chat_crud.go`,
		"listing":             `ls internal/grpc/services/ | head -60`,
		"free text":           `rg -n 'TODO' .`,
		"regex shape":         `rg -n 'func .*Handler' internal/`,
		"no search at all":    `go build ./...`,
		"short name":          `rg -n 'ok' .`,
	} {
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, symbolFromShellCommand(command), "should not fire on %s", name)
		})
	}
}

// code_context cannot resolve Python or Ruby, so pointing an agent at it there
// would send it to a tool that degrades to the search it just ran.
func TestNudge_SilentForUnsupportedLanguages(t *testing.T) {
	assert.Empty(t, symbolFromShellCommand(`rg -n 'handle_request' app/models/user.rb`))
	assert.Empty(t, symbolFromShellCommand(`rg -n 'handle_request' src/main.py`))
	assert.NotEmpty(t, symbolFromShellCommand(`rg -n 'handleRequest' internal/svc/x.go`))
}

// First qualifying call fires — that is when redirecting is cheapest.
func TestNudge_FiresOnFirstQualifyingCall(t *testing.T) {
	resetNudgeState()
	got := maybeCodeContextNudge(names.ToolBash, shellInput(t, `rg -n 'ResolveDaemon' internal/`), "thread-1")
	assert.Contains(t, got, `code_context(symbol: "ResolveDaemon")`)
}

func TestNudge_SuppressedDuringCooldown(t *testing.T) {
	resetNudgeState()
	in := shellInput(t, `rg -n 'ResolveDaemon' internal/`)
	require.NotEmpty(t, maybeCodeContextNudge(names.ToolBash, in, "thread-1"))

	for i := 0; i < nudgeCooldownTurns-1; i++ {
		assert.Empty(t, maybeCodeContextNudge(names.ToolBash, in, "thread-1"),
			"call %d is inside the cooldown", i+2)
	}
	assert.NotEmpty(t, maybeCodeContextNudge(names.ToolBash, in, "thread-1"),
		"should fire again once the cooldown elapses")
}

// Ignored twice means it is being tuned out; a third is noise.
func TestNudge_StopsAfterMaxPerThread(t *testing.T) {
	resetNudgeState()
	in := shellInput(t, `rg -n 'ResolveDaemon' internal/`)
	fired := 0
	for i := 0; i < nudgeCooldownTurns*5; i++ {
		if maybeCodeContextNudge(names.ToolBash, in, "thread-1") != "" {
			fired++
		}
	}
	assert.Equal(t, nudgeMaxPerThread, fired)
}

// Threads inline onto one Temporal workflow, so a spawned sub-agent must get
// its own budget rather than inheriting an exhausted parent's.
func TestNudge_BudgetIsPerThread(t *testing.T) {
	resetNudgeState()
	in := shellInput(t, `rg -n 'ResolveDaemon' internal/`)
	for i := 0; i < nudgeCooldownTurns*5; i++ {
		maybeCodeContextNudge(names.ToolBash, in, "parent")
	}
	assert.Empty(t, maybeCodeContextNudge(names.ToolBash, in, "parent"), "parent exhausted")
	assert.NotEmpty(t, maybeCodeContextNudge(names.ToolBash, in, "child"),
		"a spawned thread starts with its own budget")
}

func TestNudge_OnlyForShellTool(t *testing.T) {
	resetNudgeState()
	in := shellInput(t, `rg -n 'ResolveDaemon' internal/`)
	assert.Empty(t, maybeCodeContextNudge("view", in, "thread-1"))
	assert.Empty(t, maybeCodeContextNudge(names.ToolCodeContext, in, "thread-1"))
	assert.NotEmpty(t, maybeCodeContextNudge(names.ToolBash, in, "thread-1"))
}

func TestNudge_RequiresThreadID(t *testing.T) {
	resetNudgeState()
	assert.Empty(t, maybeCodeContextNudge(names.ToolBash,
		shellInput(t, `rg -n 'ResolveDaemon' internal/`), ""))
}

// The hint names the symbol just searched for; a generic rule reads as
// boilerplate and gets skipped.
func TestNudge_NamesTheSymbolAndStaysOneNote(t *testing.T) {
	resetNudgeState()
	got := maybeCodeContextNudge(names.ToolBash,
		shellInput(t, `rg -n 'CancelChatToolCalls' internal/threads/`), "thread-1")

	require.NotEmpty(t, got)
	assert.Contains(t, got, "CancelChatToolCalls")
	assert.Equal(t, 1, strings.Count(got, "[IMPORTANT]"), "exactly one note")
	// Bounded, but not as tightly as the first version: a mild one-liner was
	// measured taking two deliveries and 47 intervening shell calls to land.
	// The extra sentences buy directiveness. Past ~600 it becomes a wall the
	// reader skips, which is the failure this bound exists to prevent.
	assert.Less(t, len(got), 600, "must stay a note, not a lecture")
}

// Coverage measured against the real transcript: this is the number that says
// whether the trigger is worth having at all.
func TestNudge_CoversMostRealSymbolSearches(t *testing.T) {
	searches := []string{
		`rg -n 'workflow_name|WorkflowName' --glob '!*_test.go' -l .`,
		`rg -n 'UpdateWorkflowName' --glob '!gen/**' .`,
		`f=$(rg -l 'TransitionChatOnCompletion' --glob '!gen/**' .)`,
		`rg -n 'ResumeInput' --glob '!gen/**' internal/workflow/runtime/*.go`,
		`rg -n 'CancelChatToolCalls' -B5 -A45 internal/threads/*.go`,
		`rg -n 'GetPendingApproval|PendingApprovals|ListPendingApprovals' internal/db/repository.go`,
		`rg -n 'ThreadModeNew|ThreadModeInherit' --glob '!*_test.go' internal/`,
		`rg -n 'CreateWorkflow\(ctx' --glob '!*_test.go' internal/`,
		`rg -n 'func ValidateInputs' -A 50 internal/workflow/validation/*.go`,
	}
	hits := 0
	for _, s := range searches {
		if symbolFromShellCommand(s) != "" {
			hits++
		}
	}
	assert.GreaterOrEqual(t, hits, 8, fmt.Sprintf("only %d/%d real searches matched", hits, len(searches)))
}

// The nudge is for the MODEL, not the record. A tip appended to the stored
// tool-call row would render in the UI as if the command had printed it, and
// would still be there on reload — the transcript must stay a faithful record
// of what actually ran.
func TestNudge_DoesNotEnterDurableContent(t *testing.T) {
	src, err := os.ReadFile("execute_tools.go")
	require.NoError(t, err)
	body := string(src)

	require.Contains(t, body, "durableContent := result.Content",
		"the pre-nudge content must be captured for the durable write")
	assert.Contains(t, body, "&toolCallResultWrite{content: durableContent, isError: isError}",
		"the success-path write must persist the command's real output, not the nudged copy")
}
