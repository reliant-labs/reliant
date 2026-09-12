// Copyright (c) 2025 Reliant Labs
package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

// geminiEnvelope renders the AI Studio success envelope EXACTLY as it was
// captured from a live call:
//
//	{"candidates":[{"content":{"parts":[
//	   {"inlineData":{"mimeType":"image/png","data":"<base64>"}}]}}]}
//
// Written as a raw JSON string rather than built from genai structs on
// purpose: the point of this fixture is that our decode matches the bytes
// Google actually sends, and marshalling the SDK's own types to produce it
// would only prove the SDK round-trips itself.
func geminiEnvelope(parts ...string) string {
	return fmt.Sprintf(`{"candidates":[{"content":{"parts":[%s]}}]}`, strings.Join(parts, ","))
}

func geminiInlineDataPart(mimeType string, data []byte) string {
	return fmt.Sprintf(`{"inlineData":{"mimeType":%q,"data":%q}}`,
		mimeType, base64.StdEncoding.EncodeToString(data))
}

func geminiTextPart(text string) string {
	return fmt.Sprintf(`{"text":%q}`, text)
}

func decodeJSONBody(t *testing.T, r *http.Request, into any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		t.Errorf("decode request body: %v", err)
	}
}

// newGeminiTestClient points a real genai-backed client at a fake AI Studio.
// The SDK is exercised for real — URL composition, auth, request encoding and
// response decoding all run — with only the network endpoint substituted.
func newGeminiTestClient(t *testing.T, config Config, handler http.HandlerFunc) *GeminiClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	if config.APIKey == "" {
		config.APIKey = "AIzaSyTestKey"
	}
	config.BaseURL = server.URL

	client, err := NewGemini(config, genai.NewClient)
	if err != nil {
		t.Fatalf("NewGemini: %v", err)
	}
	return client
}

// TestGeminiGenerateImage_DecodesNativeInlineDataEnvelope is the core test for
// this path: the native envelope, which is shaped nothing like OpenAI's
// data[].b64_json, must yield the correct bytes and a MIME type sniffed from
// them. It also pins the request half — the endpoint, the credential, and the
// IMAGE response modality without which the model answers in text.
func TestGeminiGenerateImage_DecodesNativeInlineDataEnvelope(t *testing.T) {
	want := pngBytes(t)

	var gotPath, gotAPIKeyHeader, gotAPIKeyQuery string
	var gotBody map[string]any

	client := newGeminiTestClient(t, Config{
		ModelID:  "gemini-3.1-flash-image",
		APIModel: "gemini-3.1-flash-image",
		Driver:   "gemini",
		APIKey:   "AIzaSyLiveLike",
	}, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKeyHeader = r.Header.Get("x-goog-api-key")
		gotAPIKeyQuery = r.URL.Query().Get("key")
		decodeJSONBody(t, r, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(geminiEnvelope(geminiInlineDataPart("image/png", want))))
	})

	resp, err := client.GenerateImage(context.Background(), Request{
		Prompt: "a red cube",
		Size:   "1024x1024",
	})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}

	// The native endpoint, verified live: the model name is part of the PATH
	// with a :generateContent suffix, not a field in the body.
	if gotPath != "/v1beta/models/gemini-3.1-flash-image:generateContent" {
		t.Errorf("posted to %q, want /v1beta/models/gemini-3.1-flash-image:generateContent", gotPath)
	}
	// AI Studio takes the key as a credential of its own, NOT as an
	// Authorization: Bearer header the way openai and codex do.
	if gotAPIKeyHeader != "AIzaSyLiveLike" && gotAPIKeyQuery != "AIzaSyLiveLike" {
		t.Errorf("API key was sent neither as x-goog-api-key (%q) nor ?key= (%q)", gotAPIKeyHeader, gotAPIKeyQuery)
	}

	// Without responseModalities=[IMAGE] the model answers in text and no
	// inlineData part ever arrives, so this is load-bearing, not cosmetic.
	generationConfig, _ := gotBody["generationConfig"].(map[string]any)
	modalities, _ := generationConfig["responseModalities"].([]any)
	if len(modalities) != 1 || modalities[0] != "IMAGE" {
		t.Errorf("responseModalities = %#v, want [IMAGE]", modalities)
	}
	imageConfig, _ := generationConfig["imageConfig"].(map[string]any)
	if imageConfig["aspectRatio"] != "1:1" {
		t.Errorf("aspectRatio = %#v, want 1:1 (translated from size 1024x1024)", imageConfig["aspectRatio"])
	}

	if len(resp.Images) != 1 {
		t.Fatalf("got %d images, want 1", len(resp.Images))
	}
	got := resp.Images[0]
	if !bytes.Equal(got.Bytes, want) {
		t.Errorf("image bytes decoded incorrectly: got %d bytes, want %d", len(got.Bytes), len(want))
	}
	if got.MIMEType != "image/png" {
		t.Errorf("MIMEType = %q, want image/png", got.MIMEType)
	}
	if resp.ModelID != "gemini-3.1-flash-image" || resp.Driver != "gemini" {
		t.Errorf("resp model/driver = %q/%q", resp.ModelID, resp.Driver)
	}
}

