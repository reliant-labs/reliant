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

// The bookkeeping inputs (bookkeeping.go) were maps too. Their JSON is not
// byte-for-byte the old map's — an empty catalog_pin and a zero
// loop_iteration are now omitted, an empty project_id is now sent — so what
// is pinned is what the handler sees: the old map and the new struct decode
// to the same value.
func TestBookkeepingInputsDecodeAsBefore(t *testing.T) {
	decode := func(t *testing.T, v, into interface{}) {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := json.Unmarshal(b, into); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
	}
	same := func(t *testing.T, name string, old, now interface{}) {
		t.Helper()
		if !reflect.DeepEqual(old, now) {
			t.Errorf("%s decodes differently:\n old=%+v\n new=%+v", name, old, now)
		}
	}

	var loadOld, loadNew LoadWorkflowInput
	decode(t, map[string]string{"chat_id": "c1", "workflow_name": "builtin://agent", "catalog_pin": ""}, &loadOld)
	decode(t, LoadWorkflowInput{ChatID: "c1", WorkflowName: "builtin://agent"}, &loadNew)
	same(t, "LoadWorkflowInput", loadOld, loadNew)

	var presetOld, presetNew LoadPresetParamsInput
	decode(t, map[string]interface{}{"project_path": "/p", "chat_id": "c1", "preset_name": "fast", "catalog_pin": "pin"}, &presetOld)
	decode(t, LoadPresetParamsInput{ProjectPath: "/p", ChatID: "c1", PresetName: "fast", CatalogPin: "pin"}, &presetNew)
	same(t, "LoadPresetParamsInput", presetOld, presetNew)

	var cpOld, cpNew WorkflowCheckpointInput
	decode(t, map[string]interface{}{"chat_id": "c1", "workflow_id": "w1", "node_id": "loop", "loop_iteration": 0}, &cpOld)
	decode(t, WorkflowCheckpointInput{ChatID: "c1", WorkflowID: "w1", NodeID: "loop"}, &cpNew)
	same(t, "WorkflowCheckpointInput", cpOld, cpNew)
}
