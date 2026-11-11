// Copyright (c) 2025 Reliant Labs
package execfollow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSource serves a fixed update feed and a root status. The engine's own
// behaviour is tested in the SDK (sdk/go/reliant/follow); these tests cover only
// what this package adds around it: shell hooks, and the CLI's wiring.
type fakeSource struct {
	updates []RawUpdate
	root    RootState
}

func (f *fakeSource) Updates(_ context.Context, since int64) ([]RawUpdate, int64, error) {
	var out []RawUpdate
	var latest int64
	for _, u := range f.updates {
		latest = max(latest, u.Seq)
		if u.Seq > since {
			out = append(out, u)
		}
	}
	return out, latest, nil
}

func (f *fakeSource) Root(context.Context) (RootState, error)        { return f.root, nil }
func (f *fakeSource) Pending(context.Context) ([]PendingGate, error) { return nil, nil }

func nodeUpdate(seq int64, eventType int, nodeID string) RawUpdate {
	data := fmt.Sprintf(`{"update_type":"node_execution","event_type":%d,"node_id":%q,"workflow_id":"wf-1","chat_id":"chat-1"}`, eventType, nodeID)
	return RawUpdate{Seq: seq, Type: "CHAT_UPDATE_TYPE_NODE_EXECUTION", Data: []byte(data), CreatedAt: time.Date(2026, 7, 22, 12, 0, int(seq), 0, time.UTC)}
}

func workflowUpdate(seq int64, status, workflowID string) RawUpdate {
	data := fmt.Sprintf(`{"update_type":"workflow_status","status":%q,"workflow_id":%q,"workflow_name":"builtin://agent","chat_id":"chat-1","parent_workflow_id":""}`, status, workflowID)
	return RawUpdate{Seq: seq, Type: "CHAT_UPDATE_TYPE_WORKFLOW_STATUS", Data: []byte(data), CreatedAt: time.Date(2026, 7, 22, 12, 0, int(seq), 0, time.UTC)}
}

func questionUpdate(seq int64, questionID, stepID, status, metadata string) RawUpdate {
	data, _ := json.Marshal(map[string]any{"update_type": "question", "question_id": questionID, "step_id": stepID, "status": status, "metadata": metadata})
	return RawUpdate{Seq: seq, Type: "CHAT_UPDATE_TYPE_QUESTION", Data: data, CreatedAt: time.Date(2026, 7, 22, 12, 0, int(seq), 0, time.UTC)}
}

func newTestEngine(src Source, out *bytes.Buffer) *Engine {
	return &Engine{Source: src, ExecutionID: "chat-1", Out: out, Log: &bytes.Buffer{}, Interval: 5 * time.Millisecond}
}

func TestEngineFiresHooksOnEvents(t *testing.T) {
	src := &fakeSource{
		updates: []RawUpdate{
			workflowUpdate(1, "started", "wf-1"),
			nodeUpdate(2, 1, "plan"),
			nodeUpdate(3, 4, "plan"),            // node failed
			workflowUpdate(4, "failed", "wf-1"), // root failed
		},
		root: RootState{Found: true, Status: "failed"},
	}

	logFile := filepath.Join(t.TempDir(), "hooks.log")

	var out bytes.Buffer
	engine := newTestEngine(src, &out)
	engine.Hooks = []Hook{
		{On: "node_failed", Cmd: `printf 'FAILED:%s\n' "$RELIANT_EVENT_NODE_ID" >> ` + logFile},
		{On: "workflow_failed", Cmd: `printf 'WF:%s\n' "$RELIANT_EVENT_STATE" >> ` + logFile},
	}

	code, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitFailed {
		t.Errorf("exit code = %d, want %d", code, ExitFailed)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("hook log missing: %v", err)
	}
	if got, want := strings.TrimSpace(string(data)), "FAILED:plan\nWF:failed"; got != want {
		t.Errorf("hook log = %q, want %q", got, want)
	}
	if got := engine.TerminalStatus(); got != "failed" {
		t.Errorf("TerminalStatus = %q, want failed", got)
	}
}

func TestEngineQuestionHookFires(t *testing.T) {
	askMeta := `{"type":"ask_user","questions":[{"question":"Proceed?","options":[{"label":"Continue"}]}]}`
	src := &fakeSource{
		updates: []RawUpdate{
			workflowUpdate(1, "started", "wf-1"),
			questionUpdate(2, "q-1", "ask_question", "pending", askMeta),
			workflowUpdate(3, "completed", "wf-1"),
		},
		root: RootState{Found: true, Status: "completed"},
	}

	logFile := filepath.Join(t.TempDir(), "q.log")
	var out bytes.Buffer
	engine := newTestEngine(src, &out)
	engine.Hooks = []Hook{{On: EventQuestion, Cmd: `printf 'Q:%s\n' "$RELIANT_EVENT" >> ` + logFile}}

	if _, err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("hook log missing: %v", err)
	}
	if strings.TrimSpace(string(data)) != "Q:question" {
		t.Errorf("hook log = %q, want Q:question", strings.TrimSpace(string(data)))
	}
}

// A hook receives the canonical NDJSON line even when the visible stream is the
// human-readable one (`workflow watch`), so a hook's stdin does not depend on
// which command ran it.
func TestHookStdinIsNDJSONEvenWithARenderer(t *testing.T) {
	src := &fakeSource{
		updates: []RawUpdate{workflowUpdate(1, "started", "wf-1"), workflowUpdate(2, "completed", "wf-1")},
		root:    RootState{Found: true, Status: "completed"},
	}
	logFile := filepath.Join(t.TempDir(), "stdin.log")
	var out bytes.Buffer
	engine := newTestEngine(src, &out)
	engine.Renderer = RenderText
	engine.Hooks = []Hook{{On: EventWorkflowCompleted, Cmd: `cat >> ` + logFile}}

	if _, err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("hook stdin missing: %v", err)
	}
	var ev Event
	if err := json.Unmarshal(bytes.TrimSpace(data), &ev); err != nil {
		t.Fatalf("hook stdin is not NDJSON: %q: %v", data, err)
	}
	if ev.Event != EventWorkflowCompleted {
		t.Errorf("hook saw %q, want %q", ev.Event, EventWorkflowCompleted)
	}
	if !strings.Contains(out.String(), "workflow completed") || strings.HasPrefix(strings.TrimSpace(out.String()), "{") {
		t.Errorf("visible stream should be rendered text, got %q", out.String())
	}
}

// A nil Out writes no event stream, as the SDK's engine documents: nothing
// reaches the process's stdout unless the caller names a writer.
func TestEngineNilOutWritesNoStream(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = stdout })

	src := &fakeSource{
		updates: []RawUpdate{nodeUpdate(1, 1, "build"), workflowUpdate(2, "completed", "wf-1")},
		root:    RootState{Found: true, Status: "completed"},
	}
	e := &Engine{Source: src, ExecutionID: "chat-1", Log: &bytes.Buffer{}, Interval: 5 * time.Millisecond}
	code, runErr := e.Run(context.Background())
	os.Stdout = stdout
	_ = w.Close()
	got, _ := io.ReadAll(r)
	if runErr != nil || code != ExitSuccess {
		t.Fatalf("Run = %d, %v", code, runErr)
	}
	if len(got) != 0 {
		t.Fatalf("a nil Out wrote the event stream to stdout:\n%s", got)
	}
}
