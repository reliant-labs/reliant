// Copyright (c) 2025 Reliant Labs
package imagegen

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestEmptyData_IsRetriedRatherThanFailingImmediately is the behavior the Nano
// Banana Pro investigation asked for. An HTTP 200 carrying data:[] is an
// intermittent upstream drop — Vertex returns a candidate with no image part
// while still billing the generation — and a live investigation saw the
// identical prompt succeed on 3 of 3 retries. Failing on the first one turns a
// recoverable blip into a user-visible failure.
func TestEmptyData_IsRetriedRatherThanFailingImmediately(t *testing.T) {
	want := pngBytes(t)
	attempts := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			// The exact failing envelope: 200, empty data, tokens billed.
			_, _ = w.Write([]byte(`{"data":[],"usage":{"total_tokens":1294,"input_tokens":8,"output_tokens":1120}}`))
			return
		}
		_, _ = w.Write([]byte(successBody(t, [][]byte{want}, "")))
	}))
	defer server.Close()

	client := newTestClient(t, Config{
		BaseURL: server.URL + "/v1", APIModel: "gemini-3-pro-image", Driver: "reliant",
		RetryBaseDelay: time.Millisecond,
	})

	resp, err := client.GenerateImage(context.Background(), Request{Prompt: "a plain blue circle"})
	if err != nil {
		t.Fatalf("an empty data[] must be retried, not returned as a terminal error: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
	if len(resp.Images) != 1 || !bytes.Equal(resp.Images[0].Bytes, want) {
		t.Error("the retry did not return the eventual success payload")
	}
}

// TestEmptyData_RetriesExactlyOnce pins the cost cap, which is the whole
// reason this case does not simply reuse the transport ladder. Each attempt
// bills a full generation (~1120 output tokens observed) for an image nobody
// receives, so the default 3 attempts would triple the cost of a failure that
// one retry usually clears.
func TestEmptyData_RetriesExactlyOnce(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	client := newTestClient(t, Config{
		BaseURL: server.URL + "/v1", APIModel: "gemini-3-pro-image", Driver: "reliant",
		RetryBaseDelay: time.Millisecond,
	})

	_, err := client.GenerateImage(context.Background(), Request{Prompt: "x"})
	if err == nil {
		t.Fatal("expected an error once the retry also came back empty")
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want exactly 2; each billed retry must be paid for deliberately", attempts)
	}
}

// TestEmptyData_ErrorNamesBothPossibleCauses pins the message. On the managed
// path the cause is provably unrecoverable — LiteLLM's transform collapses a
// safety filter, a text-only reply, a blocked prompt and a snake_case key into
// byte-identical output — so the honest error names both possibilities and
// tells the user retrying often works. A bare "returned no images" leaves them
// unable to decide whether to retry or reword.
func TestEmptyData_ErrorNamesBothPossibleCauses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	client := newTestClient(t, Config{
		BaseURL: server.URL + "/v1", APIModel: "gemini-3-pro-image", Driver: "reliant",
		RetryBaseDelay: time.Millisecond,
	})

	_, err := client.GenerateImage(context.Background(), Request{Prompt: "x"})
	if err == nil {
		t.Fatal("expected an error")
	}
	message := err.Error()
	for _, want := range []string{"transient", "content filter", "retrying"} {
		if !strings.Contains(strings.ToLower(message), want) {
			t.Errorf("error should mention %q so the user knows what to do next; got: %s", want, message)
		}
	}
	// "status 0" would read as a transport failure. The call succeeded; the
	// payload was empty.
	if strings.Contains(message, "status 0") {
		t.Errorf("an empty result has no HTTP status to report, got: %s", message)
	}
}

// TestGeminiNative_UnexplainedEmptyResponseIsRetried pins that the native path
// treats the SAME unexplained case the same way: no image, no reason, retry.
func TestGeminiNative_UnexplainedEmptyResponseIsRetried(t *testing.T) {
	want := pngBytes(t)
	attempts := 0

	client := newGeminiTestClient(t, Config{
		ModelID: "gemini-3-pro-image", APIModel: "gemini-3-pro-image", Driver: "gemini",
		RetryBaseDelay: time.Millisecond,
	}, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[]}}]}`))
			return
		}
		_, _ = w.Write([]byte(geminiEnvelope(geminiInlineDataPart("image/png", want))))
	})

	resp, err := client.GenerateImage(context.Background(), Request{Prompt: "a plain blue circle"})
	if err != nil {
		t.Fatalf("an unexplained empty response must be retried: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
	if len(resp.Images) != 1 || !bytes.Equal(resp.Images[0].Bytes, want) {
		t.Error("the retry did not return the eventual success payload")
	}
}

// TestGeminiNative_ExplainedRefusalIsTerminal is the native path's genuine
// advantage over the proxied one, and the reason its retry decision is not a
// copy of the OpenAI-shaped path's.
//
// Calling :generateContent directly, finishReason and text parts survive —
// LiteLLM discards both. When the model explained itself, a retry would bill a
// second generation to receive the same refusal, because a content filter
// decides the same way twice. So an EXPLAINED refusal is terminal and reports
// the real reason, while only an UNEXPLAINED drop is retried.
func TestGeminiNative_ExplainedRefusalIsTerminalAndReportsTheRealReason(t *testing.T) {
	attempts := 0
	client := newGeminiTestClient(t, Config{
		ModelID: "gemini-3-pro-image", APIModel: "gemini-3-pro-image", Driver: "gemini",
		RetryBaseDelay: time.Millisecond,
	}, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		_, _ = w.Write([]byte(`{"candidates":[{"finishReason":"IMAGE_SAFETY","content":{"parts":[]}}]}`))
	})

	_, err := client.GenerateImage(context.Background(), Request{Prompt: "x"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1; a stated refusal will not change on a retry, so retrying only bills twice", attempts)
	}
	if !strings.Contains(err.Error(), "IMAGE_SAFETY") {
		t.Errorf("the native path must report the reason LiteLLM would have discarded, got: %v", err)
	}
	// The hedged managed-path message would be a regression here: we KNOW why.
	if strings.Contains(err.Error(), "transient") {
		t.Errorf("a known refusal must not be reported as possibly transient, got: %v", err)
	}
}

// TestGeminiNative_BlockedPromptIsTerminalAndReportsTheBlockReason covers the
// other signal LiteLLM discards: promptFeedback, which is where a blocked
// prompt reports itself when no candidate is returned at all.
func TestGeminiNative_BlockedPromptIsTerminalAndReportsTheBlockReason(t *testing.T) {
	attempts := 0
	client := newGeminiTestClient(t, Config{
		ModelID: "gemini-3-pro-image", APIModel: "gemini-3-pro-image", Driver: "gemini",
		RetryBaseDelay: time.Millisecond,
	}, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		_, _ = w.Write([]byte(`{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT"},"candidates":[]}`))
	})

	_, err := client.GenerateImage(context.Background(), Request{Prompt: "x"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1; a blocked prompt is blocked on every attempt", attempts)
	}
	if !strings.Contains(err.Error(), "PROHIBITED_CONTENT") {
		t.Errorf("error must name the block reason, got: %v", err)
	}
}
