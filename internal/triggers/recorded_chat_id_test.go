// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// A recorded event names a chat only when it launched one or will: a skipped
// or failed event never has a chat, and the id is never fabricated for it.
func TestRecordedEventChatID(t *testing.T) {
	launched := "chat-recorded"
	row := func(outcome core.TriggerEventOutcome, chat *string) *core.TriggerEvent {
		return &core.TriggerEvent{Kind: core.TriggerEventKindWebhook, DedupeKey: "t:key:1", Outcome: outcome, ChatID: chat}
	}
	assert.Equal(t, EventChatID(core.TriggerEventKindWebhook, "t:key:1"), RecordedEventChatID(row(core.TriggerEventPending, nil)))
	assert.Equal(t, launched, RecordedEventChatID(row(core.TriggerEventLaunched, &launched)))
	assert.Empty(t, RecordedEventChatID(row(core.TriggerEventSkipped, nil)))
	assert.Empty(t, RecordedEventChatID(row(core.TriggerEventFailed, nil)))
}
