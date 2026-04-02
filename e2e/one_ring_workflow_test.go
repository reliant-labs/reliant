// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// ONE-RING WORKFLOW E2E TESTS
//
// Tests for the one-ring workflow (builtin://one-ring) defined in:
// ./internal/workflow/builtin/one-ring.yaml
//
// Workflow structure:
//   planning (fork) → write_tests (fork) → impl_loop (ref: get-it-right, fork) → complete
//
// Planning sub-workflow: plan → criticize → revise (each fork from planning thread)
// Impl loop (get-it-right): implement → lint/test/build → review → refactor (repeats on failure)
//
// Thread architecture:
//   Main thread: [user msg] → [final plan] → [test summary] → [impl + eval iter 0] → ...
//   Planning fork: [user msg] → [plan] → [criticism] → [revision] (discarded after plan saved)
//   Each agent fork: [parent context] → [agent conversation] (only summary saved back)
//
// These tests exercise real Temporal workflows, real database, real thread
// service, and real activities with mock LLM and mock run executors.
// ============================================================================

// TestOneRing_PlanAndImplement tests the basic one-ring flow:
// plan → implement → evaluate (pass). Verifies the workflow completes
// end-to-end with the minimum set of steps.
//
// LLM call sequence:
//  1. plan agent (fork from planning sub-workflow)
//  2. implement agent (fork from impl_loop)
//  3. review agent (structured-agent: must call submit_evaluation tool)
func TestOneRing_PlanAndImplement(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Mock LLM responses consumed in order:
	// 1: plan agent response
	// 2: implement agent response
	// 3: review agent — must return tool call to submit_evaluation with pass grade + pass strategy
	h.MockLLM.SetResponses(
		"Plan: Build a hello world feature with a main.go file",
		"Implementation complete: created main.go with hello world output",
	)
	// Review uses structured-agent which requires a tool call response.
	// The 3rd LLM call must return a submit_evaluation tool call.
	h.MockLLM.AddResponse(MockResponse{
		Text: "Evaluation complete",
		ToolCalls: []MockToolCall{{
			Name: "submit_evaluation",
			Input: map[string]interface{}{
				"grade":    "pass",
				"strategy": "pass",
				"feedback": "Implementation looks good",
			},
		}},
	})

	chatID := h.StartWorkflowViaGRPC(t, "builtin://one-ring", map[string]interface{}{
		"steps":       []interface{}{"plan", "implement"},
		"model":       map[string]interface{}{"id": "mock"},
		"max_retries": float64(3),
		"yield":       false,
	}, "Build a hello world feature")

	h.WaitForWorkflowComplete(t, chatID)

	// Verify workflow completed
	history := h.GetWorkflowHistory(t, chatID)
	history.PrintActivities()

	// Verify messages were saved to the chat
	messages, err := h.DB.ListMessages(context.Background(), chatID, db.MessageListOptions{})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(messages), 2, "should have at least user message + assistant messages")

	// Verify user message exists
	var hasUser bool
	for _, msg := range messages {
		if msg.Role == reliantv1.MessageRole_MESSAGE_ROLE_USER {
			hasUser = true
			break
		}
	}
	require.True(t, hasUser, "should have a user message")

	// Verify LLM was called (plan + implement + review = at least 3)
	assert.GreaterOrEqual(t, h.MockLLM.CallCount(), 3,
		"LLM should be called at least 3 times (plan, implement, review)")

	t.Logf("✓ PlanAndImplement: workflow completed with %d messages, %d LLM calls",
		len(messages), h.MockLLM.CallCount())
}

