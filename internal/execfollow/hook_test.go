// Copyright (c) 2025 Reliant Labs
package execfollow

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleEvent() Event {
	return Event{
		Event:        EventNodeFailed,
		ExecutionID:  "chat-123",
		WorkflowID:   "wf-1",
		WorkflowName: "builtin://agent",
		NodeID:       "plan",
		OldState:     "running",
		NewState:     "failed",
		Timestamp:    "2026-07-22T12:00:00Z",
		Sequence:     42,
		Error:        "boom",
	}
}

func TestHookReceivesEnvAndStdin(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env.txt")
	stdinFile := filepath.Join(dir, "stdin.json")

	ev := sampleEvent()
	evJSON, err := marshalEvent(ev)
	if err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	runner := &HookRunner{Stderr: &stderr}
	hook := Hook{On: "node_failed", Cmd: "env > " + envFile + " && cat > " + stdinFile}

	runner.Run(context.Background(), []Hook{hook}, ev, evJSON)

	// stdin carries the exact event JSON.
	stdinData, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("hook did not run (stdin file missing): %v; stderr: %s", err, stderr.String())
	}
	if string(stdinData) != string(evJSON) {
		t.Errorf("stdin = %q, want %q", stdinData, evJSON)
	}
	var roundTrip Event
	if err := json.Unmarshal(stdinData, &roundTrip); err != nil {
		t.Errorf("stdin is not valid event JSON: %v", err)
	} else if roundTrip != ev {
		t.Errorf("stdin event = %+v, want %+v", roundTrip, ev)
	}

	// RELIANT_EVENT_* env vars are present.
	envData, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("env file missing: %v", err)
	}
	for _, want := range []string{
		"RELIANT_EVENT=node_failed",
		"RELIANT_EVENT_EXECUTION_ID=chat-123",
		"RELIANT_EVENT_WORKFLOW_ID=wf-1",
		"RELIANT_EVENT_WORKFLOW_NAME=builtin://agent",
		"RELIANT_EVENT_NODE_ID=plan",
		"RELIANT_EVENT_STATE=failed",
		"RELIANT_EVENT_OLD_STATE=running",
		"RELIANT_EVENT_SEQUENCE=42",
	} {
		if !strings.Contains(string(envData), want+"\n") {
			t.Errorf("hook env missing %q", want)
		}
	}
}

func TestHookOnlyMatchingHooksRun(t *testing.T) {
	dir := t.TempDir()
	matched := filepath.Join(dir, "matched")
	unmatched := filepath.Join(dir, "unmatched")
	anyFile := filepath.Join(dir, "any")

	ev := sampleEvent() // node_failed
	evJSON, _ := marshalEvent(ev)

	runner := &HookRunner{Stderr: &bytes.Buffer{}}
	runner.Run(context.Background(), []Hook{
		{On: "node_failed", Cmd: "touch " + matched},
		{On: "node_completed", Cmd: "touch " + unmatched},
		{On: "any", Cmd: "touch " + anyFile},
	}, ev, evJSON)

	if _, err := os.Stat(matched); err != nil {
		t.Error("matching hook did not run")
	}
	if _, err := os.Stat(anyFile); err != nil {
		t.Error("'any' hook did not run")
	}
	if _, err := os.Stat(unmatched); err == nil {
		t.Error("non-matching hook ran")
	}
}

func TestHookFailureIsLoggedAndSwallowed(t *testing.T) {
	ev := sampleEvent()
	evJSON, _ := marshalEvent(ev)

	var stderr bytes.Buffer
	runner := &HookRunner{Stderr: &stderr}
	// Must not panic or abort; second hook still runs.
	dir := t.TempDir()
	after := filepath.Join(dir, "after")
	runner.Run(context.Background(), []Hook{
		{On: "any", Cmd: "exit 3"},
		{On: "any", Cmd: "touch " + after},
	}, ev, evJSON)

	if !strings.Contains(stderr.String(), "hook failed") {
		t.Errorf("expected failure log, got: %s", stderr.String())
	}
	if _, err := os.Stat(after); err != nil {
		t.Error("hook after a failing hook did not run")
	}
}

func TestHookTimeoutDoesNotHangFollow(t *testing.T) {
	ev := sampleEvent()
	evJSON, _ := marshalEvent(ev)

	var stderr bytes.Buffer
	runner := &HookRunner{Stderr: &stderr, Timeout: 200 * time.Millisecond}

	start := time.Now()
	runner.Run(context.Background(), []Hook{{On: "any", Cmd: "sleep 30"}}, ev, evJSON)
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Fatalf("hook run took %v — timeout not applied", elapsed)
	}
	if !strings.Contains(stderr.String(), "hook failed") {
		t.Errorf("expected timeout to be logged as failure, got: %s", stderr.String())
	}
}