// TestGeminiGenerateImage_IgnoresTextPartsAlongsideAnImage covers the mixed
// response: the model may narrate as well as generate. The image is the
// result; the commentary must not displace it or become an error.
func TestGeminiGenerateImage_IgnoresTextPartsAlongsideAnImage(t *testing.T) {
	want := pngBytes(t)

	client := newGeminiTestClient(t, Config{
		ModelID: "gemini-3.1-flash-image", APIModel: "gemini-3.1-flash-image", Driver: "gemini",
	}, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(geminiEnvelope(
			geminiTextPart("Here is the cube you asked for."),
			geminiInlineDataPart("image/png", want),
		)))
	})

	resp, err := client.GenerateImage(context.Background(), Request{Prompt: "a red cube"})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if len(resp.Images) != 1 || !bytes.Equal(resp.Images[0].Bytes, want) {
		t.Fatalf("got %d images; a text part alongside an image must not displace it", len(resp.Images))
	}
}

// TestGeminiGenerateImage_TextOnlyResponseSurfacesTheModelsText is the failure
// this path most needs to explain. A text-only response is the model saying
// why it did not generate, and a bare "returned no images" throws that away —
// leaving a user with a refusal they cannot see.
func TestGeminiGenerateImage_TextOnlyResponseSurfacesTheModelsText(t *testing.T) {
	client := newGeminiTestClient(t, Config{
		ModelID: "gemini-3.1-flash-image", APIModel: "gemini-3.1-flash-image", Driver: "gemini",
	}, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(geminiEnvelope(
			geminiTextPart("I can't create images of real people."),
		)))
	})

	_, err := client.GenerateImage(context.Background(), Request{Prompt: "a photo of a real person"})
	if err == nil {
		t.Fatal("expected an error when the response carries no image")
	}
	if !strings.Contains(err.Error(), "I can't create images of real people.") {
		t.Errorf("error must carry the model's own explanation, got: %v", err)
	}
}

