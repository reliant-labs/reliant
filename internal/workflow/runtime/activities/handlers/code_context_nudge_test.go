// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/tools/names"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func shellToolInputJSON(t *testing.T, command string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"command": command})
	require.NoError(t, err)
	return string(b)
}

func shellToolOutputJSON(t *testing.T, stdout string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"stdout": stdout, "stderr": "", "exit_code": 0})
	require.NoError(t, err)
	return string(b)
}

// Hits as rg prints them, for searches that name no file type: the nudge reads
// the files the matches came from to decide whether code_context can help.
const (
	goHits    = "internal/grpc/services/chat_crud.go:960:func (s *Service) UpdateWorkflowName(ctx context.Context) error {\n"
	protoHits = "proto/services/jobs/v1/jobs.proto:41:message ListJobsRequest {\n"
	goPaths   = "internal/threads/transition.go\ninternal/threads/transition_test.go\n"
)

// Every row is a command an agent actually ran, or the minimal variant of one
// that isolates a single clause of the rule documented on
// symbolFromShellCommand. "was" records the previous regex detector's verdict
// wherever it differs from "want", so the table is also the before/after
// record of what this rule changed.
func TestSymbolFromShellCommand(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
		output  string
		want    string
		was     string
	}{
		// --- The measured false positive (roofers dogfood run, 2026-10-05).
		{
			name:    "roofers: regex over .proto files, piped through an exclusion list",
			command: `for f in proto/services/*/v1/*.proto; do rg -n 'message List\w+Request' -A 14 $f | rg -v 'page_size|page_token|order_by|descending' | rg -B0 'optional|Request'; done`,
			output:  protoHits,
			want:    "", was: "page_token",
		},
		{name: "rg -v exclusion", command: `rg -n -v 'ResolveDaemon' internal/daemon/router.go`, want: "", was: "ResolveDaemon"},
		{name: "bundled -nv exclusion", command: `rg -nv 'ResolveDaemon' internal/daemon/router.go`, want: "", was: "ResolveDaemon"},
		{name: "alternation of identifiers", command: `rg -n 'ThreadModeNew|ThreadModeInherit' --glob '!*_test.go' internal/`, output: goHits, want: "", was: "ThreadModeInherit"},
		{name: "alternation via repeated -e", command: `rg -n -e 'ResolveDaemon' -e 'RouteDaemon' internal/daemon/router.go`, want: "", was: "ResolveDaemon"},
		{name: "regex metacharacters: call site", command: `rg -n 'CreateWorkflow\(ctx' --glob '!*_test.go' internal/`, output: goHits, want: "", was: "CreateWorkflow"},
		{name: "regex metacharacters: declaration", command: `rg -n 'func ValidateInputs' -A 50 internal/workflow/validation/*.go`, want: "", was: "ValidateInputs"},
		{name: "regex metacharacters: wildcard", command: `rg -n 'func .*Handler' internal/`, output: goHits, want: ""},
		{name: ".proto file target", command: `rg -n 'ListJobsRequest' proto/services/jobs/v1/jobs.proto`, want: "", was: "ListJobsRequest"},
		{name: ".proto files through a loop variable", command: `for f in proto/services/*/v1/*.proto; do rg -n 'ListJobsRequest' $f; done`, want: "", was: "ListJobsRequest"},
		{name: "-t proto scope", command: `rg -n -t proto 'ListJobsRequest'`, output: protoHits, want: "", was: "ListJobsRequest"},
		{name: "--glob *.sql scope", command: `rg -n --glob '*.sql' 'JobStatus' db/`, want: ""},
		{name: "directory target whose hits are .proto", command: `rg -n 'ListJobsRequest' proto/`, output: protoHits, want: "", was: "ListJobsRequest"},
		{name: "mixed Go and Markdown targets", command: `rg -n 'ResolveDaemon' internal/daemon/router.go docs/daemon.md`, want: "", was: "ResolveDaemon"},
		{name: "snake_case in a Go file", command: `rg -n 'page_token' internal/handlers/jobs/service.go`, want: "", was: "page_token"},
		{name: "stdin filter: rg after a pipe", command: `cat internal/daemon/router.go | rg 'ResolveDaemon'`, want: "", was: "ResolveDaemon"},
		{name: "stdin filter: grep without -r", command: `go doc ./internal/daemon | grep 'ResolveDaemon'`, want: "", was: "ResolveDaemon"},
		{name: "unscoped with no hits", command: `rg -n 'UpdateWorkflowName' .`, output: "", want: "", was: "UpdateWorkflowName"},

		// --- Lookups that must keep firing.
		{name: "unscoped, hits in Go", command: `rg -n 'UpdateWorkflowName' --glob '!gen/**' . | head`, output: goHits, want: "UpdateWorkflowName"},
		{name: "unscoped -l in a substitution, Go paths", command: `f=$(rg -l 'TransitionChatOnCompletion' --glob '!gen/**' .); echo "$f"`, output: goPaths, want: "TransitionChatOnCompletion"},
		{name: "unscoped, hits in Go and proto", command: `rg -n 'ListJobsRequest' .`, output: protoHits + "gen/services/jobs/v1/jobs.pb.go:88:type ListJobsRequest struct {\n", want: "ListJobsRequest"},
		{name: "Go glob target", command: `rg -n 'CancelChatToolCalls' -B5 -A45 internal/threads/*.go`, want: "CancelChatToolCalls"},
		{name: "Go glob target with an exclusion glob", command: `rg -n 'ResumeInput' --glob '!gen/**' internal/workflow/runtime/*.go`, want: "ResumeInput"},
		{name: "TypeScript file target", command: `rg -n 'useChatStream' web/src/hooks/useChat.ts`, want: "useChatStream"},
		{name: "-t go", command: `rg -n -t go 'ResolveDaemon'`, want: "ResolveDaemon"},
		{name: "--type=ts", command: `rg --type=ts -n 'useChatStream'`, want: "useChatStream"},
		{name: "-g with a brace glob", command: `rg -n -g '*.{ts,tsx}' 'useChatStream' web/`, want: "useChatStream", was: "(silent)"},
		{name: "grep -r --include", command: `grep -rn 'ResolveDaemon' --include='*.go' internal/`, want: "ResolveDaemon"},
		{name: "git grep with a pathspec", command: `git grep -n 'ResolveDaemon' -- '*.go'`, want: "ResolveDaemon"},
		{name: "Go files through a loop variable", command: `for f in internal/svc/*.go; do rg -n 'ResolveDaemon' $f; done`, want: "ResolveDaemon"},
		{name: "double-quoted pattern", command: `rg -n "ResolveDaemon" internal/daemon/*.go`, want: "ResolveDaemon"},

		// --- Not searches, or not symbols.
		{name: "free text", command: `rg -n 'TODO' internal/daemon/router.go`, want: ""},
		{name: "short name", command: `rg -n 'ok' internal/daemon/router.go`, want: ""},
		{name: "lone lowercase word", command: `rg -n 'workflow' internal/daemon/router.go`, want: ""},
		{name: "file listing", command: `rg --files -g '*.go' internal/`, want: ""},
		{name: "no search at all", command: `go build ./...`, want: ""},
		{name: "Python", command: `rg -n 'HandleRequest' src/main.py`, want: ""},
		{name: "Ruby", command: `rg -n 'HandleRequest' app/models/user.rb`, want: ""},
		{name: "unparseable shell", command: `rg -n 'ResolveDaemon' $(`, want: "", was: "ResolveDaemon"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, symbolFromShellCommand(tc.command, tc.output))
		})
	}
}

