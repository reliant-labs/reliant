// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/history/v1"
)

// ============================================================================
// WORKFLOW HISTORY ANALYSIS
//
// These helpers extract real data from Temporal workflow history for assertions.
// This tests the actual data flow through the system, not mocked activity returns.
//
// Key insight: Temporal history contains the exact inputs/outputs that activities
// received and returned. This is the source of truth for "did data flow correctly?"
// ============================================================================

// WorkflowHistory provides methods to analyze a completed workflow's execution
type WorkflowHistory struct {
	t          *testing.T
	events     []*history.HistoryEvent
	activities []ActivityExecution
}

// ActivityExecution represents a single activity execution with its real inputs/outputs
type ActivityExecution struct {
	// Activity identification
	ActivityType string
	ActivityID   string
	EventID      int64

	// Scheduling info
	ScheduledInput json.RawMessage

	// Completion info (only if completed successfully)
	Completed       bool
	CompletedOutput json.RawMessage

	// Failure info (only if failed)
	Failed         bool
	FailureMessage string
}

// GetWorkflowHistory retrieves and parses the complete history for a workflow
// including all child workflow histories (recursive)
func (h *TestHarness) GetWorkflowHistory(t *testing.T, workflowID string) *WorkflowHistory {
	t.Helper()
	return h.getWorkflowHistoryRecursive(t, workflowID, make(map[string]bool))
}

// getWorkflowHistoryRecursive retrieves history for a workflow and all its children
func (h *TestHarness) getWorkflowHistoryRecursive(t *testing.T, workflowID string, visited map[string]bool) *WorkflowHistory {
	t.Helper()

	// Avoid infinite recursion
	if visited[workflowID] {
		return &WorkflowHistory{t: t}
	}
	visited[workflowID] = true

	ctx := context.Background()

	// Get all history events (not long poll, we want completed history)
	iter := h.TemporalClient.GetWorkflowHistory(
		ctx,
		workflowID,
		"",    // empty runID = latest run
		false, // isLongPoll
		enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT,
	)

	var events []*history.HistoryEvent
	for iter.HasNext() {
		event, err := iter.Next()
		require.NoError(t, err, "failed to read history event")
		events = append(events, event)
	}

	wh := &WorkflowHistory{
		t:      t,
		events: events,
	}
	wh.parseActivities()

	// Find and recursively collect child workflow histories
	children := wh.parseChildWorkflows()
	for _, child := range children {
		childHistory := h.getWorkflowHistoryRecursive(t, child.WorkflowID, visited)
		// Merge child activities into parent
		wh.activities = append(wh.activities, childHistory.activities...)
	}

	return wh
}

// ChildWorkflowExecution represents a child workflow that was started
type ChildWorkflowExecution struct {
	WorkflowID   string
	RunID        string
	WorkflowType string
}

// parseActivities extracts activity executions from raw history events
func (wh *WorkflowHistory) parseActivities() {
	// Map to correlate scheduled → completed/failed events
	scheduledByEventID := make(map[int64]*ActivityExecution)

	for _, event := range wh.events {
		switch event.EventType {
		case enums.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED:
			attrs := event.GetActivityTaskScheduledEventAttributes()
			exec := &ActivityExecution{
				ActivityType: attrs.GetActivityType().GetName(),
				ActivityID:   attrs.GetActivityId(),
				EventID:      event.EventId,
			}
			if attrs.GetInput() != nil && len(attrs.GetInput().GetPayloads()) > 0 {
				exec.ScheduledInput = attrs.GetInput().GetPayloads()[0].GetData()
			}
			scheduledByEventID[event.EventId] = exec
			wh.activities = append(wh.activities, *exec)

		case enums.EVENT_TYPE_ACTIVITY_TASK_COMPLETED:
			attrs := event.GetActivityTaskCompletedEventAttributes()
			schedID := attrs.GetScheduledEventId()
			if exec, ok := scheduledByEventID[schedID]; ok {
				exec.Completed = true
				if attrs.GetResult() != nil && len(attrs.GetResult().GetPayloads()) > 0 {
					exec.CompletedOutput = attrs.GetResult().GetPayloads()[0].GetData()
				}
				// Update in slice
				for i := range wh.activities {
					if wh.activities[i].EventID == schedID {
						wh.activities[i] = *exec
						break
					}
				}
			}

		case enums.EVENT_TYPE_ACTIVITY_TASK_FAILED:
			attrs := event.GetActivityTaskFailedEventAttributes()
			schedID := attrs.GetScheduledEventId()
			if exec, ok := scheduledByEventID[schedID]; ok {
				exec.Failed = true
				if attrs.GetFailure() != nil {
					exec.FailureMessage = attrs.GetFailure().GetMessage()
				}
				// Update in slice
				for i := range wh.activities {
					if wh.activities[i].EventID == schedID {
						wh.activities[i] = *exec
						break
					}
				}
			}
		}
	}
}

