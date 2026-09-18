// Copyright (c) 2025 Reliant Labs
package imagegen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/llm/drivers/agywire"
)

// syntheticToken stands in for the ya29… access token everywhere in this file.
// The real capture's bearer is a live credential and must never appear in
// source, a fixture, or a log line.
const syntheticToken = "ya29.synthetic-test-token"

// onePixelJPEG is the smallest thing that sniffs as image/jpeg. The capture's
// real payload is a full JPEG; only its leading bytes matter to any assertion
// here, so this reproduces the format without carrying 1.5MB of fixture.
var onePixelJPEG = []byte{
	0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01,
	0x01, 0x01, 0x01, 0x2C, 0x01, 0x2C, 0x00, 0x00, 0xFF, 0xD9,
}

// captureBody reproduces the recorded image-gen response from
// daily-cloudcode-pa.googleapis.com/v1internal:generateContent. Structure is
// byte-for-byte from the capture; the base64 payload is the synthetic JPEG
// above and the thought signature is truncated.
func captureBody(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf(`{
  "response": {
    "candidates": [
      {
        "content": {
          "role": "model",
          "parts": [
            {
              "thoughtSignature": "EvEBCu4BAWkUfRNbbvBlFHw",
              "inlineData": {"mimeType": "image/jpeg", "data": %q}
            }
          ]
        },
        "finishReason": "STOP"
      }
    ],
    "usageMetadata": {"promptTokenCount": 27, "candidatesTokenCount": 1511, "totalTokenCount": 1538},
    "modelVersion": "gemini-3.1-flash-image",
    "responseId": "UoqsavOOGejdjMcPkoS92AE"
  },
  "traceId": "36205ee2d9efddba",
  "metadata": {}
}`, base64.StdEncoding.EncodeToString(onePixelJPEG))
}

// TestAntigravityNaiveSingleEnvelopeDecodeFindsNoImage is the regression that
// justifies hand-rolling this client.
//
// google.golang.org/genai unmarshals a response body straight into
// GenerateContentResponse — a SINGLE-envelope decode. Against an Antigravity
// body that decode SUCCEEDS and yields an empty struct, so the call returns
// 200, no error, and no image: a silent success. This test pins that failure so
// a future refactor cannot quietly reintroduce the SDK here. If it ever starts
// failing because the endpoint stopped double-wrapping, the wrapper can go.
func TestAntigravityNaiveSingleEnvelopeDecodeFindsNoImage(t *testing.T) {
	raw := []byte(captureBody(t))

	// The shape genai expects: GenerateContentResponse at the TOP level.
	var naive agywire.GenerateResp
	if err := json.Unmarshal(raw, &naive); err != nil {
		t.Fatalf("naive parse errored; the point is that it succeeds-but-is-empty: %v", err)
	}
	if len(naive.Candidates) != 0 || naive.UsageMetadata != nil {
		t.Fatal("naive single-envelope parse found data; the double envelope is gone")
	}

	// And the decode path proves the consequence, not just the shape: a
	// single-envelope decode yields NO IMAGE.
	images, err := decodeAntigravityImages(&naive)
	if len(images) != 0 {
		t.Fatalf("naive decode produced %d images; it must produce none", len(images))
	}
	if err == nil {
		t.Fatal("naive decode returned no error AND no image — this is exactly the silent success being pinned")
	}

	// The double-envelope decode this client uses finds the real payload.
	var wrapped agywire.Envelope
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		t.Fatalf("wrapped parse: %v", err)
	}
	if wrapped.Response == nil || len(wrapped.Response.Candidates) == 0 {
		t.Fatal("wrapped parse found no candidates")
	}
	if wrapped.TraceID != "36205ee2d9efddba" {
		t.Errorf("traceId = %q", wrapped.TraceID)
	}
	images, err = decodeAntigravityImages(wrapped.Response)
	if err != nil {
		t.Fatalf("wrapped decode: %v", err)
	}
	if len(images) != 1 {
		t.Fatalf("wrapped decode produced %d images, want 1", len(images))
	}
}

// newTestAntigravity points a client at an httptest server and captures the
// request body it sent.
func newTestAntigravity(t *testing.T, handler func(w http.ResponseWriter, body []byte)) (*AntigravityClient, *[]byte, *http.Header) {
	t.Helper()

	var sentBody []byte
	var sentHeader http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sentBody = body
		sentHeader = r.Header.Clone()
		handler(w, body)
	}))
	t.Cleanup(server.Close)

	client, err := NewAntigravity(Config{
		BaseURL:  server.URL,
		APIKey:   syntheticToken,
		ModelID:  "gemini-3.1-flash-image",
		APIModel: "gemini-3.1-flash-image",
		Driver:   "antigravity",
	})
	if err != nil {
		t.Fatalf("NewAntigravity: %v", err)
	}
	return client, &sentBody, &sentHeader
}