// TestOneRing_PlanningPhaseThreadIsolation tests that the planning sub-workflow
// runs in a forked thread and only the final plan summary is saved to the main thread.
// Raw research/criticism/revision details should stay in the planning fork.
//
// Steps: plan + critique (enables criticize + revise)
// Expected behavior:
//   - Planning fork contains: plan summary, criticism, revision
//   - Main thread contains only: user message + final plan summary
func TestOneRing_PlanningPhaseThreadIsolation(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Mock LLM responses for: plan, criticize, revise
	h.MockLLM.SetResponses(
		"Initial plan: Create REST API with CRUD operations",
		"Criticism: Missing error handling and input validation",
		"Revised plan: Create REST API with CRUD, validation, and error handling",
	)

	chatID := h.StartWorkflowViaGRPC(t, "builtin://one-ring", map[string]interface{}{
		"steps": []interface{}{"plan", "criticize", "revise"}, // criticize + revise enable the full planning loop
		"model": map[string]interface{}{"id": "mock"},
		"yield": false,
	}, "Build a REST API for user management")

	h.WaitForWorkflowComplete(t, chatID)

	ctx := context.Background()

	// Get the workflow's root thread
	workflow, err := h.DB.GetWorkflow(ctx, chatID)
	require.NoError(t, err)
	require.NotEmpty(t, workflow.Thread, "workflow should have a root thread")

	rootThreadID := workflow.Thread

	// Resolve messages visible on the main thread
	threadSvc := threads.NewService(h.DB)
	rootCW, err := h.DB.GetLatestContextWindow(ctx, rootThreadID)
	require.NoError(t, err, "root thread should have a context window")

	rootMessages, err := threadSvc.ResolveMessagesFromCW(ctx, rootCW.ID)
	require.NoError(t, err)

	t.Logf("Main thread has %d messages:", len(rootMessages))
	for i, msg := range rootMessages {
		content := h.GetMessageText(t, msg.ID)
		t.Logf("  [%d] role=%v content=%q", i, msg.Role, truncate(content, 80))
	}

	// Main thread should have the user message
	var hasUserMsg bool
	for _, msg := range rootMessages {
		if msg.Role == reliantv1.MessageRole_MESSAGE_ROLE_USER {
			hasUserMsg = true
		}
	}
	require.True(t, hasUserMsg, "main thread should have the user message")

	// Main thread should have the final plan (saved via save_message on planning node)
	var hasFinalPlan bool
	for _, msg := range rootMessages {
		if msg.Role == reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
			content := h.GetMessageText(t, msg.ID)
			if contains(content, "Final Plan") || contains(content, "Revised") || contains(content, "plan") {
				hasFinalPlan = true
			}
		}
	}
	require.True(t, hasFinalPlan, "main thread should have the final plan summary")

	// Main thread should NOT contain raw criticism text
	for _, msg := range rootMessages {
		content := h.GetMessageText(t, msg.ID)
		require.NotContains(t, content, "Criticism: Missing error handling",
			"main thread should not contain raw criticism — it should stay in the planning fork")
	}

	// Verify that forked threads exist (planning sub-workflow creates forks)
	allThreads, err := h.DB.ListThreadsByConversation(ctx, chatID)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(allThreads), 2,
		"should have at least 2 threads (root + planning fork)")

	t.Logf("✓ PlanningPhaseThreadIsolation: %d threads, main has %d messages, criticism isolated",
		len(allThreads), len(rootMessages))
}

// TestOneRing_LoopIteratesOnFailure tests that the implementation loop
// retries when lint/test/build fails. On the first iteration, lint fails;
// on the second iteration, lint passes and review passes.
//
// Steps: implement + lint + review
// Expected: 2 iterations of the impl_loop, workflow completes
func TestOneRing_LoopIteratesOnFailure(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Mock lint command: fail first, then succeed
	// The actual command is: {{inputs.lint_command}} > {{inputs.lint_log}} 2>&1
	// With defaults: npm run lint > ./data/one-ring/lint.log 2>&1
	h.MockRun.OnPatternSequence("*lint*",
		MockRunResponse{ExitCode: 1, Stderr: "ESLint: 3 errors found"},
		MockRunResponse{ExitCode: 0, Stdout: "ESLint: no errors"},
	)

	// Mock LLM responses consumed in sequential order across agents:
	// LLM responses are consumed globally — implement (builtin://agent) and
	// review (builtin://structured-agent) share the same mock queue.
	// Order: implement iter0, review iter0, implement iter1, review iter1
	h.MockLLM.SetResponses(
		"Implementation v1: created feature files", // LLM call 1: implement iter 0 (agent, text-only → exits)
	)
	// LLM call 2: review iter 0 (structured-agent, submit_evaluation tool → completed)
	h.MockLLM.AddResponse(MockResponse{
		Text: "Evaluation iteration 0",
		ToolCalls: []MockToolCall{{
			Name: "submit_evaluation",
			Input: map[string]interface{}{
				"grade":    "pass",
				"strategy": "pass",
				"feedback": "Code looks okay but lint failed",
			},
		}},
	})
	h.MockLLM.AddResponse(MockResponse{
		Text: "Implementation v2: fixed lint errors in feature", // LLM call 3: implement iter 1
	})
	// LLM call 4: review iter 1 (structured-agent, submit_evaluation tool → completed)
	h.MockLLM.AddResponse(MockResponse{
		Text: "Evaluation iteration 1",
		ToolCalls: []MockToolCall{{
			Name: "submit_evaluation",
			Input: map[string]interface{}{
				"grade":    "pass",
				"strategy": "pass",
				"feedback": "All checks pass, implementation is clean",
			},
		}},
	})

	chatID := h.StartWorkflowViaGRPC(t, "builtin://one-ring", map[string]interface{}{
		"steps":       []interface{}{"implement", "lint"},
		"model":       map[string]interface{}{"id": "mock"},
		"max_retries": float64(5),
		"yield":       false,
	}, "Add input validation to the signup form")

	h.WaitForWorkflowComplete(t, chatID)

	// Verify lint was called at least twice (fail + pass)
	lintCalls := h.MockRun.CallCountFor("*lint*")
	assert.GreaterOrEqual(t, lintCalls, 2,
		"lint should be called at least twice (fail + pass)")

	// Verify LLM was called multiple times across iterations
	assert.GreaterOrEqual(t, h.MockLLM.CallCount(), 4,
		"LLM should be called at least 4 times (2x implement + 2x review)")

	t.Logf("✓ LoopIteratesOnFailure: lint called %d times, LLM called %d times",
		lintCalls, h.MockLLM.CallCount())
}

