// Copyright (c) 2025 Reliant Labs
package antigravity

import (
	"encoding/json"
	"testing"
)

// captureFrame is one SSE frame reproduced from .dev/agy/3.8flash.sse, in the
// pretty-printed shape the real endpoint emits (a bare `data:` line followed by
// the JSON object across several lines). The thought signature is truncated;
// everything structural is byte-for-byte.
const captureFrame = `sse-stream:


data:
{
  "response": {
    "candidates": [
      {
        "content": {
          "role": "model",
          "parts": [
            {
              "text": "Hello again! What would you like to work on today?"
            }
          ]
        }
      }
    ],
    "usageMetadata": {
      "promptTokenCount": 13273,
      "candidatesTokenCount": 12,
      "totalTokenCount": 13313,
      "thoughtsTokenCount": 28
    },
    "modelVersion": "gemini-3.8-flash",
    "responseId": "DXmsaqesBeOq9MoP7ZGD-Qo"
  },
  "traceId": "bcc173eeb447c96b",
  "metadata": {}
}




data:
{
  "response": {
    "candidates": [
      {
        "content": {
          "role": "model",
          "parts": [
            {
              "thoughtSignature": "EvEBCu4BAWkUfRNbbvBlFHw",
              "text": ""
            }
          ]
        },
        "finishReason": "STOP"
      }
    ],
    "usageMetadata": {
      "promptTokenCount": 13318,
      "candidatesTokenCount": 12,
      "totalTokenCount": 13358,
      "thoughtsTokenCount": 28
    },
    "modelVersion": "gemini-3.8-flash",
    "responseId": "DXmsaqesBeOq9MoP7ZGD-Qo"
  },
  "traceId": "bcc173eeb447c96b",
  "metadata": {}
}

`

// TestNaiveSingleEnvelopeParseProducesNothing is the regression that justifies
// this whole package. google.golang.org/genai unmarshals an SSE frame straight
// into GenerateContentResponse — a single-envelope decode. Against an
// Antigravity frame that decode SUCCEEDS and yields an empty struct, so the
// stream looks healthy and produces no content at all. This test pins the
// silent failure: if it ever starts failing because the endpoint stopped
// double-wrapping, the wrapper types can go.
func TestNaiveSingleEnvelopeParseProducesNothing(t *testing.T) {
	// The shape genai expects: GenerateContentResponse at the TOP level.
	type naiveResponse struct {
		Candidates []*candidate   `json:"candidates"`
		Usage      *usageMetadata `json:"usageMetadata"`
	}

	var frames [][]byte
	if err := scanFrames(stringReader(captureFrame), func(raw []byte) error {
		frames = append(frames, append([]byte(nil), raw...))
		return nil
	}); err != nil {
		t.Fatalf("scanFrames: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("expected 2 frames from the capture, got %d", len(frames))
	}

	for i, raw := range frames {
		var naive naiveResponse
		if err := json.Unmarshal(raw, &naive); err != nil {
			t.Fatalf("frame %d: naive parse errored, expected it to succeed-but-be-empty: %v", i, err)
		}
		if len(naive.Candidates) != 0 || naive.Usage != nil {
			t.Fatalf("frame %d: naive single-envelope parse found data; the double envelope is gone", i)
		}

		// The double-envelope parse this package uses finds the real payload.
		var wrapped streamFrame
		if err := json.Unmarshal(raw, &wrapped); err != nil {
			t.Fatalf("frame %d: wrapped parse: %v", i, err)
		}
		if wrapped.Response == nil || len(wrapped.Response.Candidates) == 0 {
			t.Fatalf("frame %d: wrapped parse found no candidates", i)
		}
		if wrapped.TraceID != "bcc173eeb447c96b" {
			t.Fatalf("frame %d: traceId = %q", i, wrapped.TraceID)
		}
	}
}

// TestRequestEnvelopeShape pins the outer request fields against the capture,
// including the one that is easy to get wrong: model lives at the TOP level
// with the effort suffix, and the inner request has no model field at all.
func TestRequestEnvelopeShape(t *testing.T) {
	env := &requestEnvelope{
		Project:     envelopeProject,
		RequestID:   "agent/c/1/t/1",
		Model:       "gemini-3.8-flash-high",
		UserAgent:   envelopeUserAgent,
		RequestType: envelopeRequestType,
		Request: &generateReq{
			Contents:  []*content{{Role: "user", Parts: []*part{{Text: "hello"}}}},
			SessionID: "-3750763034362895579",
		},
	}

	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for key, want := range map[string]string{
		"project":     "aicode-consumers",
		"model":       "gemini-3.8-flash-high",
		"userAgent":   "antigravity",
		"requestType": "agent",
	} {
		if got, _ := decoded[key].(string); got != want {
			t.Errorf("envelope[%q] = %q, want %q", key, got, want)
		}
	}

	inner, ok := decoded["request"].(map[string]any)
	if !ok {
		t.Fatal("envelope has no inner request object")
	}
	if _, present := inner["model"]; present {
		t.Error("inner request carries a model field; the capture puts model only at the top level")
	}
	if sessionID, _ := inner["sessionId"].(string); sessionID != "-3750763034362895579" {
		t.Errorf("sessionId = %v, want the signed-64-bit value as a STRING", inner["sessionId"])
	}
}

func TestModelIDForEffort(t *testing.T) {
	tests := []struct {
		name   string
		base   string
		effort string
		want   string
	}{
		{name: "high suffix", base: "gemini-3.8-flash", effort: "high", want: "gemini-3.8-flash-high"},
		{name: "medium suffix", base: "gemini-3.8-flash", effort: "medium", want: "gemini-3.8-flash-medium"},
		{name: "low suffix", base: "gemini-3.8-flash", effort: "low", want: "gemini-3.8-flash-low"},
		{name: "case insensitive", base: "gemini-3.8-flash", effort: "HIGH", want: "gemini-3.8-flash-high"},
		{name: "no effort", base: "gemini-3.8-flash", effort: "", want: "gemini-3.8-flash"},
		{name: "disabled", base: "gemini-3.8-flash", effort: "disabled", want: "gemini-3.8-flash"},
		{name: "unadvertised level", base: "gemini-3.8-flash", effort: "minimal", want: "gemini-3.8-flash"},
		{name: "base with no variants", base: "gemini-9-future", effort: "high", want: "gemini-9-future"},
		{name: "empty base", base: "", effort: "high", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := modelIDForEffort(tc.base, tc.effort); got != tc.want {
				t.Errorf("modelIDForEffort(%q, %q) = %q, want %q", tc.base, tc.effort, got, tc.want)
			}
		})
	}
}

// TestModelVersionEchoesBaseID documents that the response's modelVersion is
// the BASE id even when the request named a suffixed variant. Asserting
// equality between the two is the mistake this test exists to prevent.
func TestModelVersionEchoesBaseID(t *testing.T) {
	requested := modelIDForEffort("gemini-3.8-flash", "high")
	if requested != "gemini-3.8-flash-high" {
		t.Fatalf("requested = %q", requested)
	}

	var frame streamFrame
	raw := `{"response":{"modelVersion":"gemini-3.8-flash"},"traceId":"t"}`
	if err := json.Unmarshal([]byte(raw), &frame); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if frame.Response.ModelVersion != "gemini-3.8-flash" {
		t.Fatalf("modelVersion = %q, want the base id", frame.Response.ModelVersion)
	}
	if frame.Response.ModelVersion == requested {
		t.Fatal("modelVersion matched the suffixed request id; the suffix convention changed")
	}
}
