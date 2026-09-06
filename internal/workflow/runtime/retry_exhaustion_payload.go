// Copyright (c) 2025 Reliant Labs
package runtime

// retryExhaustionError describes one exhausted retry ladder, in the shape the
// WorkflowError activity expects.
//
// The three executors that can exhaust a ladder — the main loop, the inline
// loop, and the inline sub-workflow — each used to build this map inline. They
// drifted, and the drift WAS the bug: the main loop stamped `thread` even when
// it was empty (an error with an empty thread renders in every thread of the
// chat), while none of the three set an id, so each exhaustion minted a fresh
// uuid that could not dedup against the row the failing activity had already
// written one attempt earlier. One builder, three callers, so a fix lands once.
type retryExhaustionError struct {
	ChatID       string
	WorkflowID   string
	WorkflowName string
	// Message is the user-facing error text.
	Message string
	// Thread the failure happened on, or "" when genuinely unknown.
	Thread string
	// Summary, when non-empty, is the clean one-line explanation.
	Summary string
	// Err is the Temporal error that ended the ladder. It carries the activity
	// id this error must be keyed on — see exhaustionErrorEventID.
	Err error
}

// payload renders the WorkflowError activity input.
//
// Two fields are conditional, and both are conditional for the same reason: an
// absent field means "unknown", and a present one is an assertion. Neither may
// be filled with a plausible guess.
//   - thread: an error that names no thread is chat-scoped and renders in ALL
//     of them (deliberate, for legacy rows that predate thread attribution). So
//     omitting it when the thread IS known is a bug that shows the error
//     everywhere, and defaulting it to the chat id would be a wrong assertion.
//   - error_id: set only when the failure came from an activity, so this row
//     replaces that activity's error row instead of stacking beside it. With no
//     activity to key on, WriteWorkflowError mints a uuid, which is correct for
//     an error that is its own event.
func (e retryExhaustionError) payload() map[string]interface{} {
	p := map[string]interface{}{
		"chat_id":       e.ChatID,
		"workflow_id":   e.WorkflowID,
		"workflow_name": e.WorkflowName,
		"error_message": e.Message,
		"error_type":    "retry_exhaustion",
	}
	if e.Thread != "" {
		p["thread"] = e.Thread
	}
	if errorID := exhaustionErrorEventID(e.WorkflowID, e.Err); errorID != "" {
		p["error_id"] = errorID
	}
	if e.Summary != "" {
		p["error_summary"] = e.Summary
	}
	return p
}
