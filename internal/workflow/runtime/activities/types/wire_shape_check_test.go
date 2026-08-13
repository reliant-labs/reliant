package types

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The regular-activity dispatch path — still taken by every history recorded
// before the local-activity change — previously sent these inputs as
// map[string]interface{} and now sends them as structs. The serialized JSON
// must carry the same field names and values, or an in-flight run that
// replays its recorded regular-activity command would hand the handler a
// different payload than the one the history recorded.
//
// Compared as decoded maps, not as bytes: encoding/json emits map keys sorted
// and struct fields in declaration order, so the byte strings differ by key
// order alone. JSON decoding is order-independent, which makes that difference
// invisible to the handler and irrelevant to replay.
func TestWireShapeUnchanged(t *testing.T) {
	asMap := func(t *testing.T, v interface{}) map[string]interface{} {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return m
	}

	drainOld := asMap(t, map[string]interface{}{"chat_id": "c1", "thread": "t1"})
	drainNew := asMap(t, DrainAgentMessagesInput{ChatID: "c1", Thread: "t1"})
	if !reflect.DeepEqual(drainOld, drainNew) {
		t.Errorf("drain wire shape changed:\n old=%v\n new=%v", drainOld, drainNew)
	}

	finOld := asMap(t, map[string]interface{}{
		"chat_id": "c1", "last_stream_seq": 42, "message_id": "m1",
		"reason": "completed", "thread": "t1",
	})
	finNew := asMap(t, EmitStreamFinalizedInput{
		ChatID: "c1", MessageID: "m1", Thread: "t1", Reason: "completed", LastStreamSeq: 42,
	})
	if !reflect.DeepEqual(finOld, finNew) {
		t.Errorf("finalize wire shape changed:\n old=%v\n new=%v", finOld, finNew)
	}

	// omitempty on last_stream_seq must match the old map, which simply
	// omitted the key when the workflow had no sequence to report.
	finNoSeq := asMap(t, EmitStreamFinalizedInput{
		ChatID: "c1", MessageID: "m1", Thread: "t1", Reason: "aborted",
	})
	if _, present := finNoSeq["last_stream_seq"]; present {
		t.Errorf("last_stream_seq must be omitted when zero, got %v", finNoSeq)
	}
}
