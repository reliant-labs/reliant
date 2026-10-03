package temporal

import (
	"testing"

	commonpb "go.temporal.io/api/common/v1"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// A payload recorded before a proto field was retired must still decode into
// the typed message. Workflow code that reads an activity result into a proto
// (router_executor reads CallLLM into reliantv1.CallLLMOutput) re-decodes the
// RECORDED payload on every replay, and a strict decoder rejects the retired
// field — the workflow task fails non-deterministically (TMPRL1100) and the
// in-flight run wedges on deploy. Reserving a field number is the documented
// way to retire it; that only works if old payloads carrying it still decode.
//
// Observed when CallLLMOutput's aborted/stop_kind were folded into stop_reason:
// replaytest's router_dispatch fixture, whose recorded CallLLM result carries
// "stop_kind":"complete", stopped replaying.
func TestFlexibleDataConverter_DecodesRetiredProtoFields(t *testing.T) {
	t.Parallel()
	dc := NewFlexibleDataConverter()

	payload := &commonpb.Payload{
		Metadata: map[string][]byte{
			"encoding":    []byte("json/protobuf"),
			"messageType": []byte("reliant.v1.CallLLMOutput"),
		},
		Data: []byte(`{"response_text":"The work is done.","stop_kind":"complete","aborted":false,"finish_reason":"end_turn"}`),
	}

	var out reliantv1.CallLLMOutput
	if err := dc.FromPayload(payload, &out); err != nil {
		t.Fatalf("a payload carrying a retired field must decode, got: %v", err)
	}
	if out.GetResponseText() != "The work is done." || out.GetFinishReason() != "end_turn" {
		t.Fatalf("known fields must survive alongside retired ones, got %+v", &out)
	}
}
