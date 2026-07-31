// Copyright (c) 2025 Reliant Labs
package execfollow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// A node whose condition is false never runs — and `workflow watch` printed
// "✓ node review completed" for it, because the activity that RECORDS the skip
// completed normally and the follower read only the lifecycle. A supervisor
// reading that feed cannot tell a reviewer that passed from one that was
// configured off. NODE_EXECUTION_STATUS_SKIPPED was already on the event.

// skippedNodeUpdate builds the node_execution row the runtime writes for a
// skipped node: event_type COMPLETED (the machinery finished) carrying
// status SKIPPED (the node did not run). The status value comes from the proto
// enum the emitter stamps, not a literal, so a renumbering moves both ends.
func skippedNodeUpdate(seq int64, nodeID string) RawUpdate {
	status, err := json.Marshal(reliantv1.NodeExecutionStatus_NODE_EXECUTION_STATUS_SKIPPED)
	if err != nil {
		panic(err)
	}
	data := fmt.Sprintf(
		`{"update_type":"node_execution","event_type":3,"node_id":%q,"node_type":"run","workflow_id":"wf-1","chat_id":"chat-1","status":%s}`,
		nodeID, status)
	return RawUpdate{
		Seq:       seq,
		Type:      "CHAT_UPDATE_TYPE_NODE_EXECUTION",
		Data:      []byte(data),
		CreatedAt: time.Date(2026, 7, 26, 12, 0, int(seq), 0, time.UTC),
	}
}

func TestSkippedNodeDoesNotRenderAsCompleted(t *testing.T) {
	// Fail loudly if the emitter's enum no longer decodes to what the mapper
	// matches on — otherwise every assertion below passes vacuously against an
	// event the follower simply dropped.
	status, err := json.Marshal(reliantv1.NodeExecutionStatus_NODE_EXECUTION_STATUS_SKIPPED)
	if err != nil {
		t.Fatalf("marshalling the skipped status: %v", err)
	}
	if decodeEnum(status) != statusSkipped {
		t.Fatalf("the emitter's SKIPPED status decodes to %d, but the mapper matches %d", decodeEnum(status), statusSkipped)
	}

	src := &fakeSource{
		updates: []RawUpdate{
			workflowUpdate(1, "started", "wf-1", ""),
			skippedNodeUpdate(2, "review"),
			runNodeUpdate(3, "lint", 0),
			workflowUpdate(4, "completed", "wf-1", ""),
		},
	}

	var out bytes.Buffer
	engine := &Engine{
		Source:      src,
		ExecutionID: "chat-1",
		Out:         &out,
		Log:         &bytes.Buffer{},
		Renderer:    RenderText,
		Interval:    time.Millisecond,
	}
	if _, err := engine.Run(context.Background()); err != nil {
		t.Fatalf("engine.Run: %v", err)
	}

	text := out.String()
	if strings.Contains(text, "✓ node review completed") {
		t.Fatalf("a node that never ran printed a green completion line:\n%s", text)
	}
	if !strings.Contains(text, "node review skipped") {
		t.Fatalf("the skipped node was not reported as skipped at all:\n%s", text)
	}
	// The node that really did run must still read as completed — skipped is a
	// new state, not a reclassification.
	if !strings.Contains(text, "✓ node lint completed") {
		t.Fatalf("a node that ran and passed stopped reporting as completed:\n%s", text)
	}
}

// The machine-readable stream carries its own event kind, so a pipeline can
// branch on it without parsing prose.
func TestSkippedNodeMapsToItsOwnEvent(t *testing.T) {
	e := &Engine{ExecutionID: "chat-1", nodeStates: map[string]string{}}
	ev, ok := e.mapUpdate(skippedNodeUpdate(1, "review"))
	if !ok {
		t.Fatal("the skipped node event was dropped entirely — invisible is not better than wrong")
	}
	if ev.Event != EventNodeSkipped {
		t.Fatalf("event = %q, want %q", ev.Event, EventNodeSkipped)
	}
	if ev.NewState != "skipped" {
		t.Fatalf("new_state = %q, want skipped", ev.NewState)
	}
}
