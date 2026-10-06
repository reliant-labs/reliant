package runtime

import (
	"github.com/reliant-labs/reliant/internal/db/core"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
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
