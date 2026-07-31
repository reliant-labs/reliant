// Copyright (c) 2025 Reliant Labs
package execfollow

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// These tests pin the two lies a supervision stream told about the measured run
// e10cabae: a `run` node that exited non-zero printed "✓ node build completed",
// and a workflow that routed to its `failed` terminal printed
// "✓ workflow completed" and exited 0.

// runNodeUpdate builds the node_execution row the runtime actually writes for a
// `run` step: event_type COMPLETED (the activity succeeded) carrying the
// command's exit code (the command did not).
func runNodeUpdate(seq int64, nodeID string, exitCode int) RawUpdate {
	data := fmt.Sprintf(
		`{"update_type":"node_execution","event_type":3,"node_id":%q,"node_type":"run","workflow_id":"wf-1","chat_id":"chat-1","exit_code":%d}`,
		nodeID, exitCode)
	return RawUpdate{
		Seq:       seq,
		Type:      "CHAT_UPDATE_TYPE_NODE_EXECUTION",
		Data:      []byte(data),
		CreatedAt: time.Date(2026, 7, 22, 12, 0, int(seq), 0, time.UTC),
	}
}

// workflowUpdateWithOutcome builds a workflow_status row carrying the run's
// declared verdict alongside its lifecycle status.
func workflowUpdateWithOutcome(seq int64, status, workflowID, parentID, outcome string) RawUpdate {
	data := fmt.Sprintf(
		`{"update_type":"workflow_status","status":%q,"workflow_id":%q,"workflow_name":"builtin://forge-one-shot","chat_id":"chat-1","parent_workflow_id":%q,"outcome":%q}`,
		status, workflowID, parentID, outcome)
	return RawUpdate{
		Seq:       seq,
		Type:      "CHAT_UPDATE_TYPE_WORKFLOW_STATUS",
		Data:      []byte(data),
		CreatedAt: time.Date(2026, 7, 22, 12, 0, int(seq), 0, time.UTC),
	}
}

func questionUpdateAt(seq int64, questionID, stepID, status string, at time.Time) RawUpdate {
	data := fmt.Sprintf(
		`{"update_type":"question","question_id":%q,"chat_id":"chat-1","workflow_id":"wf-1","thread_id":"t-1","step_id":%q,"status":%q,"metadata":"{\"type\":\"ask_user\",\"questions\":[{\"question\":\"Continue?\"}]}"}`,
		questionID, stepID, status)
	return RawUpdate{
		Seq:       seq,
		Type:      "CHAT_UPDATE_TYPE_QUESTION",
		Data:      []byte(data),
		CreatedAt: at,
	}
}

// TestFailedRunStepDoesNotRenderAsCompleted is the sharpest lie on the list: a
// gate lane that ran and failed printed a green boundary line, five iterations
// in a row.
func TestFailedRunStepDoesNotRenderAsCompleted(t *testing.T) {
	src := &fakeSource{
		updates: []RawUpdate{
			workflowUpdate(1, "started", "wf-1", ""),
			runNodeUpdate(2, "build", 2),
			runNodeUpdate(3, "lint", 0),
			workflowUpdate(4, "completed", "wf-1", ""),
		},
	}
	var out bytes.Buffer
	eng := newTestEngine(src, &out)
	eng.Renderer = RenderText
	if _, err := eng.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	text := out.String()
	if strings.Contains(text, "✓ node build completed") {
		t.Fatalf("a run step that exited 2 rendered as completed:\n%s", text)
	}
	if !strings.Contains(text, "✗ node build failed (exit 2)") {
		t.Fatalf("expected the failing lane and its exit code on the boundary line, got:\n%s", text)
	}
	// The lane that really did pass must still read green — the fix must not
	// turn every run step red.
	if !strings.Contains(text, "✓ node lint completed") {
		t.Fatalf("a run step that exited 0 must still render as completed:\n%s", text)
	}
}