// The shell tool's result is a JSON document; the evidence is in its stdout.
func TestNudge_ReadsHitsFromShellResultJSON(t *testing.T) {
	resetNudgeState()
	in := shellToolInputJSON(t, `rg -n 'UpdateWorkflowName' .`)

	assert.Empty(t, maybeCodeContextNudge(names.ToolShell, in, shellToolOutputJSON(t, protoHits), "proto-thread"),
		"hits only in .proto: code_context cannot resolve them")
	assert.Contains(t, maybeCodeContextNudge(names.ToolShell, in, shellToolOutputJSON(t, goHits), "go-thread"),
		`code_context(symbol: "UpdateWorkflowName")`)
}

// goLookup is a qualifying search that needs no output evidence.
const goLookup = `rg -n 'ResolveDaemon' internal/daemon/*.go`

// First qualifying call fires — that is when redirecting is cheapest.
func TestNudge_FiresOnFirstQualifyingCall(t *testing.T) {
	resetNudgeState()
	got := maybeCodeContextNudge(names.ToolShell, shellToolInputJSON(t, goLookup), "", "thread-1")
	assert.Contains(t, got, `code_context(symbol: "ResolveDaemon")`)
}

func TestNudge_SuppressedDuringCooldown(t *testing.T) {
	resetNudgeState()
	in := shellToolInputJSON(t, goLookup)
	require.NotEmpty(t, maybeCodeContextNudge(names.ToolShell, in, "", "thread-1"))

	for i := 0; i < nudgeCooldownTurns-1; i++ {
		assert.Empty(t, maybeCodeContextNudge(names.ToolShell, in, "", "thread-1"),
			"call %d is inside the cooldown", i+2)
	}
	assert.NotEmpty(t, maybeCodeContextNudge(names.ToolShell, in, "", "thread-1"),
		"should fire again once the cooldown elapses")
}