// TestGeminiGenerateImage_NoImageSurfacesTheFinishReason covers the other way
// the model explains itself: no text part at all, only a finish reason such as
// IMAGE_SAFETY. Without it the user gets "no images" and no cause.
func TestGeminiGenerateImage_NoImageSurfacesTheFinishReason(t *testing.T) {
	client := newGeminiTestClient(t, Config{
		ModelID: "gemini-3.1-flash-image", APIModel: "gemini-3.1-flash-image", Driver: "gemini",
	}, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"candidates":[{"finishReason":"IMAGE_SAFETY","content":{"parts":[]}}]}`))
	})

	_, err := client.GenerateImage(context.Background(), Request{Prompt: "x"})
	if err == nil {
		t.Fatal("expected an error when the response carries no image")
	}
	if !strings.Contains(err.Error(), "IMAGE_SAFETY") {
		t.Errorf("error must name the finish reason, got: %v", err)
	}
}

// TestGeminiGenerateImage_NonImageBytesRejected pins that the declared
// mimeType is treated as a claim and the bytes as the fact. The attachment
// layer keys rendering off what we store, so storing text as image/png would
// produce a broken attachment rather than a clear failure.
func TestGeminiGenerateImage_NonImageBytesRejected(t *testing.T) {
	client := newGeminiTestClient(t, Config{
		ModelID: "gemini-3.1-flash-image", APIModel: "gemini-3.1-flash-image", Driver: "gemini",
	}, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(geminiEnvelope(
			geminiInlineDataPart("image/png", []byte("this is plain text, not an image")),
		)))
	})

	if _, err := client.GenerateImage(context.Background(), Request{Prompt: "x"}); err == nil {
		t.Fatal("expected an error for bytes that are not an image despite a declared image mimeType")
	}
}

// TestGeminiGenerateImage_APIErrorIsNormalized pins that AI Studio's error
// envelope — which is Google-shaped, not OpenAI-shaped — arrives as this
// package's *APIError. Every caller and the shared retry ladder read that one
// type, so a client that leaked the SDK's own error would bypass both.
func TestGeminiGenerateImage_APIErrorIsNormalized(t *testing.T) {
	client := newGeminiTestClient(t, Config{
		ModelID: "gemini-3.1-flash-image", APIModel: "gemini-3.1-flash-image", Driver: "gemini",
		RetryBaseDelay: time.Millisecond,
	}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"prompt was rejected"}}`))
	})

	_, err := client.GenerateImage(context.Background(), Request{Prompt: "x"})

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want 400", apiErr.StatusCode)
	}
	if apiErr.Type != "INVALID_ARGUMENT" {
		t.Errorf("Type = %q, want INVALID_ARGUMENT", apiErr.Type)
	}
	if !strings.Contains(apiErr.Error(), "prompt was rejected") {
		t.Errorf("Error() should carry the provider message, got: %s", apiErr.Error())
	}
}

// TestGeminiGenerateImage_QuotaExhaustionIsTerminal pins the behavior that
// matters for the key this was developed against. AI Studio's free tier
// returns 429 RESOURCE_EXHAUSTED against a DAILY per-project quota, which
// cannot clear inside a seconds-long backoff ladder — retrying spends two more
// calls to arrive at the same error.
//
// A BYO key's quota is the user's own billing relationship, so this must not
// be dressed up as Reliant-managed credit exhaustion.
func TestGeminiGenerateImage_QuotaExhaustionIsTerminalAndNotManaged(t *testing.T) {
	attempts := 0
	client := newGeminiTestClient(t, Config{
		ModelID: "gemini-3.1-flash-image", APIModel: "gemini-3.1-flash-image", Driver: "gemini",
		RetryBaseDelay: time.Millisecond,
	}, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Quota exceeded","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier"}]}]}}`))
	})

	_, err := client.GenerateImage(context.Background(), Request{Prompt: "x"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if attempts != 1 {
		t.Errorf("quota exhaustion retried %d times; a daily quota is terminal within a backoff ladder", attempts-1)
	}
	if strings.Contains(err.Error(), "RELIANT_MANAGED_QUOTA_EXHAUSTED") {
		t.Errorf("a BYO key's own quota error must not be reported as managed-credit exhaustion, got: %v", err)
	}
}

// TestGeminiGenerateImage_RetriesTransientGatewayErrors pins that the native
// client shares the OpenAI-shaped client's retry ladder rather than inheriting
// the genai SDK's own (which is off unless RetryOptions is set).
func TestGeminiGenerateImage_RetriesTransientGatewayErrors(t *testing.T) {
	want := pngBytes(t)
	attempts := 0

	client := newGeminiTestClient(t, Config{
		ModelID: "gemini-3.1-flash-image", APIModel: "gemini-3.1-flash-image", Driver: "gemini",
		RetryBaseDelay: time.Millisecond,
	}, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":503,"status":"UNAVAILABLE","message":"upstream busy"}}`))
			return
		}
		_, _ = w.Write([]byte(geminiEnvelope(geminiInlineDataPart("image/png", want))))
	})

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