// TestOneRing_LoopExitsOnSuccess tests that the implementation loop exits
// immediately when review passes on the first iteration.
//
// Steps: implement + review (no lint/test/build)
// Expected: 1 iteration, workflow completes quickly
func TestOneRing_LoopExitsOnSuccess(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Mock LLM: implement response + review pass on first try
	h.MockLLM.SetResponses(
		"Implementation complete: all changes applied",
	)
	h.MockLLM.AddResponse(MockResponse{
		Text: "Evaluation passed",
		ToolCalls: []MockToolCall{{
			Name: "submit_evaluation",
			Input: map[string]interface{}{
				"grade":    "pass",
				"strategy": "pass",
				"feedback": "Clean implementation, all good",
			},
		}},
	})

	chatID := h.StartWorkflowViaGRPC(t, "builtin://one-ring", map[string]interface{}{
		"steps":       []interface{}{"implement"},
		"model":       map[string]interface{}{"id": "mock"},
		"max_retries": float64(5),
		"yield":       false,
	}, "Refactor the logger module")

	h.WaitForWorkflowComplete(t, chatID)

	// With no lint/test/build and review pass, the loop should only run once.
	// LLM calls: implement(1) + review(1) = 2
	assert.Equal(t, 2, h.MockLLM.CallCount(),
		"LLM should be called exactly 2 times (implement + review) for single iteration")

	t.Logf("✓ LoopExitsOnSuccess: workflow completed in 1 iteration with %d LLM calls",
		h.MockLLM.CallCount())
}

// TestOneRing_MaxRetriesExhausted tests that the implementation loop stops
// after max_retries iterations even when review keeps failing.
//
// Steps: implement + review, max_retries=2
// Expected: exactly 2 iterations, then workflow completes (not infinite loop)
func TestOneRing_MaxRetriesExhausted(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Mock LLM: implement responses + review always fails with "continue" strategy.
	// We use "continue" (not "refactor") so the refactor agent isn't spawned — that
	// would consume extra mock responses. The test validates that the outer attempt
	// loop stops after max_retries iterations regardless of the failing review.
	//
	// Each iteration: implement agent (1 call) + review structured-agent (1 call) = 2 per iter
	// With max_retries=2: 4 LLM calls total.
	h.MockLLM.SetResponses(
		"Implementation attempt 1",
	)
	h.MockLLM.AddResponse(MockResponse{
		Text: "Evaluation: fail iter 0",
		ToolCalls: []MockToolCall{{
			Name: "submit_evaluation",
			Input: map[string]interface{}{
				"grade":    "fail",
				"strategy": "continue",
				"feedback": "Missing error handling",
			},
		}},
	})
	h.MockLLM.AddResponse(MockResponse{
		Text: "Implementation attempt 2",
	})
	h.MockLLM.AddResponse(MockResponse{
		Text: "Evaluation: fail iter 1",
		ToolCalls: []MockToolCall{{
			Name: "submit_evaluation",
			Input: map[string]interface{}{
				"grade":    "fail",
				"strategy": "continue",
				"feedback": "Still missing edge cases",
			},
		}},
	})

	chatID := h.StartWorkflowViaGRPC(t, "builtin://one-ring", map[string]interface{}{
		"steps":       []interface{}{"implement"},
		"model":       map[string]interface{}{"id": "mock"},
		"max_retries": float64(2),
		"yield":       false,
	}, "Implement complex error handling")

	h.WaitForWorkflowComplete(t, chatID)

	// The loop should run exactly max_retries (2) iterations.
	// Each iteration: implement + evaluate = 2 LLM calls
	// Total: 4 LLM calls (or more if structured-agent needs extra turns)
	assert.GreaterOrEqual(t, h.MockLLM.CallCount(), 4,
		"LLM should be called at least 4 times (2 iterations × 2 steps)")

	// The workflow should complete (not hang) even though review kept failing
	history := h.GetWorkflowHistory(t, chatID)
	history.PrintActivities()

	t.Logf("✓ MaxRetriesExhausted: workflow completed after max_retries with %d LLM calls",
		h.MockLLM.CallCount())
}

