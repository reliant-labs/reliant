// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	rtemporal "github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/workflow/core"
)

// ============================================================================
// Delta identity: finalize-invariant tests
// ============================================================================
// The core invariant: every pre-allocated assistant message id is eventually
// finalized with exactly one stream_finalized marker. These tests drive the
// real StepExecutor inside a Temporal test env with stubbed activities and
// capture the EmitStreamFinalized calls.

type StreamFinalizedSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
}

func TestStreamFinalized(t *testing.T) {
	suite.Run(t, new(StreamFinalizedSuite))
}

// capturedFinalize records one EmitStreamFinalized activity invocation.
type capturedFinalize struct {
	ChatID        string
	MessageID     string
	Thread        string
	Reason        string
	LastStreamSeq int64
}

// registerFinalizeCapture registers a stub EmitStreamFinalized that appends
// every call to the returned slice, and installs the production data
// converter so proto activity outputs decode into maps exactly like prod
// (getRawOutput decodes CallLLMOutput via the flexible converter).
func registerFinalizeCapture(env *testsuite.TestWorkflowEnvironment) *[]capturedFinalize {
	env.SetDataConverter(rtemporal.NewFlexibleDataConverter())
	var captured []capturedFinalize
	env.RegisterActivityWithOptions(
		func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			cf := capturedFinalize{}
			cf.ChatID, _ = input["chat_id"].(string)
			cf.MessageID, _ = input["message_id"].(string)
			cf.Thread, _ = input["thread"].(string)
			cf.Reason, _ = input["reason"].(string)
			if v, ok := input["last_stream_seq"].(float64); ok {
				cf.LastStreamSeq = int64(v)
			}
			captured = append(captured, cf)
			return map[string]interface{}{"success": true}, nil
		},
		activity.RegisterOptions{Name: "EmitStreamFinalized"},
	)
	return &captured
}

// callLLMTestNode builds a minimal call_llm proto node.
func callLLMTestNode(id string) *reliantv1.Node {
	return &reliantv1.Node{
		Id:   id,
		Type: "call_llm",
		Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{}},
	}
}

// stepExecutorStreamWorkflow runs one call_llm step through the real
// StepExecutor (Start + HandleCompletion) and returns the finalize-relevant
// facts as a map for assertions.
func stepExecutorStreamWorkflow(ctx workflow.Context) (map[string]interface{}, error) {
	ctx = WithStreamIDTracker(ctx, NewStreamIDTracker())
	workflowID := workflow.GetInfo(ctx).WorkflowExecution.ID

	executor := NewStepExecutor(
		ctx, workflowID, "chat-1", "test-workflow",
		map[string]interface{}{}, map[string]interface{}{},
		&ChildWorkflowTracker{children: make(map[string]bool)},
	).WithExecContext(&ExecutionContext{
		WorkflowID: workflowID,
		ChatID:     "chat-1",
		Thread:     "thread-1",
	})

	node := callLLMTestNode("llm-step")
	running := executor.Start(&core.TriggeredNode{
		Node: node,
		Event: &core.WorkflowEvent{
			ID:         "evt-1",
			WorkflowID: workflowID,
			ChatID:     "chat-1",
		},
	})

	stepEvent := executor.HandleCompletion(running)

	result := map[string]interface{}{
		"preallocated_id": running.PreallocatedMessageID,
		"had_error":       stepEvent.Error != nil,
		"retry_exhausted": stepEvent.RetryExhausted,
	}
	if tracker := streamIDTrackerFrom(ctx); tracker != nil {
		result["outstanding"] = tracker.OutstandingIDs()
	}
	return result, nil
}

// successCallLLMStub returns a CallLLM stub whose output echoes the
// pre-allocated id from the ActivityInput envelope (as the real activity does).
func successCallLLMStub(t *testing.T) func(context.Context, json.RawMessage) (*reliantv1.CallLLMOutput, error) {
	return func(_ context.Context, input json.RawMessage) (*reliantv1.CallLLMOutput, error) {
		var envelope struct {
			Runtime struct {
				AssistantMessageID string `json:"assistant_message_id"`
			} `json:"runtime"`
		}
		require.NoError(t, json.Unmarshal(input, &envelope))
		return &reliantv1.CallLLMOutput{
			ResponseText:  "hello",
			MessageId:     envelope.Runtime.AssistantMessageID,
			LastStreamSeq: 7,
		}, nil
	}
}

