package core

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// Verbatim copies of the three models as they were declared before their
// nullable columns became pointers (origin/main at 578de5554). Their
// encoding/json output is the contract the pointer models must keep.

type mainStepExecution struct {
	ID             string         `json:"id"`
	WorkflowID     string         `json:"workflow_id"`
	StepID         string         `json:"step_id"`
	ActivityName   string         `json:"activity_name"`
	OutputJSON     sql.NullString `json:"output_json"`
	ExitCode       sql.NullInt64  `json:"exit_code"`
	Success        sql.NullBool   `json:"success"`
	DurationMs     sql.NullInt64  `json:"duration_ms"`
	LoopNodeID     sql.NullString `json:"loop_node_id"`
	LoopIteration  sql.NullInt64  `json:"loop_iteration"`
	CreatedAt      time.Time      `json:"created_at"`
	SavedMessageID sql.NullString `json:"saved_message_id"`
	InputJSON      sql.NullString `json:"input_json"`
	ErrorMessage   sql.NullString `json:"error_message"`
	Attempt        sql.NullInt32  `json:"attempt"`
	NodePath       sql.NullString `json:"node_path"`
}

type mainChatStepExecution struct {
	ID             string
	WorkflowID     string
	StepID         string
	ActivityName   string
	ExitCode       sql.NullInt64
	Success        sql.NullBool
	DurationMs     sql.NullInt64
	LoopNodeID     sql.NullString
	LoopIteration  sql.NullInt64
	CreatedAt      time.Time
	SavedMessageID sql.NullString
	OutputJSON     string
}