// TestGeminiGenerateImage_EmptyPromptIsRejectedWithoutCallingProvider matches
// the guard the OpenAI-shaped client already makes, so the two clients agree
// on what a caller may send.
func TestGeminiGenerateImage_EmptyPromptIsRejectedWithoutCallingProvider(t *testing.T) {
	called := false
	client := newGeminiTestClient(t, Config{
		ModelID: "gemini-3.1-flash-image", APIModel: "gemini-3.1-flash-image", Driver: "gemini",
	}, func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	if _, err := client.GenerateImage(context.Background(), Request{Prompt: "  "}); err == nil {
		t.Fatal("expected an error for an empty prompt")
	}
	if called {
		t.Error("provider was called for an empty prompt")
	}
}

// TestNewGemini_RequiresAnAPIKey pins that a missing credential fails at
// construction with a message naming the cause, rather than at call time as an
// opaque 401 from Google.
func TestNewGemini_RequiresAnAPIKey(t *testing.T) {
	if _, err := NewGemini(Config{ModelID: "gemini-3.1-flash-image", Driver: "gemini"}, genai.NewClient); err == nil {
		t.Fatal("expected an error when no API key is configured")
	}
}

// TestNewGemini_UsesTheInjectedSDKConstructor pins the seam that keeps this
// package off genai.NewClient. The repo requires every SDK client to come from
// llm.NewGenAISDKClient so it gets a response-body idle timeout; this package
// cannot import internal/llm without a cycle, so the constructor is injected
// and must actually be the one used.
func TestNewGemini_UsesTheInjectedSDKConstructor(t *testing.T) {
	called := false
	factory := func(ctx context.Context, config *genai.ClientConfig) (*genai.Client, error) {
		called = true
		if config.Backend != genai.BackendGeminiAPI {
			t.Errorf("backend = %v, want the Gemini API (AI Studio) backend", config.Backend)
		}
		if config.APIKey != "AIzaSyTestKey" {
			t.Errorf("APIKey = %q, want the configured key", config.APIKey)
		}
		return genai.NewClient(ctx, config)
	}

	if _, err := NewGemini(Config{APIKey: "AIzaSyTestKey", Driver: "gemini"}, factory); err != nil {
		t.Fatalf("NewGemini: %v", err)
	}
	if !called {
		t.Error("NewGemini bypassed the injected constructor; the idle-timeout HTTP client would be lost")
	}
}

// TestGeminiAspectRatio_UnknownSizeIsOmittedRatherThanForwarded pins the
// translation's failure mode. The SDK rejects an unrecognized aspectRatio
// outright, so forwarding "4096x4096" would turn a cosmetic mismatch into a
// failed generation; dropping it lets the model apply its own default.
func TestGeminiAspectRatio_UnknownSizeIsOmittedRatherThanForwarded(t *testing.T) {
	for _, size := range []string{"", "auto", "4096x4096", "nonsense"} {
		if got := geminiAspectRatio(size); got != "" {
			t.Errorf("geminiAspectRatio(%q) = %q, want \"\" so the field is omitted", size, got)
		}
	}
	for size, want := range map[string]string{
		"1024x1024": "1:1",
		"1536x1024": "3:2",
		"1024x1536": "2:3",
	} {
		if got := geminiAspectRatio(size); got != want {
			t.Errorf("geminiAspectRatio(%q) = %q, want %q", size, got, want)
		}
	}
}

// TestGeminiClient_SatisfiesTheSameCallSurfaceAsTheOpenAIClient pins the
// substitution the whole design rests on: ResolveImageGenerator returns either
// client, and the generate_image tool calls it through a one-method interface
// over GenerateImage. If the two signatures ever diverge, this stops
// compiling — which is the point.
func TestGeminiClient_SatisfiesTheSameCallSurfaceAsTheOpenAIClient(t *testing.T) {
	type imageGenerator interface {
		GenerateImage(ctx context.Context, request Request) (*Response, error)
	}

	var openAIShaped imageGenerator = newTestClient(t, Config{BaseURL: "https://example.test/v1"})
	var native imageGenerator = newGeminiWithModels(Config{}, nil)

	if openAIShaped == nil || native == nil {
		t.Fatal("both clients must satisfy the tool's one-method call surface")
	}
}

// TestGeminiClient_FakeSDKNeedsNoNetwork pins that geminiImageModels is a real
// seam: a caller can substitute the SDK entirely. This is what keeps a live,
// billed API call out of the test suite.
func TestGeminiClient_FakeSDKNeedsNoNetwork(t *testing.T) {
	want := pngBytes(t)
	fake := &fakeGeminiModels{response: &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
			{InlineData: &genai.Blob{MIMEType: "image/png", Data: want}},
		}}}},
	}}

	client := newGeminiWithModels(Config{
		ModelID: "gemini-3-pro-image", APIModel: "gemini-3-pro-image", Driver: "gemini",
	}, fake)

	resp, err := client.GenerateImage(context.Background(), Request{Prompt: "a blue circle"})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if fake.gotModel != "gemini-3-pro-image" {
		t.Errorf("called model %q, want the provider api_model", fake.gotModel)
	}
	if len(resp.Images) != 1 || !bytes.Equal(resp.Images[0].Bytes, want) {
		t.Fatal("fake SDK response did not decode to the expected image")
	}
}

