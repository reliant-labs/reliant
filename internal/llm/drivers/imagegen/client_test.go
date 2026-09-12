// Copyright (c) 2025 Reliant Labs
package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	openaisdk "github.com/openai/openai-go/v3"
)

// newTestClient builds a client the way production does — through the same
// injected-SDK-constructor seam — but with the vendor constructor directly,
// since a test has no reason to import internal/llm and the idle-timeout
// wrapper that constructor installs is what internal/llm's own tests cover.
//
// The construction error is asserted here rather than at each of the sixteen
// call sites, which are all about wire behavior.
func newTestClient(t *testing.T, config Config) *Client {
	t.Helper()
	client, err := New(config, openaisdk.NewClient)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

// pngBytes builds a real 1x1 PNG so MIME sniffing has genuine bytes to work
// from rather than a hand-written magic-number prefix.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func successBody(t *testing.T, images [][]byte, revisedPrompt string) string {
	t.Helper()
	type entry struct {
		B64JSON       string `json:"b64_json"`
		RevisedPrompt string `json:"revised_prompt,omitempty"`
	}
	envelope := struct {
		Data []entry `json:"data"`
	}{}
	for _, img := range images {
		envelope.Data = append(envelope.Data, entry{
			B64JSON:       base64.StdEncoding.EncodeToString(img),
			RevisedPrompt: revisedPrompt,
		})
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return string(raw)
}

func TestGenerateImage_ReturnsRawBytesAndMIMEType(t *testing.T) {
	want := pngBytes(t)

	var gotPath, gotAuth, gotManagedKey, gotContentType string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotManagedKey = r.Header.Get("X-Reliant-Managed-Key")
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-litellm-call-id", "call-abc")
		w.Header().Set("x-request-id", "req-xyz")
		_, _ = w.Write([]byte(successBody(t, [][]byte{want}, "a revised prompt")))
	}))
	defer server.Close()

	client := newTestClient(t, Config{
		BaseURL:      server.URL + "/v1",
		APIKey:       "sk-test",
		ExtraHeaders: map[string]string{"X-Reliant-Managed-Key": "rly_managed"},
		ModelID:      "gpt-image-2.5-flare",
		APIModel:     "gpt-image-2.5-flare",
		Driver:       "reliant",
	})

	resp, err := client.GenerateImage(context.Background(), Request{
		Prompt: "a red cube",
		Size:   "1024x1024",
	})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}

	if gotPath != "/v1/images/generations" {
		t.Errorf("posted to %q, want /v1/images/generations", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want Bearer sk-test", gotAuth)
	}
	if gotManagedKey != "rly_managed" {
		t.Errorf("X-Reliant-Managed-Key = %q, want rly_managed", gotManagedKey)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotBody["model"] != "gpt-image-2.5-flare" || gotBody["prompt"] != "a red cube" || gotBody["size"] != "1024x1024" {
		t.Errorf("request body = %#v", gotBody)
	}
	if _, present := gotBody["quality"]; present {
		t.Error("unset optional field quality was sent; it should be omitted so the provider defaults apply")
	}

	if len(resp.Images) != 1 {
		t.Fatalf("got %d images, want 1", len(resp.Images))
	}
	got := resp.Images[0]
	if !bytes.Equal(got.Bytes, want) {
		t.Errorf("image bytes round-tripped incorrectly: got %d bytes, want %d", len(got.Bytes), len(want))
	}
	if got.MIMEType != "image/png" {
		t.Errorf("MIMEType = %q, want image/png", got.MIMEType)
	}
	if got.RevisedPrompt != "a revised prompt" {
		t.Errorf("RevisedPrompt = %q", got.RevisedPrompt)
	}
	if resp.LiteLLMCallID != "call-abc" {
		t.Errorf("LiteLLMCallID = %q, want call-abc", resp.LiteLLMCallID)
	}
	if resp.UpstreamRequestID != "req-xyz" {
		t.Errorf("UpstreamRequestID = %q, want req-xyz", resp.UpstreamRequestID)
	}
	if resp.ModelID != "gpt-image-2.5-flare" || resp.Driver != "reliant" {
		t.Errorf("resp model/driver = %q/%q", resp.ModelID, resp.Driver)
	}
}

func TestGenerateImage_MultipleImages(t *testing.T) {
	first := pngBytes(t)
	second := pngBytes(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if count, _ := body["n"].(float64); count != 2 {
			t.Errorf("n = %v, want 2", body["n"])
		}
		_, _ = w.Write([]byte(successBody(t, [][]byte{first, second}, "")))
	}))
	defer server.Close()

	client := newTestClient(t, Config{BaseURL: server.URL + "/v1", APIModel: "gpt-image-2.5-flare", Driver: "openai"})
	resp, err := client.GenerateImage(context.Background(), Request{Prompt: "two cubes", Count: 2})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if len(resp.Images) != 2 {
		t.Fatalf("got %d images, want 2", len(resp.Images))
	}
}

