package streaming

import (
	"testing"
)

type testUpdate struct {
	ID   string `json:"id"`
	Data string `json:"data"`
}

// --- UpdateEvent JSON round-trip ---

func TestUpdateEvent_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	original := UpdateEvent[testUpdate]{
		Key:            "user-123",
		SequenceNumber: 42,
		Payload:        testUpdate{ID: "u1", Data: "hello"},
	}

	data, err := original.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON failed: %v", err)
	}

	var decoded UpdateEvent[testUpdate]
	if err := decoded.UnmarshalJSON(data); err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}

	if decoded.Key != original.Key {
		t.Errorf("Key: got %q, want %q", decoded.Key, original.Key)
	}
	if decoded.SequenceNumber != original.SequenceNumber {
		t.Errorf("SequenceNumber: got %d, want %d", decoded.SequenceNumber, original.SequenceNumber)
	}
	if decoded.Payload.ID != original.Payload.ID {
		t.Errorf("Payload.ID: got %q, want %q", decoded.Payload.ID, original.Payload.ID)
	}
	if decoded.Payload.Data != original.Payload.Data {
		t.Errorf("Payload.Data: got %q, want %q", decoded.Payload.Data, original.Payload.Data)
	}
}