type fakeGeminiModels struct {
	response *genai.GenerateContentResponse
	err      error
	gotModel string
}

func (f *fakeGeminiModels) GenerateContent(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	f.gotModel = model
	return f.response, f.err
}

// TestGeminiGenerateImage_UnspecifiedFinishReasonIsRetried pins that
// FINISH_REASON_UNSPECIFIED is treated as NO explanation rather than as a
// refusal.
//
// It is the proto enum's zero value — literally "the provider did not say" —
// so it is indistinguishable in meaning from an omitted field. Treating it as
// an explanation would make it terminal, and the intermittent upstream drop
// (the one failure a retry reliably fixes) would fail permanently whenever the
// provider sent the placeholder instead of omitting the field.
func TestGeminiGenerateImage_UnspecifiedFinishReasonIsRetried(t *testing.T) {
	want := pngBytes(t)
	var attempts int
	client := newGeminiTestClient(t, Config{
		ModelID: "gemini-3.1-flash-image", APIModel: "gemini-3.1-flash-image", Driver: "gemini",
		RetryBaseDelay: time.Millisecond,
	}, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			_, _ = w.Write([]byte(`{"candidates":[{"finishReason":"FINISH_REASON_UNSPECIFIED","content":{"parts":[]}}]}`))
			return
		}
		_, _ = w.Write([]byte(geminiEnvelope(geminiInlineDataPart("image/png", want))))
	})

	resp, err := client.GenerateImage(context.Background(), Request{Prompt: "x"})
	if err != nil {
		t.Fatalf("an unspecified finish reason explains nothing and must be retried, not returned as terminal: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want exactly 2 (one retry)", attempts)
	}
	if len(resp.Images) != 1 {
		t.Errorf("images = %d, want 1 from the successful retry", len(resp.Images))
	}
}

// TestGeminiGenerateImage_RealRefusalStaysTerminal is the counterweight: a
// genuine refusal must NOT become retryable as a side effect of the above. A
// content filter decides the same way twice, so a retry only bills the user
// again for the same "no".
func TestGeminiGenerateImage_RealRefusalStaysTerminal(t *testing.T) {
	var attempts int
	client := newGeminiTestClient(t, Config{
		ModelID: "gemini-3.1-flash-image", APIModel: "gemini-3.1-flash-image", Driver: "gemini",
		RetryBaseDelay: time.Millisecond,
	}, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		_, _ = w.Write([]byte(`{"candidates":[{"finishReason":"IMAGE_PROHIBITED_CONTENT","content":{"parts":[]}}]}`))
	})

	_, err := client.GenerateImage(context.Background(), Request{Prompt: "x"})
	if err == nil {
		t.Fatal("expected a terminal error for an explained refusal")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want exactly 1 — an explained refusal must not be retried", attempts)
	}
	if !strings.Contains(err.Error(), "IMAGE_PROHIBITED_CONTENT") {
		t.Errorf("error must name the refusal reason, got: %v", err)
	}
}
