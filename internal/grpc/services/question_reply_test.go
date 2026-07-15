// Copyright (c) 2025 Reliant Labs
package services

import "testing"

// formatAnswersReply must mirror parseQuestionResponse's feedback semantics
// (inline_workflow_executor.go): bare Continue = no feedback = no message.
func TestFormatAnswersReply(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
	}{
		{"freetext feedback", `{"answers":[{"question":"Q1","selected":[],"freetext":"use dark mode"}]}`, "use dark mode"},
		{"continue only is not feedback", `{"answers":[{"question":"Q1","selected":["Continue"],"freetext":""}]}`, ""},
		{"selection feedback", `{"answers":[{"question":"Q1","selected":["Option B"],"freetext":""}]}`, "Option B"},
		{"continue plus freetext keeps freetext", `{"answers":[{"question":"Q1","selected":["Continue"],"freetext":"but fix the header"}]}`, "but fix the header"},
		{"multiple questions labeled", `{"answers":[{"question":"Color","selected":["Blue"],"freetext":""},{"question":"Tone","selected":[],"freetext":"playful"}]}`, "Color: Blue\nTone: playful"},
		{"empty answers", `{"answers":[]}`, ""},
		{"not the answers shape", `{"foo":"bar"}`, ""},
		{"invalid json", `not json`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatAnswersReply(tc.data); got != tc.want {
				t.Errorf("formatAnswersReply(%s) = %q, want %q", tc.data, got, tc.want)
			}
		})
	}
}
