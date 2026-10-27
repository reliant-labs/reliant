package core

import "errors"

// ErrAdmissionDeferred leaves the durable event queued, not skipped.
var ErrAdmissionDeferred = errors.New("automation admission deferred")

// TriggerAdmission is execution bookkeeping on an existing trigger event.
// Head is source-observed identity, never evidence of provider freshness.
// EventID is the immutable reservation generation; ChatID is the root Temporal ID.
type TriggerAdmission struct {
	EventID      string `bun:"event_id"`
	Scope        string `bun:"admission_scope"`
	Key          string `bun:"admission_key"`
	Head         string `bun:"admission_head"`
	Order        int64  `bun:"admission_order"`
	State        string `bun:"admission_state"`
	RunID        string `bun:"admission_run_id"`
	ChatID       string `bun:"chat_id"`
	SupersededBy string `bun:"superseded_by"`
}

// TriggerAdmissionRequest is validated intake policy. PendingLimit is server
// configuration, not a workflow-author controlled quota.
type TriggerAdmissionRequest struct {
	Scope, Key, Head string
	PendingLimit     int
}
