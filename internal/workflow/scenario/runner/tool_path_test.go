// Copyright (c) 2025 Reliant Labs
package runner

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/require"
)

// These tests drive the real write_scenario tool — wired to the runner exactly
// as the server wires it — end to end, pinning that every assertion a scenario
// can express reaches the runner and is enforced. The tool used to parse
// stored YAML into a private mirror struct that silently dropped fields
// (completed:, skipped:, outputs:, typed events), so a scenario could report
// PASS through the tool while CI enforced something else.

// workflowWithFailingNode makes `boom` fail during config evaluation. The index
// is out of range, which COMPILES fine and only blows up when evaluated — a
// static-validation failure would be rejected before the runner ever runs,
// and so would never exercise node-level error bookkeeping.
const workflowWithFailingNode = `name: error-probe
apiVersion: "1.0"
entry: [start]
nodes:
  - id: start
    type: save_message
    role: "assistant"
    content: "start"
  - id: boom
    type: save_message
    role: "assistant"
    content: "${{ [1, 2, 3][10] }}"
edges:
  - from: start
    to: boom
`

// workflowWithSkippedNode has a node guarded by a permanently false condition.
// `maybe` is therefore SKIPPED, never completed — which is precisely the case
// `reached:` cannot distinguish and `completed:` exists to catch.
const workflowWithSkippedNode = `name: skip-probe
apiVersion: "1.0"
entry: [start]
nodes:
  - id: start
    type: save_message
    role: "assistant"
    content: "start"
  - id: maybe
    type: save_message
    role: "assistant"
    content: "maybe"
    condition: "false"
  - id: done
    type: save_message
    role: "assistant"
    content: "done"
edges:
  - from: start
    to: maybe
  - from: maybe
    to: done
`

// seedScenarioDraft creates a chat and a workflow draft bound to it, so the
// scenario tools resolve the draft implicitly the way they do in production.
func seedScenarioDraft(t *testing.T, repo db.Repository, definition string) (*db.WorkflowDraft, string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	chatID := uuid.New().String()
	workflowName := "test-workflow"
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "Test Chat", ProjectID: "test-project", UserID: "test-user",
		WorkflowName: &workflowName, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	suffix := uuid.New().String()[:8]
	draft := &db.WorkflowDraft{
		ID: uuid.New().String(), UserID: "test-user",
		Name: "skip-probe-" + suffix, Slug: "skip-probe-" + suffix,
		Definition: definition, ChatID: &chatID, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, repo.CreateWorkflowDraft(ctx, draft))
	return draft, chatID
}

// writeScenario drives the real write_scenario tool, which saves the scenario,
// runs it on the runner, and returns the formatted result.
func writeScenario(t *testing.T, repo db.Repository, chatID, name, content string) string {
	t.Helper()
	input, err := json.Marshal(tools.WriteScenarioParams{Name: name, Content: content})
	require.NoError(t, err)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	resp, err := tools.NewWriteScenarioTool(repo, RunScenario).Run(
		rctx.NewToolContext(ctx, chatID, chatID, nil, nil),
		tools.ToolCall{ID: "c1", Input: string(input)})
	require.NoError(t, err)
	return resp.Content
}

func setupTestDB(t *testing.T) (db.Repository, func()) {
	t.Helper()
	return db.SetupTestDB(t)
}

// TestToolPath_CompletedAssertionIsEnforced is the regression test for the
// silent-drop bug. A scenario asserting `completed: [maybe]` where `maybe` was
// merely SKIPPED must FAIL. Before the fix the tool dropped Expectation.Completed
// on the floor, so the run reported PASSED — a false green on the exact
// assertion an agent would write to prove a node really ran.
//
// NOTE: this pins that the assertion REACHES the engine and is evaluated.
// That `completed:` is also PASSABLE for a node that genuinely ran is the
// separate concern of TestCompletedDistinguishesExecutedFromSkipped below.
func TestToolPath_CompletedAssertionIsEnforced(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	_, chatID := seedScenarioDraft(t, repo, workflowWithSkippedNode)

	out := writeScenario(t, repo, chatID, "completed_must_fail", `name: completed_must_fail
description: asserts a skipped node completed, which must fail
events: []
expect:
  outcome: completed
  completed:
    - maybe
`)

	if !strings.Contains(out, "FAILED") {
		t.Fatalf("scenario asserting completed:[maybe] on a SKIPPED node must FAIL, "+
			"but the tool reported:\n%s", out)
	}
	require.Contains(t, out, "to be completed",
		"the failure must name the completed assertion that was violated")
}

// TestToolPath_SkippedAssertionIsEnforced pins the other half: `skipped:` must
// be enforced too, in both directions.
func TestToolPath_SkippedAssertionIsEnforced(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	_, chatID := seedScenarioDraft(t, repo, workflowWithSkippedNode)

	// `maybe` really is skipped, so this must PASS.
	out := writeScenario(t, repo, chatID, "skipped_true", `name: skipped_true
description: maybe is genuinely skipped
events: []
expect:
  outcome: completed
  skipped:
    - maybe
`)
	require.Contains(t, out, "PASSED", "a true skipped: assertion must pass:\n%s", out)

	// `start` completed, so asserting it was skipped must FAIL.
	out = writeScenario(t, repo, chatID, "skipped_false", `name: skipped_false
description: asserts a completed node was skipped, which must fail
events: []
expect:
  outcome: completed
  skipped:
    - start
`)
	if !strings.Contains(out, "FAILED") {
		t.Fatalf("scenario asserting skipped:[start] on a COMPLETED node must FAIL, "+
			"but the tool reported:\n%s", out)
	}
}

