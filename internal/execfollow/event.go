// Copyright (c) 2025 Reliant Labs

package execfollow

import (
	"encoding/json"
	"fmt"
	"strings"
)

// HookEventAny matches every event.
const HookEventAny = "any"

// hookableEvents is the set of event types a --hook may bind to (plus "any").
// Deliberately a subset of emitted events: workflow_started and
// workflow_cancelled are observable via "any" and the NDJSON stream.
var hookableEvents = map[string]bool{
	EventNodeStarted:       true,
	EventNodeCompleted:     true,
	EventNodeFailed:        true,
	EventNodeSkipped:       true,
	EventWorkflowCompleted: true,
	EventWorkflowFailed:    true,
	EventQuestion:          true,
	EventQuestionAnswered:  true,
	EventApproval:          true,
	EventApprovalResolved:  true,
	HookEventAny:           true,
}

// Hook binds a shell command to an event type, from the --hook flag.
type Hook struct {
	On  string
	Cmd string
}

// Matches reports whether the hook fires for the given event type.
func (h Hook) Matches(eventType string) bool {
	return h.On == HookEventAny || h.On == eventType
}

// ValidateHook checks the event name and command of a hook (from flag or
// config file).
func ValidateHook(h Hook) error {
	if !hookableEvents[h.On] {
		return fmt.Errorf("invalid hook event %q — expected one of: node_started, node_completed, node_failed, workflow_completed, workflow_failed, question, question_answered, approval, approval_resolved, any", h.On)
	}
	if strings.TrimSpace(h.Cmd) == "" {
		return fmt.Errorf("hook command must not be empty")
	}
	return nil
}

// ParseHookFlag parses a repeatable --hook value of the form:
//
//	on=<event> cmd=<shell command>
//
// Everything after "cmd=" (including spaces and '=') is the command.
func ParseHookFlag(raw string) (Hook, error) {
	s := strings.TrimSpace(raw)
	if !strings.HasPrefix(s, "on=") {
		return Hook{}, fmt.Errorf("invalid --hook %q — expected 'on=<event> cmd=<shell>'", raw)
	}
	rest := s[len("on="):]
	idx := strings.IndexAny(rest, " \t")
	if idx < 0 {
		return Hook{}, fmt.Errorf("invalid --hook %q — missing 'cmd=' part", raw)
	}
	event := rest[:idx]
	cmdPart := strings.TrimLeft(rest[idx:], " \t")
	if !strings.HasPrefix(cmdPart, "cmd=") {
		return Hook{}, fmt.Errorf("invalid --hook %q — expected 'cmd=' after the event", raw)
	}
	h := Hook{On: event, Cmd: cmdPart[len("cmd="):]}
	if err := ValidateHook(h); err != nil {
		return Hook{}, err
	}
	return h, nil
}

// marshalEvent renders the single-line JSON form of an event: what a hook
// receives on stdin.
func marshalEvent(ev Event) ([]byte, error) {
	return json.Marshal(ev)
}
