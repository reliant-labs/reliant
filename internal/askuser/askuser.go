// Copyright (c) 2025 Reliant Labs

// Package askuser parses the `ask_user` question metadata a workflow persists
// on a pending question and constructs the ResolveQuestion response_data
// payload. It is the Go counterpart of web/src/components/Chat/askUserUtils.ts
// and MUST agree with it: the two produce/consume the same wire shapes.
//
// Metadata shape (a question's Metadata JSON):
//
//	{
//	  "type": "ask_user",
//	  "tool_call_id": "call_abc",
//	  "questions": [
//	    {"question": "Which approach?",
//	     "options": [{"label": "A", "description": "…", "preview": "…"}],
//	     "allow_multiple": false}
//	  ]
//	}
//
// The backend has been observed to persist `questions` as a double-encoded
// JSON string, or to wrap the payload in an older envelope where the questions
// live inside a JSON-encoded `input` field — this package tolerates both, just
// like the web parser.
//
// Response shape (ResolveQuestion.response_data, action "reply"):
//
//	{"answers": [{"question": "<exact text>",
//	              "selected": ["<exact label>"],
//	              "freetext": "…"}]}
//
// forge:exclude-contract
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
package askuser

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Option is one selectable choice for a sub-question.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Preview     string `json:"preview,omitempty"`
}

// Question is a single sub-question within an ask_user payload.
type Question struct {
	Question      string   `json:"question"`
	Options       []Option `json:"options"`
	AllowMultiple bool     `json:"allow_multiple,omitempty"`
}

// Metadata is a parsed ask_user question, bundling one or more sub-questions.
type Metadata struct {
	Type       string     `json:"type"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Questions  []Question `json:"questions"`
}

// Answer is one resolved sub-question in a response_data payload.
type Answer struct {
	Question string   `json:"question"`
	Selected []string `json:"selected"`
	Freetext string   `json:"freetext,omitempty"`
}

// responseEnvelope is the top-level response_data shape.
type responseEnvelope struct {
	Answers []Answer `json:"answers"`
}

// ParseMetadata parses an ask_user question's Metadata JSON into a structured
// form. It returns (nil, false) when the metadata is empty, not JSON, not an
// ask_user question, or carries no valid sub-questions. This mirrors
// parseAskUserMetadata in the web client, including recovery of a
// double-encoded `questions` string and the legacy `input` envelope.
func ParseMetadata(metadata string) (*Metadata, bool) {
	if strings.TrimSpace(metadata) == "" {
		return nil, false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(metadata), &raw); err != nil {
		return nil, false
	}
	if typeString(raw["type"]) != "ask_user" {
		return nil, false
	}

	md := &Metadata{Type: "ask_user", ToolCallID: typeString(raw["tool_call_id"])}

	// New format: questions at the top level.
	if qs := normalizeQuestions(raw["questions"]); len(qs) > 0 {
		md.Questions = qs
		return md, true
	}

	// Legacy envelope: questions nested inside a JSON-encoded "input" string.
	if inner := typeString(raw["input"]); inner != "" {
		var innerRaw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(inner), &innerRaw); err == nil {
			if qs := normalizeQuestions(innerRaw["questions"]); len(qs) > 0 {
				md.Questions = qs
				return md, true
			}
		}
	}

	return nil, false
}

// IsAskUser reports whether the metadata is an ask_user question (regardless of
// whether it carries usable sub-questions).
func IsAskUser(metadata string) bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(metadata), &raw); err != nil {
		return false
	}
	return typeString(raw["type"]) == "ask_user"
}

// normalizeQuestions coerces a raw `questions` value into a validated slice.
// The value may be an array, or a JSON string that decodes to an array
// (double-encoded case). Only objects with a non-empty string `question` field
// survive; a missing/invalid `options` is coerced to an empty slice.
func normalizeQuestions(raw json.RawMessage) []Question {
	if len(raw) == 0 {
		return nil
	}

	// Double-encoded: the value is a JSON string that itself holds the array.
	if s := typeString(raw); s != "" {
		raw = json.RawMessage(s)
	}

	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}

	out := make([]Question, 0, len(arr))
	for _, entry := range arr {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(entry, &obj); err != nil {
			continue
		}
		qText := typeString(obj["question"])
		if qText == "" {
			continue
		}
		q := Question{Question: qText, Options: []Option{}}
		if b, ok := obj["allow_multiple"]; ok {
			_ = json.Unmarshal(b, &q.AllowMultiple)
		}
		if opts := obj["options"]; len(opts) > 0 {
			var parsed []Option
			if err := json.Unmarshal(opts, &parsed); err == nil {
				q.Options = parsed
			}
		}
		out = append(out, q)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// typeString returns the string value of a raw JSON message when it is a JSON
// string, else "".
func typeString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// BuildResponseData serializes answers into the ResolveQuestion response_data
// JSON: {"answers":[{"question","selected","freetext"}]}. selected is always
// emitted (as [] when empty) to match the web client's shape.
func BuildResponseData(answers []Answer) (string, error) {
	for i := range answers {
		if answers[i].Selected == nil {
			answers[i].Selected = []string{}
		}
	}
	b, err := json.Marshal(responseEnvelope{Answers: answers})
	if err != nil {
		return "", fmt.Errorf("encoding response_data: %w", err)
	}
	return string(b), nil
}

// OptionLabels returns the option labels of a sub-question, in order.
func (q Question) OptionLabels() []string {
	labels := make([]string, len(q.Options))
	for i, o := range q.Options {
		labels[i] = o.Label
	}
	return labels
}

// MatchOption returns the option whose label equals want (case-sensitive
// first, then case-insensitive fallback). ok is false when no option matches.
func (q Question) MatchOption(want string) (Option, bool) {
	for _, o := range q.Options {
		if o.Label == want {
			return o, true
		}
	}
	for _, o := range q.Options {
		if strings.EqualFold(o.Label, want) {
			return o, true
		}
	}
	return Option{}, false
}