// parseChildWorkflows extracts child workflow IDs from history
func (wh *WorkflowHistory) parseChildWorkflows() []ChildWorkflowExecution {
	var children []ChildWorkflowExecution

	for _, event := range wh.events {
		if event.EventType == enums.EVENT_TYPE_CHILD_WORKFLOW_EXECUTION_STARTED {
			attrs := event.GetChildWorkflowExecutionStartedEventAttributes()
			children = append(children, ChildWorkflowExecution{
				WorkflowID:   attrs.GetWorkflowExecution().GetWorkflowId(),
				RunID:        attrs.GetWorkflowExecution().GetRunId(),
				WorkflowType: attrs.GetWorkflowType().GetName(),
			})
		}
	}
	return children
}

// ============================================================================
// ACTIVITY QUERY METHODS
// ============================================================================

// GetActivities returns all activity executions in order
func (wh *WorkflowHistory) GetActivities() []ActivityExecution {
	return wh.activities
}

// GetActivitiesOfType returns all executions of a specific activity type
func (wh *WorkflowHistory) GetActivitiesOfType(activityType string) []ActivityExecution {
	var result []ActivityExecution
	for _, act := range wh.activities {
		if act.ActivityType == activityType {
			result = append(result, act)
		}
	}
	return result
}

// GetFirstActivity returns the first execution of an activity type, or nil
func (wh *WorkflowHistory) GetFirstActivity(activityType string) *ActivityExecution {
	acts := wh.GetActivitiesOfType(activityType)
	if len(acts) == 0 {
		return nil
	}
	return &acts[0]
}

// GetNthActivity returns the nth execution of an activity type (0-indexed), or nil
func (wh *WorkflowHistory) GetNthActivity(activityType string, n int) *ActivityExecution {
	acts := wh.GetActivitiesOfType(activityType)
	if n < 0 || n >= len(acts) {
		return nil
	}
	return &acts[n]
}

// ============================================================================
// INPUT/OUTPUT PARSING
// ============================================================================

// ParseInput parses the activity's scheduled input into a map
func (a *ActivityExecution) ParseInput() (map[string]interface{}, error) {
	if a.ScheduledInput == nil {
		return nil, nil
	}
	var result map[string]interface{}
	if err := json.Unmarshal(a.ScheduledInput, &result); err != nil {
		return nil, fmt.Errorf("failed to parse activity input: %w", err)
	}
	return result, nil
}

// ParseOutput parses the activity's completed output into a map
func (a *ActivityExecution) ParseOutput() (map[string]interface{}, error) {
	if a.CompletedOutput == nil {
		return nil, nil
	}
	var result map[string]interface{}
	if err := json.Unmarshal(a.CompletedOutput, &result); err != nil {
		return nil, fmt.Errorf("failed to parse activity output: %w", err)
	}
	return result, nil
}

// MustParseInput is like ParseInput but fails the test on error
func (a *ActivityExecution) MustParseInput(t *testing.T) map[string]interface{} {
	t.Helper()
	result, err := a.ParseInput()
	require.NoError(t, err)
	return result
}

// MustParseOutput is like ParseOutput but fails the test on error
func (a *ActivityExecution) MustParseOutput(t *testing.T) map[string]interface{} {
	t.Helper()
	result, err := a.ParseOutput()
	require.NoError(t, err)
	return result
}

// ============================================================================
// ASSERTIONS
// ============================================================================

// AssertActivityExecuted asserts that an activity type was executed at least once
func (wh *WorkflowHistory) AssertActivityExecuted(activityType string) *WorkflowHistory {
	acts := wh.GetActivitiesOfType(activityType)
	require.NotEmpty(wh.t, acts, "activity %q was not executed", activityType)
	return wh
}

// AssertActivityNotExecuted asserts that an activity type was never executed
func (wh *WorkflowHistory) AssertActivityNotExecuted(activityType string) *WorkflowHistory {
	acts := wh.GetActivitiesOfType(activityType)
	require.Empty(wh.t, acts, "activity %q was executed but should not have been", activityType)
	return wh
}

// AssertActivityCount asserts exact number of executions of an activity type
func (wh *WorkflowHistory) AssertActivityCount(activityType string, expected int) *WorkflowHistory {
	acts := wh.GetActivitiesOfType(activityType)
	require.Len(wh.t, acts, expected, "activity %q execution count mismatch", activityType)
	return wh
}