type mainWorkflowScenario struct {
	ID              string         `json:"id"`
	WorkflowDraftID sql.NullString `json:"workflow_draft_id,omitempty"`
	UserID          string         `json:"user_id"`
	Name            string         `json:"name"`
	Description     sql.NullString `json:"description,omitempty"`
	Events          string         `json:"events"`
	Expect          sql.NullString `json:"expect,omitempty"`
	LastRunAt       sql.NullTime   `json:"last_run_at,omitempty"`
	LastRunStatus   sql.NullString `json:"last_run_status,omitempty"`
	LastRunResult   sql.NullString `json:"last_run_result,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	Version         int64          `json:"version"`
}

func ptr[T any](v T) *T { return &v }

// encodings returns v's JSON through every path a value can take to an
// encoder: by value, by pointer, behind an interface, in a slice, and as a
// field of a wrapper struct.
func encodings(t *testing.T, v, p any) map[string]string {
	t.Helper()
	type wrapper struct {
		Payload any `json:"payload"`
	}
	out := map[string]string{}
	for name, x := range map[string]any{
		"value":     v,
		"pointer":   p,
		"slice":     []any{v},
		"wrapper":   wrapper{Payload: v},
		"wrapperP":  wrapper{Payload: p},
		"mapOfAny":  map[string]any{"k": v},
		"interface": any(v),
	} {
		b, err := json.Marshal(x)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out[name] = string(b)
	}
	return out
}

func assertSameJSON(t *testing.T, got, want map[string]string) {
	t.Helper()
	for path, w := range want {
		if got[path] != w {
			t.Errorf("%s:\n got  %s\n want %s", path, got[path], w)
		}
	}
}

func TestStepExecutionJSONKeepsThePrePointerShape(t *testing.T) {
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		model StepExecution
		main  mainStepExecution
	}{
		"nil": {
			model: StepExecution{ID: "s1", CreatedAt: at},
			main:  mainStepExecution{ID: "s1", CreatedAt: at},
		},
		"zero": {
			model: StepExecution{ID: "s1", CreatedAt: at, OutputJSON: ptr(""), ExitCode: ptr(int64(0)), Success: ptr(false), DurationMs: ptr(int64(0)), LoopNodeID: ptr(""), LoopIteration: ptr(int64(0)), SavedMessageID: ptr(""), InputJSON: ptr(""), ErrorMessage: ptr(""), Attempt: ptr(int32(0)), NodePath: ptr("")},
			main:  mainStepExecution{ID: "s1", CreatedAt: at, OutputJSON: sql.NullString{Valid: true}, ExitCode: sql.NullInt64{Valid: true}, Success: sql.NullBool{Valid: true}, DurationMs: sql.NullInt64{Valid: true}, LoopNodeID: sql.NullString{Valid: true}, LoopIteration: sql.NullInt64{Valid: true}, SavedMessageID: sql.NullString{Valid: true}, InputJSON: sql.NullString{Valid: true}, ErrorMessage: sql.NullString{Valid: true}, Attempt: sql.NullInt32{Valid: true}, NodePath: sql.NullString{Valid: true}},
		},
		"set": {
			model: StepExecution{ID: "s1", WorkflowID: "w", StepID: "st", ActivityName: "a", CreatedAt: at, OutputJSON: ptr(`{"x":1}`), ExitCode: ptr(int64(2)), Success: ptr(true), DurationMs: ptr(int64(30)), LoopNodeID: ptr("loop"), LoopIteration: ptr(int64(4)), SavedMessageID: ptr("m"), InputJSON: ptr(`{}`), ErrorMessage: ptr("boom"), Attempt: ptr(int32(3)), NodePath: ptr("a.b")},
			main:  mainStepExecution{ID: "s1", WorkflowID: "w", StepID: "st", ActivityName: "a", CreatedAt: at, OutputJSON: sql.NullString{String: `{"x":1}`, Valid: true}, ExitCode: sql.NullInt64{Int64: 2, Valid: true}, Success: sql.NullBool{Bool: true, Valid: true}, DurationMs: sql.NullInt64{Int64: 30, Valid: true}, LoopNodeID: sql.NullString{String: "loop", Valid: true}, LoopIteration: sql.NullInt64{Int64: 4, Valid: true}, SavedMessageID: sql.NullString{String: "m", Valid: true}, InputJSON: sql.NullString{String: `{}`, Valid: true}, ErrorMessage: sql.NullString{String: "boom", Valid: true}, Attempt: sql.NullInt32{Int32: 3, Valid: true}, NodePath: sql.NullString{String: "a.b", Valid: true}},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			assertSameJSON(t, encodings(t, c.model, &c.model), encodings(t, c.main, &c.main))

			b, _ := json.Marshal(c.main)
			var back StepExecution
			if err := json.Unmarshal(b, &back); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(back, c.model) {
				t.Errorf("decoding the pre-pointer JSON:\n got  %+v\n want %+v", back, c.model)
			}
		})
	}
}

func TestChatStepExecutionJSONKeepsThePrePointerShape(t *testing.T) {
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		model ChatStepExecution
		main  mainChatStepExecution
	}{
		"nil": {
			model: ChatStepExecution{ID: "c1", CreatedAt: at},
			main:  mainChatStepExecution{ID: "c1", CreatedAt: at},
		},
		"zero": {
			model: ChatStepExecution{ID: "c1", CreatedAt: at, ExitCode: ptr(int64(0)), Success: ptr(false), DurationMs: ptr(int64(0)), LoopNodeID: ptr(""), LoopIteration: ptr(int64(0)), SavedMessageID: ptr("")},
			main:  mainChatStepExecution{ID: "c1", CreatedAt: at, ExitCode: sql.NullInt64{Valid: true}, Success: sql.NullBool{Valid: true}, DurationMs: sql.NullInt64{Valid: true}, LoopNodeID: sql.NullString{Valid: true}, LoopIteration: sql.NullInt64{Valid: true}, SavedMessageID: sql.NullString{Valid: true}},
		},
		"set": {
			model: ChatStepExecution{ID: "c1", WorkflowID: "w", StepID: "st", ActivityName: "a", CreatedAt: at, ExitCode: ptr(int64(1)), Success: ptr(true), DurationMs: ptr(int64(9)), LoopNodeID: ptr("l"), LoopIteration: ptr(int64(2)), SavedMessageID: ptr("m"), OutputJSON: `{"y":2}`},
			main:  mainChatStepExecution{ID: "c1", WorkflowID: "w", StepID: "st", ActivityName: "a", CreatedAt: at, ExitCode: sql.NullInt64{Int64: 1, Valid: true}, Success: sql.NullBool{Bool: true, Valid: true}, DurationMs: sql.NullInt64{Int64: 9, Valid: true}, LoopNodeID: sql.NullString{String: "l", Valid: true}, LoopIteration: sql.NullInt64{Int64: 2, Valid: true}, SavedMessageID: sql.NullString{String: "m", Valid: true}, OutputJSON: `{"y":2}`},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			assertSameJSON(t, encodings(t, c.model, &c.model), encodings(t, c.main, &c.main))

			b, _ := json.Marshal(c.main)
			var back ChatStepExecution
			if err := json.Unmarshal(b, &back); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(back, c.model) {
				t.Errorf("decoding the pre-pointer JSON:\n got  %+v\n want %+v", back, c.model)
			}
		})
	}
}

func TestWorkflowScenarioJSONKeepsThePrePointerShape(t *testing.T) {
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		model WorkflowScenario
		main  mainWorkflowScenario
	}{
		"nil": {
			model: WorkflowScenario{ID: "sc", UserID: "u", Events: "[]", CreatedAt: at, UpdatedAt: at, Version: 1},
			main:  mainWorkflowScenario{ID: "sc", UserID: "u", Events: "[]", CreatedAt: at, UpdatedAt: at, Version: 1},
		},
		"zero": {
			model: WorkflowScenario{ID: "sc", UserID: "u", CreatedAt: at, UpdatedAt: at, WorkflowDraftID: ptr(""), Description: ptr(""), Expect: ptr(""), LastRunAt: ptr(time.Time{}), LastRunStatus: ptr(""), LastRunResult: ptr("")},
			main:  mainWorkflowScenario{ID: "sc", UserID: "u", CreatedAt: at, UpdatedAt: at, WorkflowDraftID: sql.NullString{Valid: true}, Description: sql.NullString{Valid: true}, Expect: sql.NullString{Valid: true}, LastRunAt: sql.NullTime{Valid: true}, LastRunStatus: sql.NullString{Valid: true}, LastRunResult: sql.NullString{Valid: true}},
		},
		"set": {
			model: WorkflowScenario{ID: "sc", UserID: "u", Name: "n", Events: `[{"e":1}]`, CreatedAt: at, UpdatedAt: at, Version: 7, WorkflowDraftID: ptr("d"), Description: ptr("desc"), Expect: ptr(`{"ok":true}`), LastRunAt: ptr(at), LastRunStatus: ptr("passed"), LastRunResult: ptr(`{}`)},
			main:  mainWorkflowScenario{ID: "sc", UserID: "u", Name: "n", Events: `[{"e":1}]`, CreatedAt: at, UpdatedAt: at, Version: 7, WorkflowDraftID: sql.NullString{String: "d", Valid: true}, Description: sql.NullString{String: "desc", Valid: true}, Expect: sql.NullString{String: `{"ok":true}`, Valid: true}, LastRunAt: sql.NullTime{Time: at, Valid: true}, LastRunStatus: sql.NullString{String: "passed", Valid: true}, LastRunResult: sql.NullString{String: `{}`, Valid: true}},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			assertSameJSON(t, encodings(t, c.model, &c.model), encodings(t, c.main, &c.main))

			b, _ := json.Marshal(c.main)
			var back WorkflowScenario
			if err := json.Unmarshal(b, &back); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(back, c.model) {
				t.Errorf("decoding the pre-pointer JSON:\n got  %+v\n want %+v", back, c.model)
			}
		})
	}
}

// The literal bytes of an all-NULL row, so the contract does not rest on the
// mirror types above alone.
func TestWorkflowScenarioNullColumnsLiteralJSON(t *testing.T) {
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	b, err := json.Marshal(any(WorkflowScenario{ID: "sc", UserID: "u", Events: "[]", CreatedAt: at, UpdatedAt: at, Version: 1}))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"id":"sc","workflow_draft_id":{"String":"","Valid":false},"user_id":"u","name":"","description":{"String":"","Valid":false},"events":"[]","expect":{"String":"","Valid":false},"last_run_at":{"Time":"0001-01-01T00:00:00Z","Valid":false},"last_run_status":{"String":"","Valid":false},"last_run_result":{"String":"","Valid":false},"created_at":"2026-11-01T12:00:00Z","updated_at":"2026-11-01T12:00:00Z","version":1}`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
}
