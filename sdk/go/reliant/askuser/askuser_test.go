// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0
package askuser

import (
	"encoding/json"
	"testing"
)

func TestParseMetadataRejectsNonAskUser(t *testing.T) {
	cases := []string{"", "  ", "{}", `{"type":"other"}`, "not-json", "{broken"}
	for _, c := range cases {
		if md, ok := ParseMetadata(c); ok || md != nil {
			t.Errorf("ParseMetadata(%q) = (%v, %v), want (nil, false)", c, md, ok)
		}
	}
}

func TestParseMetadataRejectsNoQuestions(t *testing.T) {
	for _, c := range []string{`{"type":"ask_user"}`, `{"type":"ask_user","questions":[]}`} {
		if _, ok := ParseMetadata(c); ok {
			t.Errorf("ParseMetadata(%q) ok=true, want false", c)
		}
	}
}

func TestParseMetadataNewFormat(t *testing.T) {
	metadata := mustJSON(t, map[string]any{
		"type":         "ask_user",
		"tool_call_id": "call_abc123",
		"questions": []any{
			map[string]any{
				"question": "Which approach?",
				"options": []any{
					map[string]any{"label": "A", "description": "Option A"},
					map[string]any{"label": "B", "description": "Option B"},
				},
				"allow_multiple": false,
			},
		},
	})
	md, ok := ParseMetadata(metadata)
	if !ok {
		t.Fatal("expected ok")
	}
	if md.ToolCallID != "call_abc123" {
		t.Errorf("tool_call_id = %q", md.ToolCallID)
	}
	if len(md.Questions) != 1 || md.Questions[0].Question != "Which approach?" {
		t.Fatalf("questions = %+v", md.Questions)
	}
	if got := md.Questions[0].OptionLabels(); len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Errorf("option labels = %v", got)
	}
}

func TestParseMetadataLegacyEnvelope(t *testing.T) {
	innerInput := mustJSON(t, map[string]any{
		"questions": []any{
			map[string]any{"question": "Pick a flavor", "options": []any{map[string]any{"label": "Vanilla"}}, "allow_multiple": false},
			map[string]any{"question": "Pick toppings", "options": []any{map[string]any{"label": "Sprinkles"}, map[string]any{"label": "Nuts"}}, "allow_multiple": true},
		},
	})
	metadata := mustJSON(t, map[string]any{
		"__reliant_tool_meta__": map[string]any{"available_tools": []string{"bash", "ask_user"}},
		"input":                 innerInput,
		"tool_call_id":          "call_xyz789",
		"type":                  "ask_user",
	})
	md, ok := ParseMetadata(metadata)
	if !ok {
		t.Fatal("expected ok")
	}
	if len(md.Questions) != 2 {
		t.Fatalf("questions = %+v", md.Questions)
	}
	if md.Questions[0].Question != "Pick a flavor" || md.Questions[1].Question != "Pick toppings" {
		t.Errorf("question text wrong: %+v", md.Questions)
	}
	if !md.Questions[1].AllowMultiple {
		t.Errorf("expected allow_multiple on second question")
	}
}

func TestParseMetadataLegacyEnvelopeTask(t *testing.T) {
	for _, tc := range []struct {
		name  string
		inner any
		outer any
		want  bool
	}{
		{name: "nested true", inner: true, want: true},
		{name: "nested string true", inner: "true", want: true},
		{name: "nested false", inner: false, want: false},
		{name: "outer false wins", inner: true, outer: false, want: false},
		{name: "outer true wins", inner: false, outer: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner := mustJSON(t, map[string]any{
				"task":      tc.inner,
				"questions": []any{map[string]any{"question": "Sign in"}},
			})
			raw := map[string]any{"type": "ask_user", "input": inner}
			if tc.outer != nil {
				raw["task"] = tc.outer
			}
			md, ok := ParseMetadata(mustJSON(t, raw))
			if !ok {
				t.Fatal("expected ok")
			}
			if md.Task != tc.want {
				t.Errorf("Task = %v, want %v", md.Task, tc.want)
			}
			if len(md.Questions) != 1 || md.Questions[0].Question != "Sign in" {
				t.Fatalf("questions = %+v", md.Questions)
			}
		})
	}
}

func TestParseMetadataDoubleEncodedQuestions(t *testing.T) {
	questions := mustJSON(t, []any{
		map[string]any{"question": "Which approach?", "options": []any{map[string]any{"label": "A"}}},
	})
	metadata := mustJSON(t, map[string]any{
		"type":         "ask_user",
		"tool_call_id": "call_double",
		"questions":    questions, // double-encoded: a JSON string
	})
	md, ok := ParseMetadata(metadata)
	if !ok {
		t.Fatal("expected ok")
	}
	if len(md.Questions) != 1 || md.Questions[0].Question != "Which approach?" {
		t.Fatalf("questions = %+v", md.Questions)
	}
}

func TestParseMetadataDoubleEncodedInEnvelope(t *testing.T) {
	questions := mustJSON(t, []any{map[string]any{"question": "Envelope double?", "options": []any{map[string]any{"label": "Yes"}}}})
	inner := mustJSON(t, map[string]any{"questions": questions})
	metadata := mustJSON(t, map[string]any{"type": "ask_user", "tool_call_id": "c", "input": inner})
	md, ok := ParseMetadata(metadata)
	if !ok {
		t.Fatal("expected ok")
	}
	if len(md.Questions) != 1 || md.Questions[0].Question != "Envelope double?" {
		t.Fatalf("questions = %+v", md.Questions)
	}
}