// AssertActivitySucceeded asserts that an activity completed successfully
func (wh *WorkflowHistory) AssertActivitySucceeded(activityType string) *WorkflowHistory {
	act := wh.GetFirstActivity(activityType)
	require.NotNil(wh.t, act, "activity %q was not executed", activityType)
	require.True(wh.t, act.Completed, "activity %q did not complete successfully", activityType)
	require.False(wh.t, act.Failed, "activity %q failed: %s", activityType, act.FailureMessage)
	return wh
}

// AssertActivityFailed asserts that an activity failed
func (wh *WorkflowHistory) AssertActivityFailed(activityType string) *WorkflowHistory {
	act := wh.GetFirstActivity(activityType)
	require.NotNil(wh.t, act, "activity %q was not executed", activityType)
	require.True(wh.t, act.Failed, "activity %q did not fail as expected", activityType)
	return wh
}

// AssertActivityFailedWithMessage asserts activity failed with specific message substring
func (wh *WorkflowHistory) AssertActivityFailedWithMessage(activityType, messageContains string) *WorkflowHistory {
	act := wh.GetFirstActivity(activityType)
	require.NotNil(wh.t, act, "activity %q was not executed", activityType)
	require.True(wh.t, act.Failed, "activity %q did not fail as expected", activityType)
	require.Contains(wh.t, act.FailureMessage, messageContains,
		"activity %q failure message does not contain expected string", activityType)
	return wh
}

// AssertActivitySequence asserts activities were executed in order
// Filters out infrastructure activities (LoadWorkflow, WorkflowStatus, Cleanup)
func (wh *WorkflowHistory) AssertActivitySequence(expectedTypes ...string) *WorkflowHistory {
	infraActivities := map[string]bool{
		"ActivityLoadWorkflow": true,
		"WorkflowStatus":       true,
		"Cleanup":              true,
		"FailStep":             true,
	}

	var businessActivities []string
	for _, act := range wh.activities {
		if !infraActivities[act.ActivityType] {
			businessActivities = append(businessActivities, act.ActivityType)
		}
	}

	require.Equal(wh.t, expectedTypes, businessActivities,
		"activity sequence mismatch (infrastructure activities excluded)")
	return wh
}

// AssertNoActivityFailures asserts that no activities failed
func (wh *WorkflowHistory) AssertNoActivityFailures() *WorkflowHistory {
	for _, act := range wh.activities {
		require.False(wh.t, act.Failed, "activity %q failed: %s", act.ActivityType, act.FailureMessage)
	}
	return wh
}

// ============================================================================
// INPUT ASSERTIONS
// ============================================================================

// AssertActivityInputContains asserts that an activity's input contains a specific key/value
func (wh *WorkflowHistory) AssertActivityInputContains(activityType, key string, expectedValue interface{}) *WorkflowHistory {
	act := wh.GetFirstActivity(activityType)
	require.NotNil(wh.t, act, "activity %q was not executed", activityType)

	input := act.MustParseInput(wh.t)
	require.Contains(wh.t, input, key, "activity %q input missing key %q", activityType, key)
	require.Equal(wh.t, expectedValue, input[key],
		"activity %q input[%q] value mismatch", activityType, key)
	return wh
}

// AssertActivityInputHasKey asserts that an activity's input contains a specific key
func (wh *WorkflowHistory) AssertActivityInputHasKey(activityType, key string) *WorkflowHistory {
	act := wh.GetFirstActivity(activityType)
	require.NotNil(wh.t, act, "activity %q was not executed", activityType)

	input := act.MustParseInput(wh.t)
	require.Contains(wh.t, input, key, "activity %q input missing key %q", activityType, key)
	return wh
}

// AssertActivityOutputContains asserts that an activity's output contains a specific key/value
func (wh *WorkflowHistory) AssertActivityOutputContains(activityType, key string, expectedValue interface{}) *WorkflowHistory {
	act := wh.GetFirstActivity(activityType)
	require.NotNil(wh.t, act, "activity %q was not executed", activityType)
	require.True(wh.t, act.Completed, "activity %q did not complete", activityType)

	output := act.MustParseOutput(wh.t)
	require.Contains(wh.t, output, key, "activity %q output missing key %q", activityType, key)
	require.Equal(wh.t, expectedValue, output[key],
		"activity %q output[%q] value mismatch", activityType, key)
	return wh
}

// ============================================================================
// DATA FLOW ASSERTIONS
//
// These are the critical tests: verify that data actually flows between steps
// via edge bindings. This is what catches the V3 binding bugs.
// ============================================================================

