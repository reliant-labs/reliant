// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventFireWorkflowCorrelation(t *testing.T) {
	for _, tc := range []struct {
		kind core.TriggerEventKind
		want string
	}{
		{core.TriggerEventKindSchedule, "trigger-fire-t1-manual-fire"},
		{core.TriggerEventKindWebhook, EventFireWorkflowID("event-1")},
		{core.TriggerEventKindIntegration, EventFireWorkflowID("event-1")},
		{core.TriggerEventKindWorkflowEvent, EventFireWorkflowID("event-1")},
		{core.TriggerEventKindChatStart, ""},
		{core.TriggerEventKindAgentStartRun, ""},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			ev, err := EventToProto(&core.TriggerEvent{ID: "event-1", Kind: tc.kind, DedupeKey: "trigger-fire-t1-manual-fire"})
			require.NoError(t, err)
			assert.Equal(t, tc.want, ev.GetFireWorkflowId())
		})
	}
}