// TestToolPath_TypedEventsSurvive covers the same class of bug on the event
// side. SimulatedEvent supports a typed mode (type/text/tool_calls/tool/
// tool_output/is_error) that the tool's mirror struct never declared, so every
// typed event stored through the tools decayed into an empty raw-output event.
func TestToolPath_TypedEventsSurvive(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	const workflow = `name: typed-probe
apiVersion: "1.0"
entry: [call_llm]
nodes:
  - id: call_llm
    type: call_llm
    args:
      model: mock
edges: []
`
	_, chatID := seedScenarioDraft(t, repo, workflow)

	// node_outputs asserts on the event's payload, so this can only pass if the
	// typed fields survived. A dropped `text:` yields an empty response_text.
	out := writeScenario(t, repo, chatID, "typed_event", `name: typed_event
description: typed llm_response event must reach the runner intact
events:
  - node: call_llm
    type: llm_response
    text: "hello from the typed event"
expect:
  outcome: completed
  reached:
    - call_llm
  node_outputs:
    call_llm:
      response_text: "hello from the typed event"
`)
	require.Contains(t, out, "PASSED",
		"a typed llm_response event must be carried through the tool path:\n%s", out)
}

// TestToolPath_OutputsAssertionIsEnforced covers Expectation.Outputs, the third
// field the mirror struct omitted.
func TestToolPath_OutputsAssertionIsEnforced(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	const workflow = `name: outputs-probe
apiVersion: "1.0"
entry: [call_llm]
nodes:
  - id: call_llm
    type: call_llm
    args:
      model: mock
edges: []
outputs:
  answer: "nodes.call_llm.response_text"
`
	_, chatID := seedScenarioDraft(t, repo, workflow)

	out := writeScenario(t, repo, chatID, "outputs_must_fail", `name: outputs_must_fail
description: asserts a workflow output value that is wrong, which must fail
events:
  - node: call_llm
    type: llm_response
    text: "actual answer"
expect:
  outcome: completed
  outputs:
    answer: "a completely different answer"
`)
	if !strings.Contains(out, "FAILED") {
		t.Fatalf("scenario asserting a wrong outputs: value must FAIL, but the tool reported:\n%s", out)
	}
}

// TestCompletedDistinguishesExecutedFromSkipped is the acceptance pair for
// `expect.completed:`, the only assertion that can prove a node genuinely RAN.
//
// `reached:` deliberately counts skipped nodes, so it cannot catch a node that
// was wrongly skipped. That makes these two halves inseparable: a `completed:`
// that passes for an executed node is worthless unless it also FAILS for a
// skipped one, since an implementation that marks everything completed would
// satisfy the first half alone.
func TestCompletedDistinguishesExecutedFromSkipped(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	_, chatID := seedScenarioDraft(t, repo, workflowWithSkippedNode)

	// `start` and `done` genuinely execute, so asserting they completed passes.
	out := writeScenario(t, repo, chatID, "completed_passes_for_executed", `name: completed_passes_for_executed
description: start and done really do execute
events: []
expect:
  outcome: completed
  completed:
    - start
    - done
`)
	require.NotContains(t, out, "FAILED",
		"expect.completed must pass for nodes that genuinely executed:\n%s", out)

	// `maybe` is guarded by `condition: "false"`, so it is reached but never runs.
	out = writeScenario(t, repo, chatID, "completed_fails_for_skipped", `name: completed_fails_for_skipped
description: asserts a skipped node completed, which must fail
events: []
expect:
  outcome: completed
  completed:
    - maybe
`)
	require.Contains(t, out, "FAILED",
		"expect.completed must NOT pass for a node that was only skipped:\n%s", out)
	require.Contains(t, out, `expected node "maybe" to be completed but it wasn't`,
		"the failure must name the unmet completion, not some unrelated mismatch:\n%s", out)

	// The same node IS still reported as skipped, which is exactly the
	// distinction `completed:` exists to draw.
	require.Regexp(t, `skipped: +maybe`, out,
		"a node skipped by a false condition must still be reported as skipped:\n%s", out)
}

// TestErrorStateIsRecordedForFailingNode pins the third state. Before node
// bookkeeping was fixed, NodeStateError was declared and read but never
// assigned, so a failing node was reported as neither completed nor errored and
// the run_scenario summary could not say where the run died.
func TestErrorStateIsRecordedForFailingNode(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	_, chatID := seedScenarioDraft(t, repo, workflowWithFailingNode)

	out := writeScenario(t, repo, chatID, "boom_errors", `name: boom_errors
description: boom fails config evaluation and is attributed as the error node
events: []
expect:
  outcome: error
  error_node: boom
  completed:
    - start
`)

	require.NotContains(t, out, "FAILED",
		"outcome: error with error_node: boom must pass for a node that really failed:\n%s", out)
	require.Regexp(t, `errored: +boom`, out,
		"the failing node must be reported in the errored list:\n%s", out)
	require.Regexp(t, `completed: +start`, out,
		"a node that ran before the failure must still be reported completed:\n%s", out)
}