// AssertDataFlowed asserts that output from sourceActivity was received as input by targetActivity
// This verifies edge bindings actually work.
func (wh *WorkflowHistory) AssertDataFlowed(sourceActivity, outputKey, targetActivity, inputKey string) *WorkflowHistory {
	// Get source output
	src := wh.GetFirstActivity(sourceActivity)
	require.NotNil(wh.t, src, "source activity %q was not executed", sourceActivity)
	require.True(wh.t, src.Completed, "source activity %q did not complete", sourceActivity)

	srcOutput := src.MustParseOutput(wh.t)
	require.Contains(wh.t, srcOutput, outputKey,
		"source activity %q output missing key %q", sourceActivity, outputKey)

	// Get target input
	tgt := wh.GetFirstActivity(targetActivity)
	require.NotNil(wh.t, tgt, "target activity %q was not executed", targetActivity)

	tgtInput := tgt.MustParseInput(wh.t)
	require.Contains(wh.t, tgtInput, inputKey,
		"target activity %q input missing key %q", targetActivity, inputKey)

	// Compare values
	require.Equal(wh.t, srcOutput[outputKey], tgtInput[inputKey],
		"data did not flow from %s.%s to %s.%s", sourceActivity, outputKey, targetActivity, inputKey)

	return wh
}

// AssertNthDataFlowed is like AssertDataFlowed but for specific occurrences (0-indexed)
func (wh *WorkflowHistory) AssertNthDataFlowed(
	sourceActivity string, sourceN int, outputKey string,
	targetActivity string, targetN int, inputKey string,
) *WorkflowHistory {
	src := wh.GetNthActivity(sourceActivity, sourceN)
	require.NotNil(wh.t, src, "source activity %q (occurrence %d) was not executed", sourceActivity, sourceN)
	require.True(wh.t, src.Completed, "source activity %q (occurrence %d) did not complete", sourceActivity, sourceN)

	srcOutput := src.MustParseOutput(wh.t)
	require.Contains(wh.t, srcOutput, outputKey,
		"source activity %q (occurrence %d) output missing key %q", sourceActivity, sourceN, outputKey)

	tgt := wh.GetNthActivity(targetActivity, targetN)
	require.NotNil(wh.t, tgt, "target activity %q (occurrence %d) was not executed", targetActivity, targetN)

	tgtInput := tgt.MustParseInput(wh.t)
	require.Contains(wh.t, tgtInput, inputKey,
		"target activity %q (occurrence %d) input missing key %q", targetActivity, targetN, inputKey)

	require.Equal(wh.t, srcOutput[outputKey], tgtInput[inputKey],
		"data did not flow from %s[%d].%s to %s[%d].%s",
		sourceActivity, sourceN, outputKey, targetActivity, targetN, inputKey)

	return wh
}

// ============================================================================
// DEBUGGING HELPERS
// ============================================================================

// PrintActivities prints all activities for debugging
func (wh *WorkflowHistory) PrintActivities() *WorkflowHistory {
	fmt.Printf("\n=== Workflow Activity History (%d activities) ===\n", len(wh.activities))
	for i, act := range wh.activities {
		status := "scheduled"
		if act.Completed {
			status = "completed"
		} else if act.Failed {
			status = fmt.Sprintf("failed: %s", act.FailureMessage)
		}
		fmt.Printf("[%d] %s (ID: %s) - %s\n", i, act.ActivityType, act.ActivityID, status)

		if input, err := act.ParseInput(); err == nil && input != nil {
			inputJSON, _ := json.MarshalIndent(input, "     ", "  ")
			fmt.Printf("     Input: %s\n", string(inputJSON))
		}

		if act.Completed {
			if output, err := act.ParseOutput(); err == nil && output != nil {
				outputJSON, _ := json.MarshalIndent(output, "     ", "  ")
				fmt.Printf("     Output: %s\n", string(outputJSON))
			}
		}
	}
	fmt.Println("================================================")
	return wh
}

// GetActivityInputJSON returns the raw JSON input for debugging
func (wh *WorkflowHistory) GetActivityInputJSON(activityType string) string {
	act := wh.GetFirstActivity(activityType)
	if act == nil {
		return "<activity not found>"
	}
	return string(act.ScheduledInput)
}

// GetActivityOutputJSON returns the raw JSON output for debugging
func (wh *WorkflowHistory) GetActivityOutputJSON(activityType string) string {
	act := wh.GetFirstActivity(activityType)
	if act == nil {
		return "<activity not found>"
	}
	if !act.Completed {
		return "<activity did not complete>"
	}
	return string(act.CompletedOutput)
}