// TestAntigravityGenerateImage_DecodesCapture runs the recorded body end to
// end through the real client.
func TestAntigravityGenerateImage_DecodesCapture(t *testing.T) {
	client, _, _ := newTestAntigravity(t, func(w http.ResponseWriter, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, captureBody(t))
	})

	response, err := client.GenerateImage(context.Background(), Request{Prompt: "a cat", Size: "1024x1024"})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if len(response.Images) != 1 {
		t.Fatalf("got %d images, want 1", len(response.Images))
	}
	// The MIME type is sniffed from the bytes, not trusted from the declared
	// mimeType, because the attachment layer keys rendering off what we store.
	if response.Images[0].MIMEType != "image/jpeg" {
		t.Errorf("MIMEType = %q, want image/jpeg", response.Images[0].MIMEType)
	}
	if string(response.Images[0].Bytes) != string(onePixelJPEG) {
		t.Error("image bytes did not survive the base64 round trip")
	}
	if response.ModelID != "gemini-3.1-flash-image" || response.Driver != "antigravity" {
		t.Errorf("response = %+v", response)
	}
	if response.UpstreamRequestID != "36205ee2d9efddba" {
		t.Errorf("UpstreamRequestID = %q, want the capture's traceId", response.UpstreamRequestID)
	}
}

// TestAntigravityRequestEnvelope pins the outer request against the capture.
// The fields here are the ones that are easy to carry over wrongly from the
// chat driver: requestType, the requestId prefix, and a model id with no
// effort suffix.
func TestAntigravityRequestEnvelope(t *testing.T) {
	client, sentBody, sentHeader := newTestAntigravity(t, func(w http.ResponseWriter, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, captureBody(t))
	})

	if _, err := client.GenerateImage(context.Background(), Request{Prompt: "a cat", Size: "1024x1024"}); err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(*sentBody, &decoded); err != nil {
		t.Fatalf("unmarshal sent body: %v", err)
	}

	for key, want := range map[string]string{
		"project":     "aicode-consumers",
		"model":       "gemini-3.1-flash-image",
		"userAgent":   "antigravity",
		"requestType": "image_gen",
	} {
		if got, _ := decoded[key].(string); got != want {
			t.Errorf("envelope[%q] = %q, want %q", key, got, want)
		}
	}

	requestID, _ := decoded["requestId"].(string)
	if !strings.HasPrefix(requestID, "image_gen/") {
		t.Errorf("requestId = %q, want the image_gen/<epochMs>/<uuid>/<n> shape", requestID)
	}
	if parts := strings.Split(requestID, "/"); len(parts) != 4 {
		t.Errorf("requestId = %q, want 4 slash-separated segments", requestID)
	}

	inner, ok := decoded["request"].(map[string]any)
	if !ok {
		t.Fatal("envelope has no inner request object")
	}
	if _, present := inner["model"]; present {
		t.Error("inner request carries a model field; the capture puts model only at the top level")
	}
	// Chat-only fields must not leak onto this surface.
	for _, absent := range []string{"systemInstruction", "tools", "sessionId"} {
		if _, present := inner[absent]; present {
			t.Errorf("inner request carries %q; the image capture sends none", absent)
		}
	}

	generation, ok := inner["generationConfig"].(map[string]any)
	if !ok {
		t.Fatal("inner request has no generationConfig")
	}
	if count, _ := generation["candidateCount"].(float64); count != 1 {
		t.Errorf("candidateCount = %v, want 1", generation["candidateCount"])
	}
	if _, present := generation["thinkingConfig"]; present {
		t.Error("generationConfig carries thinkingConfig; thinking is a chat concern")
	}
	imageConfig, ok := generation["imageConfig"].(map[string]any)
	if !ok {
		t.Fatal("generationConfig has no imageConfig")
	}
	if ratio, _ := imageConfig["aspectRatio"].(string); ratio != "1:1" {
		t.Errorf("aspectRatio = %q, want 1:1 for a 1024x1024 request", ratio)
	}

	if got := sentHeader.Get("Authorization"); got != "Bearer "+syntheticToken {
		t.Errorf("Authorization = %q", got)
	}
	if got := sentHeader.Get("User-Agent"); got != agywire.UserAgentHeader {
		t.Errorf("User-Agent = %q, want the captured antigravity/cli identity", got)
	}
}