// TestFailedRunStepEmitsNodeFailedOnNDJSON pins the machine surface: `follow`
// consumers grep the event type, so a red lane must not be a node_completed.
func TestFailedRunStepEmitsNodeFailedOnNDJSON(t *testing.T) {
	src := &fakeSource{
		updates: []RawUpdate{
			workflowUpdate(1, "started", "wf-1", ""),
			runNodeUpdate(2, "test", 1),
			workflowUpdate(3, "completed", "wf-1", ""),
		},
	}
	var out bytes.Buffer
	eng := newTestEngine(src, &out)
	if _, err := eng.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, ev := range parseNDJSON(t, out.String()) {
		if ev.NodeID != "test" {
			continue
		}
		if ev.Event != EventNodeFailed {
			t.Fatalf("a run step that exited 1 emitted %q, want %q", ev.Event, EventNodeFailed)
		}
		if ev.ExitCode == nil || *ev.ExitCode != 1 {
			t.Fatalf("node_failed must carry the exit code, got %v", ev.ExitCode)
		}
		return
	}
	t.Fatalf("no event for the run node at all:\n%s", out.String())
}

// TestRunEndingAtFailureOutcomeIsNotSuccess covers the run's terminal boundary:
// routing to a `failed` node completes the graph, so the lifecycle status alone
// reports a success.
func TestRunEndingAtFailureOutcomeIsNotSuccess(t *testing.T) {
	src := &fakeSource{
		updates: []RawUpdate{
			workflowUpdate(1, "started", "wf-1", ""),
			workflowUpdateWithOutcome(2, "completed", "wf-1", "", OutcomeFailure),
		},
	}
	var out bytes.Buffer
	eng := newTestEngine(src, &out)
	eng.Renderer = RenderText
	code, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if code == ExitSuccess {
		t.Fatalf("a run that ended at its failure terminal exited %d (success)", code)
	}
	if strings.Contains(out.String(), "✓ workflow completed") {
		t.Fatalf("a run that did not pass rendered as completed:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "WITHOUT PASSING") {
		t.Fatalf("expected the terminal line to say the run did not pass, got:\n%s", out.String())
	}
	if got := eng.TerminalOutcome(); got != OutcomeFailure {
		t.Fatalf("TerminalOutcome() = %q, want %q", got, OutcomeFailure)
	}
	// The LIFECYCLE is still completed — the Temporal execution really did
	// finish. The two facts must stay separable.
	if got := eng.TerminalStatus(); got != "completed" {
		t.Fatalf("TerminalStatus() = %q, want %q — the lifecycle must not be overwritten by the verdict", got, "completed")
	}
}

// TestRunEndingWithSuccessOutcomeStillExitsZero guards the other direction: the
// fix must not turn ordinary completions red.
func TestRunEndingWithSuccessOutcomeStillExitsZero(t *testing.T) {
	for _, outcome := range []string{OutcomeSuccess, ""} {
		src := &fakeSource{
			updates: []RawUpdate{
				workflowUpdate(1, "started", "wf-1", ""),
				workflowUpdateWithOutcome(2, "completed", "wf-1", "", outcome),
			},
		}
		var out bytes.Buffer
		eng := newTestEngine(src, &out)
		eng.Renderer = RenderText
		code, err := eng.Run(context.Background())
		if err != nil {
			t.Fatalf("outcome %q: Run: %v", outcome, err)
		}
		if code != ExitSuccess {
			t.Fatalf("outcome %q: exited %d, want 0", outcome, code)
		}
		if !strings.Contains(out.String(), "✓ workflow completed") {
			t.Fatalf("outcome %q: expected a green terminal line, got:\n%s", outcome, out.String())
		}
	}
}

// TestAttachAfterFinishReportsFailureOutcome covers the fallback path: a
// follower that attaches after the run ended never sees the terminal event and
// must read the verdict off the root state instead.
func TestAttachAfterFinishReportsFailureOutcome(t *testing.T) {
	src := &fakeSource{
		root: RootState{Found: true, Status: "completed", Outcome: OutcomeFailure},
	}
	var out bytes.Buffer
	eng := newTestEngine(src, &out)
	eng.Renderer = RenderText
	code, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code == ExitSuccess {
		t.Fatalf("attaching to a finished run that did not pass exited %d (success)", code)
	}
	if strings.Contains(out.String(), "✓ workflow completed") {
		t.Fatalf("synthetic terminal rendered as success:\n%s", out.String())
	}
}

// TestQuestionGateEmitsCloseEventWithTimeInGate covers the missing gate close:
// the measured run showed "❓ question raised" at 02:05:01 and nothing until
// 02:29:05, leaving 22m44s of parked time invisible on the stream.
func TestQuestionGateEmitsCloseEventWithTimeInGate(t *testing.T) {
	opened := time.Date(2026, 7, 22, 2, 5, 1, 0, time.UTC)
	closed := opened.Add(22*time.Minute + 44*time.Second)

	src := &fakeSource{
		updates: []RawUpdate{
			workflowUpdate(1, "started", "wf-1", ""),
			questionUpdateAt(2, "q-1", "review_checkpoint", "pending", opened),
			questionUpdateAt(3, "q-1", "review_checkpoint", "resolved", closed),
			workflowUpdate(4, "completed", "wf-1", ""),
		},
	}
	var out bytes.Buffer
	eng := newTestEngine(src, &out)
	if _, err := eng.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var closeEv *Event
	for _, ev := range parseNDJSON(t, out.String()) {
		if ev.Event == EventQuestionAnswered {
			e := ev
			closeEv = &e
		}
	}
	if closeEv == nil {
		t.Fatalf("no gate close event on the stream:\n%s", out.String())
	}
	wantMs := (22*time.Minute + 44*time.Second).Milliseconds()
	if closeEv.DurationMs != wantMs {
		t.Fatalf("time in gate = %dms, want %dms", closeEv.DurationMs, wantMs)
	}

	// And it must be readable on the human stream, with the duration.
	var text bytes.Buffer
	eng2 := newTestEngine(&fakeSource{updates: src.updates}, &text)
	eng2.Renderer = RenderText
	if _, err := eng2.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(text.String(), "question answered") ||
		!strings.Contains(text.String(), "22m44s in gate") {
		t.Fatalf("expected a rendered gate close with its duration, got:\n%s", text.String())
	}
}

// TestGateCloseIsEmittedOnce guards against the resolved row replaying on every
// poll the way the pending one does.
func TestGateCloseIsEmittedOnce(t *testing.T) {
	opened := time.Date(2026, 7, 22, 2, 5, 1, 0, time.UTC)
	src := &fakeSource{
		updates: []RawUpdate{
			workflowUpdate(1, "started", "wf-1", ""),
			questionUpdateAt(2, "q-1", "review_checkpoint", "pending", opened),
			questionUpdateAt(3, "q-1", "review_checkpoint", "resolved", opened.Add(time.Minute)),
			questionUpdateAt(4, "q-1", "review_checkpoint", "resolved", opened.Add(time.Minute)),
			workflowUpdate(5, "completed", "wf-1", ""),
		},
	}
	var out bytes.Buffer
	eng := newTestEngine(src, &out)
	if _, err := eng.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	closes := 0
	for _, ev := range parseNDJSON(t, out.String()) {
		if ev.Event == EventQuestionAnswered {
			closes++
		}
	}
	if closes != 1 {
		t.Fatalf("gate close emitted %d times, want 1", closes)
	}
}

// TestGateCloseWithoutObservedOpenOmitsDuration: a follower that attached late
// must report the close with no duration rather than inventing one.
func TestGateCloseWithoutObservedOpenOmitsDuration(t *testing.T) {
	src := &fakeSource{
		updates: []RawUpdate{
			questionUpdateAt(1, "q-1", "review_checkpoint", "resolved", time.Date(2026, 7, 22, 2, 29, 5, 0, time.UTC)),
			workflowUpdate(2, "completed", "wf-1", ""),
		},
	}
	var out bytes.Buffer
	eng := newTestEngine(src, &out)
	if _, err := eng.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, ev := range parseNDJSON(t, out.String()) {
		if ev.Event == EventQuestionAnswered && ev.DurationMs != 0 {
			t.Fatalf("duration %dms reported for a gate whose open was never seen", ev.DurationMs)
		}
	}
}
