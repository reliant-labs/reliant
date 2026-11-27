package runtime

import (
	"github.com/reliant-labs/reliant/internal/db/core"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// TriggerInfo is the event that launched a chat, as the engine sees it. It is
// fixed at launch and never changes for the life of the chat.
type TriggerInfo struct {
	Kind       string         `json:"kind"`
	TriggerID  string         `json:"trigger_id,omitempty"`
	EventID    string         `json:"event_id,omitempty"`
	OccurredAt string         `json:"occurred_at,omitempty"` // RFC3339
	Payload    map[string]any `json:"payload,omitempty"`
	// Sender is who the event came from, as its source authenticated it;
	// nil for a start a person made themselves.
	Sender *core.TriggerSender `json:"sender,omitempty"`
	// Origin says whether Reliant caused the event and which run did; nil reads
	// as "not caused by Reliant" (OriginCELValue).
	Origin *core.TriggerOrigin `json:"origin,omitempty"`
	// TraceParent is the W3C traceparent of the span that fired an
	// unattended launch. Such a run is a trace of its own, not a child of
	// that span (one event fans out to several orgs' triggers), and links to
	// it instead (startRunTelemetry). Empty for an attended start, whose run
	// stays in its caller's trace. Telemetry only: never in CELValue.
	TraceParent string `json:"trace_parent,omitempty"`
}

// CELValue is the `trigger` namespace: the envelope fields plus `name` and
// `scheduled_for` lifted from the payload, and `sender`. Every key is always
// present (empty string when not applicable, `sender.verified` false) so a
// template or filter never fails with "no such key".
func (t *TriggerInfo) CELValue() map[string]interface{} {
	payload := make(map[string]interface{}, len(t.Payload))
	for key, value := range t.Payload {
		payload[key] = value
	}
	scheduledFor := t.OccurredAt
	if value, ok := payload["scheduled_for"].(string); ok && value != "" {
		scheduledFor = value
	}
	name, _ := payload["trigger_name"].(string)
	return map[string]interface{}{
		"kind":          t.Kind,
		"trigger_id":    t.TriggerID,
		"event_id":      t.EventID,
		"occurred_at":   t.OccurredAt,
		"scheduled_for": scheduledFor,
		"name":          name,
		"payload":       payload,
		"sender":        SenderCELValue(t.Sender),
		"origin":        OriginCELValue(t.Origin),
		"record":        RecordCELValue(t.Kind, t.Payload),
	}
}

// RecordKindDue is the trigger kind of a record_due firing
// (core.TriggerEventKindRecordDue).
const RecordKindDue = "record_due"

// RecordCELValue is `trigger.record`: the record an event is about. Every key
// is always present, empty (0 for number) when the event names no record, so
// a template that reads it never fails with "no such key".
//
//   - A record_due firing is about one due record: its payload says which
//     (table, record_id, repository, number, head_sha, next_action, due_at).
//   - A GitHub pull request event (an integration event whose data carries a
//     pull_request, or an issue comment on a pull request) is about that
//     PR: repository, number and head_sha come from its payload. id,
//     next_action and due_at stay empty: they are the PR pipeline's, and
//     reading them would take a database read at fire time.
//
// So `{{ trigger.record.repository }}#{{ trigger.record.number }}` names the
// same PR whichever of the two fired, which a concurrency key relies on.
func RecordCELValue(kind string, payload map[string]any) map[string]any {
	out := map[string]any{
		"id": "", "table": "", "repository": "", "number": int64(0),
		"head_sha": "", "next_action": "", "due_at": "",
	}
	switch kind {
	case RecordKindDue:
		for _, key := range []string{"table", "repository", "head_sha", "next_action", "due_at"} {
			if v, ok := payload[key].(string); ok {
				out[key] = v
			}
		}
		if v, ok := payload["record_id"].(string); ok {
			out["id"] = v
		}
		if n, ok := recordNumber(payload["number"]); ok {
			out["number"] = n
		}
	case "integration":
		data, _ := payload["data"].(map[string]any)
		repository := recordAttribute(payload["attributes"], "repository")
		if data == nil || repository == "" {
			return out
		}
		if pr, ok := data["pull_request"].(map[string]any); ok {
			if n, ok := recordNumber(pr["number"]); ok {
				out["repository"], out["number"] = repository, n
				if head, ok := pr["head"].(map[string]any); ok {
					if sha, ok := head["sha"].(string); ok {
						out["head_sha"] = sha
					}
				}
			}
		} else if issue, ok := data["issue"].(map[string]any); ok && recordAttribute(payload["attributes"], "is_pull_request") == "true" {
			if n, ok := recordNumber(issue["number"]); ok {
				out["repository"], out["number"] = repository, n
			}
		}
	}
	return out
}

// recordAttribute reads one routing attribute of an integration payload;
// attributes are strings, held as map[string]string in memory and
// map[string]any once stored.
func recordAttribute(attrs any, key string) string {
	switch m := attrs.(type) {
	case map[string]string:
		return m[key]
	case map[string]any:
		s, _ := m[key].(string)
		return s
	}
	return ""
}

// recordNumber reads a positive whole number, as held in memory (int,
// int64) or read back from storage (float64).
func recordNumber(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), n > 0
	case int64:
		return n, n > 0
	case float64:
		return int64(n), n > 0 && n == float64(int64(n))
	}
	return 0, false
}