func (s *StreamFinalizedSuite) TestSuccess_ExactlyOneCompletedMarker() {
	env := s.NewTestWorkflowEnvironment()
	captured := registerFinalizeCapture(env)
	env.RegisterActivityWithOptions(successCallLLMStub(s.T()), activity.RegisterOptions{Name: "CallLLM"})

	env.ExecuteWorkflow(stepExecutorStreamWorkflow)

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError())

	var result map[string]interface{}
	s.NoError(env.GetWorkflowResult(&result))

	preallocatedID, _ := result["preallocated_id"].(string)
	s.NotEmpty(preallocatedID, "call_llm step must pre-allocate a message id")

	s.Require().Len(*captured, 1, "exactly one finalize marker on success")
	marker := (*captured)[0]
	s.Equal(preallocatedID, marker.MessageID)
	s.Equal("completed", marker.Reason)
	s.Equal("chat-1", marker.ChatID)
	s.Equal("thread-1", marker.Thread)
	s.Equal(int64(7), marker.LastStreamSeq, "last_stream_seq carried from CallLLMOutput")

	outstanding, _ := result["outstanding"].([]interface{})
	s.Empty(outstanding, "no outstanding ids after finalize")
}

func (s *StreamFinalizedSuite) TestRetryExhausted_AbortedMarkerBeforePause() {
	env := s.NewTestWorkflowEnvironment()
	captured := registerFinalizeCapture(env)
	env.RegisterActivityWithOptions(
		func(context.Context, json.RawMessage) (*reliantv1.CallLLMOutput, error) {
			return nil, temporal.NewApplicationError("429 Too Many Requests", "RateLimitError")
		},
		activity.RegisterOptions{Name: "CallLLM"},
	)

	env.ExecuteWorkflow(stepExecutorStreamWorkflow)

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError())

	var result map[string]interface{}
	s.NoError(env.GetWorkflowResult(&result))
	s.Equal(true, result["retry_exhausted"], "step should report retry exhaustion")

	preallocatedID, _ := result["preallocated_id"].(string)
	s.NotEmpty(preallocatedID)

	// HandleCompletion emits the aborted marker BEFORE the caller pauses:
	// the workflow above returns right after HandleCompletion, so the marker
	// being captured proves it fired inside HandleCompletion.
	s.Require().Len(*captured, 1, "exactly one finalize marker on retry exhaustion")
	marker := (*captured)[0]
	s.Equal(preallocatedID, marker.MessageID)
	s.Equal("aborted", marker.Reason)

	outstanding, _ := result["outstanding"].([]interface{})
	s.Empty(outstanding)
}

// cancelStreamWorkflow starts a call_llm step, then blocks so the test can
// cancel the workflow mid-stream. Completion cleanup must emit the aborted
// marker for the outstanding id on a disconnected context.
func cancelStreamWorkflow(ctx workflow.Context) (err error) {
	ctx = WithStreamIDTracker(ctx, NewStreamIDTracker())
	workflowID := workflow.GetInfo(ctx).WorkflowExecution.ID

	defer func() {
		if ctx.Err() != nil {
			finalizeOutstandingStreams(ctx, streamReasonAborted)
		}
	}()

	executor := NewStepExecutor(
		ctx, workflowID, "chat-1", "test-workflow",
		map[string]interface{}{}, map[string]interface{}{},
		&ChildWorkflowTracker{children: make(map[string]bool)},
	).WithExecContext(&ExecutionContext{
		WorkflowID: workflowID,
		ChatID:     "chat-1",
		Thread:     "thread-1",
	})

	running := executor.Start(&core.TriggeredNode{
		Node:  callLLMTestNode("llm-step"),
		Event: &core.WorkflowEvent{ID: "evt-1", WorkflowID: workflowID, ChatID: "chat-1"},
	})

	// Block on the activity future; cancellation interrupts this.
	var out map[string]interface{}
	if err := running.Future.Get(ctx, &out); err != nil {
		return err
	}
	return nil
}

