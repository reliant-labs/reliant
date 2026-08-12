package db

import (
	"encoding/json"
	"testing"
)

// The frontend parses this with protobuf-es fromJsonString(MessageSchema,...).
// A missing or renamed key is silent on the client — that exact failure shipped
// once already when `ordinal` became `seq` and one writer was missed. Pin the
// wire contract so a rename breaks here instead of in a user's transcript.
func TestMessageUpdateWireShape(t *testing.T) {
	cs := 0
	atts := []map[string]interface{}{}
	got, err := MessageUpdateData{
		UpdateType: "message", ID: "m1", Role: int32(1), Seq: 4211, Ordinal: 622,
		Thread: "t1", ContentBlocks: []map[string]interface{}{},
		CreatedAt: "2026-08-02T10:00:00Z", UpdatedAt: "2026-08-02T10:00:00Z",
		ContextWindowID: "cw1", StreamingState: "complete",
		ContextSequence: &cs, Attachments: &atts,
	}.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"update_type", "id", "role", "seq", "ordinal", "thread",
		"content_blocks", "created_at", "updated_at", "context_window_id",
		"streaming_state", "context_sequence", "attachments"} {
		if _, ok := m[k]; !ok {
			t.Errorf("wire payload missing key %q", k)
		}
	}
	if v, ok := m["seq"].(float64); !ok || int64(v) != 4211 {
		t.Errorf("seq = %v, want numeric 4211", m["seq"])
	}
}