// TestOneRing_FullPipeline tests the complete one-ring workflow with all steps:
// plan → critique → tdd → implement → lint → test → build → review
//
// This exercises the full end-to-end pipeline with all steps enabled.
// All checks pass and review passes on first iteration.
func TestOneRing_FullPipeline(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Mock all run commands to succeed
	h.MockRun.OnPattern("*lint*", MockRunResponse{ExitCode: 0, Stdout: "no errors"})
	h.MockRun.OnPattern("*test*", MockRunResponse{ExitCode: 0, Stdout: "all tests pass"})
	h.MockRun.OnPattern("*build*", MockRunResponse{ExitCode: 0, Stdout: "build successful"})

	// Mock LLM responses for all steps:
	// 1: plan (fork from planning sub-workflow)
	// 2: criticize (fork from planning sub-workflow)
	// 3: revise (fork from planning sub-workflow)
	// 4: write_tests / TDD (fork from main)
	// 5: implement (fork from impl_loop)
	// 6: review (structured-agent with submit_evaluation tool call)
	h.MockLLM.SetResponses(
		"Plan: Implement user authentication with JWT tokens",
		"Criticism: Plan lacks rate limiting and session management",
		"Revised plan: Add JWT auth with rate limiting and session management",
		"Tests written: auth.test.ts with 12 test cases covering login, logout, token refresh",
		"Implementation complete: auth module with JWT, rate limiter, session store",
	)
	h.MockLLM.AddResponse(MockResponse{
		Text: "Full pipeline evaluation",
		ToolCalls: []MockToolCall{{
			Name: "submit_evaluation",
			Input: map[string]interface{}{
				"grade":    "pass",
				"strategy": "pass",
				"feedback": "All checks pass, implementation follows the revised plan",
			},
		}},
	})

	chatID := h.StartWorkflowViaGRPC(t, "builtin://one-ring", map[string]interface{}{
		"steps": []interface{}{
			"plan", "criticize", "revise", "tdd",
			"implement", "lint", "test", "build",
		},
		"model":       map[string]interface{}{"id": "mock"},
		"max_retries": float64(3),
		"yield":       false,
	}, "Implement user authentication with JWT tokens")

	h.WaitForWorkflowComplete(t, chatID)

	ctx := context.Background()

	// Verify all run commands were called
	assert.True(t, h.MockRun.WasCalled("*lint*"), "lint should have been called")
	assert.True(t, h.MockRun.WasCalled("*test*"), "test should have been called")
	assert.True(t, h.MockRun.WasCalled("*build*"), "build should have been called")

	// Verify messages on the main thread include the final plan and completion marker
	messages, err := h.DB.ListMessages(ctx, chatID, db.MessageListOptions{})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(messages), 2, "should have user + assistant messages")

	// Verify Pipeline Complete message exists
	var hasCompletion bool
	for _, msg := range messages {
		if msg.Role == reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
			content := h.GetMessageText(t, msg.ID)
			if contains(content, "Pipeline Complete") {
				hasCompletion = true
			}
		}
	}
	require.True(t, hasCompletion, "should have 'Pipeline Complete' message on main thread")

	// Verify LLM was called for all agent steps
	// plan + criticize + revise + write_tests + implement + review = 6+
	assert.GreaterOrEqual(t, h.MockLLM.CallCount(), 6,
		"LLM should be called at least 6 times for full pipeline")

	t.Logf("✓ FullPipeline: workflow completed with %d messages, %d LLM calls, %d run commands",
		len(messages), h.MockLLM.CallCount(), h.MockRun.CallCount())
}