func (s *StreamFinalizedSuite) TestCancelMidStream_AbortedMarkerOnDisconnectedCtx() {
	env := s.NewTestWorkflowEnvironment()
	captured := registerFinalizeCapture(env)
	env.RegisterActivityWithOptions(
		func(ctx context.Context, _ json.RawMessage) (*reliantv1.CallLLMOutput, error) {
			// Simulate a long-running stream that only ends via cancellation.
			<-ctx.Done()
			return nil, ctx.Err()
		},
		activity.RegisterOptions{Name: "CallLLM"},
	)

	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 10*time.Millisecond)

	env.ExecuteWorkflow(cancelStreamWorkflow)

	s.True(env.IsWorkflowCompleted())

	s.Require().Len(*captured, 1, "cancelled stream must still get exactly one finalize marker")
	marker := (*captured)[0]
	s.NotEmpty(marker.MessageID)
	s.Equal("aborted", marker.Reason)
	s.Equal("chat-1", marker.ChatID)
	s.Equal("thread-1", marker.Thread)
}

// saveFailStreamWorkflow: CallLLM succeeds but the node's inline SaveMessage
// fails. The completed marker must still fire (the F5 coverage: marker after
// the save ATTEMPT, not gated on save success).
func saveFailStreamWorkflow(ctx workflow.Context) (map[string]interface{}, error) {
	ctx = WithStreamIDTracker(ctx, NewStreamIDTracker())
	workflowID := workflow.GetInfo(ctx).WorkflowExecution.ID

	executor := NewStepExecutor(
		ctx, workflowID, "chat-1", "test-workflow",
		map[string]interface{}{}, map[string]interface{}{},
		&ChildWorkflowTracker{children: make(map[string]bool)},
	).WithExecContext(&ExecutionContext{
		WorkflowID: workflowID,
		ChatID:     "chat-1",
		Thread:     "thread-1",
	})

	node := callLLMTestNode("llm-step")
	node.SaveMessage = &reliantv1.SaveMessageConfig{
		Role:    &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "assistant"}},
		Content: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "hello"}},
	}

	running := executor.Start(&core.TriggeredNode{
		Node:  node,
		Event: &core.WorkflowEvent{ID: "evt-1", WorkflowID: workflowID, ChatID: "chat-1"},
	})

	stepEvent := executor.HandleCompletion(running)
	return map[string]interface{}{
		"preallocated_id": running.PreallocatedMessageID,
		"had_error":       stepEvent.Error != nil,
	}, nil
}

func (s *StreamFinalizedSuite) TestSaveMessageFailure_MarkerStillFires() {
	env := s.NewTestWorkflowEnvironment()
	captured := registerFinalizeCapture(env)
	env.RegisterActivityWithOptions(successCallLLMStub(s.T()), activity.RegisterOptions{Name: "CallLLM"})
	env.RegisterActivityWithOptions(
		func(context.Context, json.RawMessage) (*reliantv1.SaveMessageOutput, error) {
			return nil, temporal.NewNonRetryableApplicationError("db write failed", "SaveError", fmt.Errorf("db write failed"))
		},
		activity.RegisterOptions{Name: "SaveMessage"},
	)

	env.ExecuteWorkflow(saveFailStreamWorkflow)

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError())

	var result map[string]interface{}
	s.NoError(env.GetWorkflowResult(&result))
	s.Equal(false, result["had_error"], "save_message failure must not fail the step")

	preallocatedID, _ := result["preallocated_id"].(string)
	s.NotEmpty(preallocatedID)

	s.Require().Len(*captured, 1, "marker fires even when SaveMessage failed")
	s.Equal("completed", (*captured)[0].Reason)
	s.Equal(preallocatedID, (*captured)[0].MessageID)
}

// TestStreamIDTracker pins the tracker's bookkeeping.
func TestStreamIDTracker(t *testing.T) {
	tr := NewStreamIDTracker()
	tr.Register("b", "chat-1", "t1")
	tr.Register("a", "chat-1", "t1")
	tr.Register("", "chat-1", "t1") // empty id ignored

	assert.Equal(t, []string{"a", "b"}, tr.OutstandingIDs(), "sorted for deterministic replay")

	tr.Resolve("a")
	assert.Equal(t, []string{"b"}, tr.OutstandingIDs())

	// Nil-safety
	var nilTracker *StreamIDTracker
	nilTracker.Register("x", "c", "t")
	nilTracker.Resolve("x")
	assert.Nil(t, nilTracker.OutstandingIDs())
}