// OriginCELValue is `trigger.origin`. Every key is always present, so a filter
// never fails with "no such key" and `trigger.origin.same_workflow && ...` is
// simply false for an event nothing attributed. A nil origin is the
// unattributed one.
//
// The run's own ids are filled only when the run belongs to the trigger's own
// owner (core.TriggerOrigin.Foreign): another user's trigger sees that Reliant
// caused the event and nothing about whose run.
func OriginCELValue(o *core.TriggerOrigin) map[string]interface{} {
	if o == nil {
		o = &core.TriggerOrigin{Confidence: core.OriginNone, Actor: core.OriginActorUnknown}
	}
	confidence, actor := string(o.Confidence), o.Actor
	if confidence == "" {
		confidence = string(core.OriginNone)
	}
	if actor == "" {
		actor = core.OriginActorUnknown
	}
	return map[string]interface{}{
		"caused_by_reliant":    o.CausedByReliant,
		"confidence":           confidence,
		"run_id":               o.RunID,
		model.NodeTypeWorkflow: o.Workflow,
		"trigger_name":         o.TriggerName,
		"same_workflow":        o.SameWorkflow,
		"actor":                actor,
		"lineage_depth":        int64(o.LineageDepth()),
	}
}

// SenderCELValue is `trigger.sender`. A nil sender is the empty, unverified
// one, so `trigger.sender.verified && ...` is false rather than an error.
func SenderCELValue(sender *core.TriggerSender) map[string]interface{} {
	if sender == nil {
		sender = &core.TriggerSender{}
	}
	return map[string]interface{}{
		"kind":         string(sender.Kind),
		"id":           sender.ID,
		"display_name": sender.DisplayName,
		"verified":     sender.Verified,
	}
}

// propagateTrigger hands the parent's launch event to a child input map, so
// `trigger` means the same thing in every sub-workflow of the run.
func propagateTrigger(parent, child map[string]interface{}) {
	if child == nil {
		return
	}
	if t, ok := parent[wfcel.TriggerInputKey]; ok {
		child[wfcel.TriggerInputKey] = t
	}
}

// propagateFailure hands a terminal handler's failure (and a finally
// handler's run ending) to the bodies nested
// inside it (a loop, an inline workflow), so `failure` means the same thing in
// every node of the handler. Absent in a run's main graph, where there is none.
func propagateFailure(parent, child map[string]interface{}) {
	if child == nil {
		return
	}
	if f, ok := parent[wfcel.FailureInputKey]; ok {
		child[wfcel.FailureInputKey] = f
	}
	// A finally handler's `run`, likewise.
	if r, ok := parent[wfcel.RunInputKey]; ok {
		child[wfcel.RunInputKey] = r
	}
}