// TestAntigravityAspectRatio checks the size translation, including that an
// unrecognized value is DROPPED rather than forwarded — the endpoint rejects an
// unknown aspectRatio outright, so forwarding turns a cosmetic mismatch into a
// failed generation.
func TestAntigravityAspectRatio(t *testing.T) {
	tests := []struct {
		size string
		want string // "" means imageConfig must be absent entirely
	}{
		{size: "1024x1024", want: "1:1"},
		{size: "1536x1024", want: "3:2"},
		{size: "1024x1536", want: "2:3"},
		{size: "auto", want: ""},
		{size: "", want: ""},
		{size: "4096x999", want: ""},
	}

	client, _ := NewAntigravity(Config{APIKey: syntheticToken, APIModel: "gemini-3.1-flash-image"})
	for _, tc := range tests {
		t.Run(tc.size, func(t *testing.T) {
			generation := client.buildEnvelope(Request{Prompt: "x", Size: tc.size}).Request.GenerationConfig
			if tc.want == "" {
				if generation.ImageConfig != nil {
					t.Errorf("size %q produced aspectRatio %q; an unmapped size must be omitted",
						tc.size, generation.ImageConfig.AspectRatio)
				}
				return
			}
			if generation.ImageConfig == nil || generation.ImageConfig.AspectRatio != tc.want {
				t.Errorf("size %q produced %+v, want aspectRatio %q", tc.size, generation.ImageConfig, tc.want)
			}
		})
	}
}

// TestAntigravityNoImage_ExplainedIsTerminal covers a 200 that carried no
// image but DID say why. An explained refusal must not be retried: a content
// filter will decide the same way next time, so a retry only bills the user
// twice for the same "no".
func TestAntigravityNoImage_ExplainedIsTerminal(t *testing.T) {
	body := `{"response":{"candidates":[{"content":{"role":"model","parts":[
		{"text":"I can't generate that."}]},"finishReason":"IMAGE_SAFETY"}]},"traceId":"t"}`

	calls := 0
	client, _, _ := newTestAntigravity(t, func(w http.ResponseWriter, _ []byte) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	})

	_, err := client.GenerateImage(context.Background(), Request{Prompt: "something"})
	if err == nil {
		t.Fatal("expected an error when no image came back")
	}
	if calls != 1 {
		t.Errorf("made %d calls; an explained refusal is terminal and must not be retried", calls)
	}
	if !strings.Contains(err.Error(), "IMAGE_SAFETY") {
		t.Errorf("error = %q, want the finish reason named", err)
	}
	if !strings.Contains(err.Error(), "I can't generate that.") {
		t.Errorf("error = %q, want the model's own text surfaced", err)
	}
}

// TestAntigravityNoImage_UnexplainedIsRetryable is the other half. An
// UNEXPLAINED empty response is the intermittent upstream drop, which is
// exactly the case worth one retry — and it is capped at one, because the
// provider already billed the generation.
func TestAntigravityNoImage_UnexplainedIsRetryable(t *testing.T) {
	calls := 0
	client, _, _ := newTestAntigravity(t, func(w http.ResponseWriter, _ []byte) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		// STOP means the generation succeeded; no image alongside it is the
		// unexplained drop, not a refusal.
		_, _ = io.WriteString(w, `{"response":{"candidates":[{"finishReason":"STOP"}]},"traceId":"t"}`)
	})
	client.config.RetryBaseDelay = time.Millisecond

	_, err := client.GenerateImage(context.Background(), Request{Prompt: "something"})
	if err == nil {
		t.Fatal("expected an error when no image came back")
	}
	if calls != emptyResultMaxAttempts {
		t.Errorf("made %d calls, want %d; an unexplained empty result gets exactly one retry",
			calls, emptyResultMaxAttempts)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !apiErr.EmptyResult {
		t.Errorf("error = %#v, want an *APIError with EmptyResult set", err)
	}
}

// TestAntigravityAPIError parses Google's error envelope, which — unlike the
// success body — is NOT double-wrapped.
func TestAntigravityAPIError(t *testing.T) {
	client, _, _ := newTestAntigravity(t, func(w http.ResponseWriter, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":400,"message":"bad aspect ratio","status":"INVALID_ARGUMENT"}}`)
	})

	_, err := client.GenerateImage(context.Background(), Request{Prompt: "x"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %#v, want an *APIError", err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d", apiErr.StatusCode)
	}
	if apiErr.Type != "INVALID_ARGUMENT" || apiErr.Message != "bad aspect ratio" {
		t.Errorf("apiErr = %+v", apiErr)
	}
}

// TestAntigravityRequiresToken pins the one construction precondition: this
// surface has no anonymous mode, so a client with no bearer can only 401.
func TestAntigravityRequiresToken(t *testing.T) {
	if _, err := NewAntigravity(Config{APIModel: "gemini-3.1-flash-image"}); err == nil {
		t.Fatal("expected an error when no access token is configured")
	}
}
