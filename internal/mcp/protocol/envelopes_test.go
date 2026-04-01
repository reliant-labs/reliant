package protocol

import "testing"

func TestBuildCompatibilityEnvelopes(t *testing.T) {
	args := map[string]interface{}{"foo": "bar"}
	payloads := BuildCompatibilityEnvelopes(args)
	if len(payloads) != 4 {
		t.Fatalf("expected 4 payloads got %d", len(payloads))
	}

	params0, ok := payloads[0]["params"].(map[string]interface{})
	if !ok || params0["foo"] != "bar" {
		t.Fatalf("payload[0] malformed: %#v", payloads[0])
	}

	params1, ok := payloads[1]["params"].(map[string]interface{})
	if !ok {
		t.Fatalf("payload[1] missing params: %#v", payloads[1])
	}
	json1, ok := params1["application/json"].(map[string]interface{})
	if !ok || json1["foo"] != "bar" {
		t.Fatalf("payload[1] malformed: %#v", payloads[1])
	}
}

func TestHasExplicitEnvelopeShape(t *testing.T) {
	if !HasExplicitEnvelopeShape(map[string]interface{}{"params": map[string]interface{}{}}) {
		t.Fatal("expected params to be explicit envelope")
	}
	if !HasExplicitEnvelopeShape(map[string]interface{}{"application/json": map[string]interface{}{}}) {
		t.Fatal("expected application/json to be explicit envelope")
	}
	if HasExplicitEnvelopeShape(map[string]interface{}{"foo": "bar"}) {
		t.Fatal("did not expect plain args to be explicit envelope")
	}
}