func TestGenerateImage_URLOnlyResponseIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"url":"https://example.com/img.png"}]}`))
	}))
	defer server.Close()

	client := newTestClient(t, Config{BaseURL: server.URL + "/v1", APIModel: "gpt-image-2.5-flare", Driver: "openai"})
	_, err := client.GenerateImage(context.Background(), Request{Prompt: "x"})
	if err == nil {
		t.Fatal("expected an error for a url-only response")
	}
	if !strings.Contains(err.Error(), "b64_json") {
		t.Errorf("error should name the b64_json requirement, got: %v", err)
	}
}

func TestGenerateImage_NonImageBytesRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(successBody(t, [][]byte{[]byte("this is plain text, not an image")}, "")))
	}))
	defer server.Close()

	client := newTestClient(t, Config{BaseURL: server.URL + "/v1", APIModel: "gpt-image-2.5-flare", Driver: "openai"})
	if _, err := client.GenerateImage(context.Background(), Request{Prompt: "x"}); err == nil {
		t.Fatal("expected an error for non-image bytes")
	}
}

func TestGenerateImage_EmptyPromptIsRejectedWithoutCallingProvider(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()

	client := newTestClient(t, Config{BaseURL: server.URL + "/v1", APIModel: "gpt-image-2.5-flare", Driver: "openai"})
	if _, err := client.GenerateImage(context.Background(), Request{Prompt: "  "}); err == nil {
		t.Fatal("expected an error for an empty prompt")
	}
	if called {
		t.Error("provider was called for an empty prompt")
	}
}

func TestGenerateImage_APIErrorSurfacesStatusAndMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"prompt was rejected","type":"invalid_request_error","code":"content_policy_violation"}}`))
	}))
	defer server.Close()

	client := newTestClient(t, Config{BaseURL: server.URL + "/v1", APIModel: "gpt-image-2.5-flare", Driver: "openai"})
	_, err := client.GenerateImage(context.Background(), Request{Prompt: "x"})

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want 400", apiErr.StatusCode)
	}
	if apiErr.Code != "content_policy_violation" {
		t.Errorf("Code = %q", apiErr.Code)
	}
	if !strings.Contains(apiErr.Error(), "prompt was rejected") {
		t.Errorf("Error() should carry the provider message, got: %s", apiErr.Error())
	}
}

func TestGenerateImage_ManagedQuotaExhaustedIsTerminalAndMarked(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"Free tier quota exceeded","type":"insufficient_quota","code":"insufficient_quota","upgrade_url":"/billing/plans"}}`))
	}))
	defer server.Close()

	client := newTestClient(t, Config{BaseURL: server.URL + "/v1", APIModel: "gpt-image-2.5-flare", Driver: "reliant", RetryBaseDelay: time.Millisecond})
	_, err := client.GenerateImage(context.Background(), Request{Prompt: "x"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if attempts != 1 {
		t.Errorf("quota exhaustion retried %d times; it is terminal and must not retry", attempts-1)
	}
	if !strings.Contains(err.Error(), "RELIANT_MANAGED_QUOTA_EXHAUSTED") {
		t.Errorf("error must carry the quota marker so it survives Temporal serialization, got: %v", err)
	}
}

func TestGenerateImage_BYOQuotaErrorIsNotMarkedAsManaged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"You exceeded your quota","type":"insufficient_quota","code":"insufficient_quota"}}`))
	}))
	defer server.Close()

	client := newTestClient(t, Config{BaseURL: server.URL + "/v1", APIModel: "gpt-image-2.5-flare", Driver: "openai", RetryBaseDelay: time.Millisecond})
	_, err := client.GenerateImage(context.Background(), Request{Prompt: "x"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "RELIANT_MANAGED_QUOTA_EXHAUSTED") {
		t.Errorf("a BYO key's own quota error must not be reported as managed-credit exhaustion, got: %v", err)
	}
}

func TestGenerateImage_RetriesTransientGatewayErrors(t *testing.T) {
	want := pngBytes(t)
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream busy"}}`))
			return
		}
		_, _ = w.Write([]byte(successBody(t, [][]byte{want}, "")))
	}))
	defer server.Close()

	client := newTestClient(t, Config{BaseURL: server.URL + "/v1", APIModel: "gpt-image-2.5-flare", Driver: "openai", RetryBaseDelay: time.Millisecond})
	resp, err := client.GenerateImage(context.Background(), Request{Prompt: "x"})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	if len(resp.Images) != 1 || !bytes.Equal(resp.Images[0].Bytes, want) {
		t.Error("retry did not return the eventual success payload")
	}
}

func TestGenerateImage_DoesNotRetryClientErrors(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"nope"}}`))
	}))
	defer server.Close()

	client := newTestClient(t, Config{BaseURL: server.URL + "/v1", APIModel: "gpt-image-2.5-flare", Driver: "openai", RetryBaseDelay: time.Millisecond})
	if _, err := client.GenerateImage(context.Background(), Request{Prompt: "x"}); err == nil {
		t.Fatal("expected an error")
	}
	if attempts != 1 {
		t.Errorf("a 400 was retried %d times; client errors are terminal", attempts-1)
	}
}

func TestGenerateImage_BaseURLTrailingSlashIsNormalized(t *testing.T) {
	want := pngBytes(t)
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(successBody(t, [][]byte{want}, "")))
	}))
	defer server.Close()

	client := newTestClient(t, Config{BaseURL: server.URL + "/v1/", APIModel: "gpt-image-2.5-flare", Driver: "openai"})
	if _, err := client.GenerateImage(context.Background(), Request{Prompt: "x"}); err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if gotPath != "/v1/images/generations" {
		t.Errorf("path = %q, want /v1/images/generations", gotPath)
	}
}
