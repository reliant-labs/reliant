// Copyright (c) 2025 Reliant Labs
package responseswire

import (
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
)

func TestGateToolCalls(t *testing.T) {
	const twoCalls = `"output":[` +
		`{"type":"function_call","id":"fc_done","call_id":"call_done","name":"read","arguments":"{}","status":"completed"},` +
		`{"type":"function_call","id":"fc_half","call_id":"call_half","name":"read","arguments":"{\"pa","status":"in_progress"},` +
		`{"type":"function_call","call_id":"call_noid","name":"read","arguments":"{}","status":"completed"}]`

	tests := []struct {
		name  string
		raw   string // "" means a nil response
		allow map[string]bool
	}{
		{
			name:  "completed allows every call",
			raw:   `{"id":"r","status":"completed",` + twoCalls + `}`,
			allow: map[string]bool{"fc_done": true, "fc_half": true, "call_noid": true, "fc_streamed_only": true},
		},
		{
			name:  "missing status reads as completed",
			raw:   `{"id":"r",` + twoCalls + `}`,
			allow: map[string]bool{"fc_done": true, "fc_half": true},
		},
		{
			name:  "incomplete allows only items it lists as completed",
			raw:   `{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},` + twoCalls + `}`,
			allow: map[string]bool{"fc_done": true, "fc_half": false, "call_noid": true, "fc_streamed_only": false},
		},
		{
			name:  "failed allows nothing",
			raw:   `{"id":"r","status":"failed",` + twoCalls + `}`,
			allow: map[string]bool{"fc_done": false, "fc_half": false, "call_noid": false},
		},
		{
			name:  "cancelled allows nothing",
			raw:   `{"id":"r","status":"cancelled",` + twoCalls + `}`,
			allow: map[string]bool{"fc_done": false, "fc_half": false},
		},
		{
			name:  "nil response allows nothing",
			allow: map[string]bool{"fc_done": false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := GateToolCalls(nil)
			if tt.raw != "" {
				gate = GateToolCalls(decode(t, tt.raw))
			}
			for key, want := range tt.allow {
				if got := gate.Allows(key); got != want {
					t.Errorf("Allows(%q) = %v, want %v", key, got, want)
				}
			}
		})
	}
}

func TestErrStreamEndedWithoutTerminalEventIsTheTransientClass(t *testing.T) {
	if !errors.Is(ErrStreamEndedWithoutTerminalEvent, drivererrors.ErrStreamEndedEarly) {
		t.Fatal("ErrStreamEndedWithoutTerminalEvent must match drivererrors.ErrStreamEndedEarly, which the runtime retries")
	}
}