// Ignored twice means it is being tuned out; a third is noise.
func TestNudge_StopsAfterMaxPerThread(t *testing.T) {
	resetNudgeState()
	in := shellToolInputJSON(t, goLookup)
	fired := 0
	for i := 0; i < nudgeCooldownTurns*5; i++ {
		if maybeCodeContextNudge(names.ToolShell, in, "", "thread-1") != "" {
			fired++
		}
	}
	assert.Equal(t, nudgeMaxPerThread, fired)
}

// Threads inline onto one Temporal workflow, so a spawned sub-agent must get
// its own budget rather than inheriting an exhausted parent's.
func TestNudge_BudgetIsPerThread(t *testing.T) {
	resetNudgeState()
	in := shellToolInputJSON(t, goLookup)
	for i := 0; i < nudgeCooldownTurns*5; i++ {
		maybeCodeContextNudge(names.ToolShell, in, "", "parent")
	}
	assert.Empty(t, maybeCodeContextNudge(names.ToolShell, in, "", "parent"), "parent exhausted")
	assert.NotEmpty(t, maybeCodeContextNudge(names.ToolShell, in, "", "child"),
		"a spawned thread starts with its own budget")
}

func TestNudge_OnlyForShellTool(t *testing.T) {
	resetNudgeState()
	in := shellToolInputJSON(t, goLookup)
	assert.Empty(t, maybeCodeContextNudge("view", in, "", "thread-1"))
	assert.Empty(t, maybeCodeContextNudge(names.ToolCodeContext, in, "", "thread-1"))
	assert.NotEmpty(t, maybeCodeContextNudge(names.ToolShell, in, "", "thread-1"))
}

func TestNudge_RequiresThreadID(t *testing.T) {
	resetNudgeState()
	assert.Empty(t, maybeCodeContextNudge(names.ToolShell, shellToolInputJSON(t, goLookup), "", ""))
}

// The hint names the symbol just searched for; a generic rule reads as
// boilerplate and gets skipped.
func TestNudge_NamesTheSymbolAndStaysOneNote(t *testing.T) {
	resetNudgeState()
	got := maybeCodeContextNudge(names.ToolShell,
		shellToolInputJSON(t, `rg -n 'CancelChatToolCalls' internal/threads/*.go`), "", "thread-1")

	require.NotEmpty(t, got)
	assert.Contains(t, got, "CancelChatToolCalls")
	assert.Equal(t, 1, strings.Count(got, "[IMPORTANT]"), "exactly one note")
	// Bounded, but not as tightly as the first version: a mild one-liner was
	// measured taking two deliveries and 47 intervening shell calls to land.
	// The extra sentences buy directiveness. Past ~600 it becomes a wall the
	// reader skips, which is the failure this bound exists to prevent.
	assert.Less(t, len(got), 600, "must stay a note, not a lecture")
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
