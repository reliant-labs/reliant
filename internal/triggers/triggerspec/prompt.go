// Copyright (c) 2025 Reliant Labs
package triggerspec

import (
	"encoding/json"
	"fmt"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
)

// A declared trigger's `prompt` is the message each run it starts begins
// from, written by the workflow's author once rather than by every person who
// activates it. It is a template over the `trigger` root, like an inputs
// value:
//
//	prompt: "Triage issue #{{ trigger.payload.data.issue.number }}: {{ trigger.payload.data.issue.title }}"
//
// An activation's own message overrides it. Whatever the template renders,
// the fire path still appends the event itself as labelled, untrusted data:
// the template only decides how the request is worded.

// PromptError reports a prompt template that cannot run.
type PromptError struct{ Reason string }

func (e *PromptError) Error() string { return "prompt: " + e.Reason }

// CompilePrompt checks that every expression in prompt compiles over the
// `trigger` root. A prompt with no {{ }} is plain text and always compiles.
func CompilePrompt(prompt string) error {
	exprs := InputExpressions(prompt)
	if len(exprs) == 0 {
		return nil
	}
	env, err := triggerEnv()
	if err != nil {
		return &PromptError{Reason: "build environment: " + err.Error()}
	}
	for _, expr := range exprs {
		if _, issues := env.Compile(expr); issues != nil && issues.Err() != nil {
			return &PromptError{Reason: fmt.Sprintf("{{ %s }}: %v", expr, issues.Err())}
		}
	}
	return nil
}

// RenderPrompt resolves prompt against a `trigger` root. A value an
// expression yields that is not a string (a number, a list) is written as
// JSON. An evaluation error — a key the payload lacks — fails the render: a
// run seeded with half a sentence is worse than recording why it did not
// start.
func RenderPrompt(prompt string, root map[string]any) (string, error) {
	if len(InputExpressions(prompt)) == 0 {
		return prompt, nil
	}
	value, err := wfcel.EvaluateTemplate(prompt, &triggerEvalContext{root: root})
	if err != nil {
		return "", &PromptError{Reason: err.Error() + "; guard optional fields with has() or a ternary"}
	}
	switch v := value.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(v), nil
		}
		return string(raw), nil
	}
}
