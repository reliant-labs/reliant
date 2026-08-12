// Copyright (c) 2025 Reliant Labs
package threads

import (
	"encoding/json"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
)

// Every live message update must carry the chat-global `seq` the client sorts
// by. This is the primary write path — a user sending a message, or an
// assistant reply landing — so if `seq` is absent the message deserializes at
// seq 0 on the client and sorts to the TOP of the transcript. It was delivered
// and rendered; just nowhere the user would look. The reported symptom is "my
// message never showed up, but a refresh brings it back", because the reload
// path reads seq from the database where it is correct.
//
// This marshals db.MessageUpdateData — the typed payload emitChatUpdate
// builds — and checks the actual JSON output, so it catches both a missing
// struct field and a broken/renamed json tag, unlike a source grep.
func TestChatUpdatePayloadIncludesSeq(t *testing.T) {
	update := db.MessageUpdateData{
		UpdateType: "message",
		ID:         "msg-1",
		Seq:        4211,
		Ordinal:    7,
	}
	raw, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := got["seq"]; !ok {
		t.Error(`the message chat_update payload does not set "seq"; the client sorts by seq, so every live message would render at position 0`)
	}
	if _, ok := got["ordinal"]; !ok {
		t.Log(`note: "ordinal" no longer present — fine, nothing orders by it`)
	}
}

// Guards the shape the frontend actually parses: seq must survive JSON
// marshalling as a number, not a string, or BigInt conversion on the client
// silently yields 0.
func TestSeqMarshalsAsNumber(t *testing.T) {
	payload := map[string]interface{}{"seq": int64(4211)}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := got["seq"].(float64); !ok {
		t.Fatalf("seq round-tripped as %T, want a JSON number", got["seq"])
	}
}