func TestParseMetadataRejectsBadShapes(t *testing.T) {
	cases := []string{
		`{"type":"ask_user","questions":{"question":"Solo?"}}`,
		`{"type":"ask_user","questions":42}`,
		`{"type":"ask_user","questions":"not json at all"}`,
		`{"type":"ask_user","questions":"{\"question\":\"hi\"}"}`,
		`{"type":"ask_user","input":"not-valid-json"}`,
		`{"type":"ask_user","questions":[null,42,{"nope":true}]}`,
	}
	for _, c := range cases {
		if _, ok := ParseMetadata(c); ok {
			t.Errorf("ParseMetadata(%q) ok=true, want false", c)
		}
	}
}

func TestParseMetadataFiltersInvalidEntries(t *testing.T) {
	metadata := `{"type":"ask_user","tool_call_id":"call_filter","questions":["just a string",42,null,{"no_question":true},{"question":123},{"question":"Valid one?"}]}`
	md, ok := ParseMetadata(metadata)
	if !ok {
		t.Fatal("expected ok")
	}
	if len(md.Questions) != 1 || md.Questions[0].Question != "Valid one?" {
		t.Fatalf("questions = %+v", md.Questions)
	}
	if len(md.Questions[0].Options) != 0 {
		t.Errorf("options should be coerced to empty, got %+v", md.Questions[0].Options)
	}
}

func TestBuildResponseDataSingle(t *testing.T) {
	got, err := BuildResponseData([]Answer{{Question: "Which approach?", Selected: []string{"A"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"answers":[{"question":"Which approach?","selected":["A"]}]}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestBuildResponseDataMultiWithFreetext(t *testing.T) {
	got, err := BuildResponseData([]Answer{
		{Question: "Pick a flavor", Selected: []string{"Vanilla"}},
		{Question: "Pick toppings", Selected: []string{"Sprinkles", "Nuts"}},
		{Question: "Anything else?", Selected: []string{}, Freetext: "make it fast"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Round-trip and assert structure rather than exact bytes for the 3-answer case.
	var env responseEnvelope
	if err := json.Unmarshal([]byte(got), &env); err != nil {
		t.Fatalf("invalid response_data JSON %q: %v", got, err)
	}
	if len(env.Answers) != 3 {
		t.Fatalf("answers = %+v", env.Answers)
	}
	if env.Answers[1].Question != "Pick toppings" || len(env.Answers[1].Selected) != 2 {
		t.Errorf("second answer wrong: %+v", env.Answers[1])
	}
	if env.Answers[2].Freetext != "make it fast" {
		t.Errorf("freetext lost: %+v", env.Answers[2])
	}
	if env.Answers[0].Selected == nil {
		t.Errorf("selected must serialize as [] not null")
	}
}

func TestMatchOption(t *testing.T) {
	q := Question{Options: []Option{{Label: "Continue"}, {Label: "Revise"}}}
	if _, ok := q.MatchOption("Revise"); !ok {
		t.Error("exact match failed")
	}
	if _, ok := q.MatchOption("revise"); !ok {
		t.Error("case-insensitive match failed")
	}
	if _, ok := q.MatchOption("nope"); ok {
		t.Error("unexpected match")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseMetadataTask(t *testing.T) {
	for metadata, want := range map[string]bool{
		`{"type":"ask_user","task":true,"questions":[{"question":"Run gcloud auth login"}]}`:   true,
		`{"type":"ask_user","task":"true","questions":[{"question":"Run gcloud auth login"}]}`: true,
		`{"type":"ask_user","task":false,"questions":[{"question":"Which?"}]}`:                 false,
		`{"type":"ask_user","questions":[{"question":"Which?","options":[{"label":"A"}]}]}`:    false,
	} {
		md, ok := ParseMetadata(metadata)
		if !ok {
			t.Fatalf("ParseMetadata(%s) not ok", metadata)
		}
		if md.Task != want {
			t.Errorf("ParseMetadata(%s).Task = %v, want %v", metadata, md.Task, want)
		}
		if len(md.Questions[0].Options) != 0 && want {
			t.Errorf("a task item carries no options")
		}
	}
}

func TestTaskAnswerRoundTripsThroughResponseData(t *testing.T) {
	data, err := BuildResponseData([]Answer{
		TaskAnswer("Sign in", true, ""),
		TaskAnswer("Approve the PR", false, "no access"),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"answers":[{"question":"Sign in","selected":["Done"]},{"question":"Approve the PR","selected":["Not done"],"freetext":"no access"}]}`
	if data != want {
		t.Errorf("response data = %s, want %s", data, want)
	}
}

func TestTaskOutcome(t *testing.T) {
	for _, tc := range []struct {
		answer Answer
		want   string
	}{
		{TaskAnswer("x", true, ""), "The user marked it done."},
		{TaskAnswer("x", true, "took a while"), "The user marked it done (note: took a while)."},
		{TaskAnswer("x", false, "no access"), "The user marked it not done: no access"},
		{TaskAnswer("x", false, " "), "The user marked it not done (no reason given)."},
		{Answer{Question: "x", Freetext: "will do tomorrow"}, "The user replied: will do tomorrow"},
		{Answer{Question: "x", Selected: []string{"Continue"}}, ""},
		{Answer{Question: "x"}, ""},
	} {
		if got := tc.answer.TaskOutcome(); got != tc.want {
			t.Errorf("TaskOutcome(%+v) = %q, want %q", tc.answer, got, tc.want)
		}
	}
}
