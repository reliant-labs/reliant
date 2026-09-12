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

	openaisdk "github.com/openai/openai-go/v3"
	openaiopt "github.com/openai/openai-go/v3/option"
)

// This file covers the guarantees that became load-bearing when this client
// moved onto the official openai-go SDK. Each one is a place where the SDK's
// default is WRONG for image generation, so a future edit that drops the
// override would silently reintroduce a real defect — an extra billed call, a
// missing Codex header, an unexplainable decode failure.

// TestSDKRetriesAreDisabled_SoTheLadderIsNotMultiplied is the most expensive
// mistake this port could make, and it is invisible without a test.
//
// The SDK retries a 503 twice on its own (three HTTP calls per attempt,
// verified against a live-shaped server), while this package runs its own
// ladder of up to three attempts. Composed, one GenerateImage would issue up to
// NINE requests to the provider instead of three — and for the empty-data case,
// which is deliberately capped at one extra attempt because each retry bills a
// full generation, the SDK knows nothing of the cap and would bill straight
// through it.
//
// The assertion is on the raw HTTP count, not on our attempt count, because
// that is where the multiplication would show up.
func TestSDKRetriesAreDisabled_SoTheLadderIsNotMultiplied(t *testing.T) {
	httpCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream busy"}}`))
	}))
	defer server.Close()

	client := newTestClient(t, Config{
		BaseURL: server.URL + "/v1", APIModel: "gpt-image-2", Driver: "openai",
		RetryBaseDelay: time.Millisecond,
	})
	if _, err := client.GenerateImage(context.Background(), Request{Prompt: "x"}); err == nil {
		t.Fatal("expected an error after the ladder was exhausted")
	}

	// defaultMaxAttempts attempts, one HTTP call each. The SDK must add none.
	if httpCalls != defaultMaxAttempts {
		t.Errorf("the provider was called %d times for %d attempts; the SDK's own retries must be disabled "+
			"so they do not multiply with this package's ladder (and bill through the empty-result cap)",
			httpCalls, defaultMaxAttempts)
	}
}

// TestEmptyResultCapIsNotMultipliedBySDKRetries is the same guarantee on the
// path where it costs real money: an empty data[] means the provider generated
// and BILLED an image (~1120 output tokens observed) and then failed to return
// it. The cap of one extra attempt is a spend decision, so the HTTP count must
// match it exactly.
func TestEmptyResultCapIsNotMultipliedBySDKRetries(t *testing.T) {
	httpCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[],"usage":{"output_tokens":1120}}`))
	}))
	defer server.Close()

	client := newTestClient(t, Config{
		BaseURL: server.URL + "/v1", APIModel: "gemini-3-pro-image", Driver: "reliant",
		RetryBaseDelay: time.Millisecond,
	})
	if _, err := client.GenerateImage(context.Background(), Request{Prompt: "x"}); err == nil {
		t.Fatal("expected an error once the retry also came back empty")
	}

	if httpCalls != emptyResultMaxAttempts {
		t.Errorf("a billed empty result cost %d provider calls, want exactly %d; "+
			"every retry here is money for an image nobody receives", httpCalls, emptyResultMaxAttempts)
	}
}

// TestCodexIdentityHeadersSurviveTheSDK pins the complete header set from the
// request that returned a verified live HTTP 200. The Codex backend gates on
// these, so any one of them going missing turns a working path into a rejection
// that no unit test would otherwise catch.
//
// The interesting one is user-agent. The SDK sets its own ("OpenAI/Go x.y.z")
// as a default, so this only passes because caller headers are applied AFTER
// the SDK's defaults and with Set rather than Add semantics. Swap that ordering
// and Codex sees the wrong client.
func TestCodexIdentityHeadersSurviveTheSDK(t *testing.T) {
	const (
		accountID  = "3eddf627-dcc9-461a-98b2-cd84140abf91"
		originator = "Codex Desktop"
		version    = "0.147.0-alpha.6.5"
		userAgent  = "Codex Desktop/0.147.0-alpha.6.5 (Mac OS 14.3.0; arm64) unknown (Codex Desktop; 26.803.41515)"
	)

	var got http.Header
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, gotPath = r.Header.Clone(), r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":[{"b64_json":%q}]}`,
			base64.StdEncoding.EncodeToString(pngBytes(t)))
	}))
	defer server.Close()

	client := newTestClient(t, Config{
		// Mirrors the real base URL's shape: it ends in /codex, not /v1.
		BaseURL: server.URL + "/backend-api/codex",
		APIKey:  "codex-oauth-access-token",
		ExtraHeaders: map[string]string{
			"chatgpt-account-id": accountID,
			"originator":         originator,
			"version":            version,
			"user-agent":         userAgent,
		},
		ModelID: "gpt-image-2", APIModel: "gpt-image-2", Driver: "codex",
	})

	if _, err := client.GenerateImage(context.Background(), Request{
		Prompt: "a beautiful, friendly dog", Size: "auto", Quality: "auto", Background: "auto",
	}); err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}

	if gotPath != "/backend-api/codex/images/generations" {
		t.Errorf("posted to %q, want /backend-api/codex/images/generations — the verified live path", gotPath)
	}
	for header, want := range map[string]string{
		"Authorization":      "Bearer codex-oauth-access-token",
		"chatgpt-account-id": accountID,
		"originator":         originator,
		"version":            version,
		"User-Agent":         userAgent,
	} {
		if got.Get(header) != want {
			t.Errorf("%s = %q, want %q; Codex gates on this and rejects the request without it",
				header, got.Get(header), want)
		}
	}

	// A caller-supplied header must REPLACE the SDK's default, not append to
	// it: "OpenAI/Go x, Codex Desktop/y" is not a client Codex recognizes.
	if values := got.Values("User-Agent"); len(values) != 1 {
		t.Errorf("User-Agent sent %d times (%q); the Codex identity must replace the SDK default, not append to it",
			len(values), values)
	}

	// Cookies are deliberately not part of this credential. The SDK must not
	// have acquired a jar that starts sending them.
	if cookie := got.Get("Cookie"); cookie != "" {
		t.Errorf("Cookie header = %q; the Codex image path authenticates with a bearer token and account id only", cookie)
	}
}

// TestMislabeledJSONSuccessBodyIsStillDecoded pins a tolerance the SDK does not
// have on its own: it hard-fails any 2xx whose Content-Type is not
// application/json, before it looks at the bytes.
//
// This path speaks to three different fronts (OpenAI, the Codex ChatGPT
// backend, a LiteLLM proxy). A gateway that omits the header — or lets Go sniff
// it to text/plain, which is what happens when a handler just writes JSON —
// would otherwise turn a perfectly good image into "expected destination type
// of 'string' or '[]byte'", which names nothing a caller can act on.
func TestMislabeledJSONSuccessBodyIsStillDecoded(t *testing.T) {
	want := pngBytes(t)

	for _, contentType := range []string{"", "text/plain; charset=utf-8", "application/json"} {
		t.Run(fmt.Sprintf("content-type=%q", contentType), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if contentType != "" {
					w.Header().Set("Content-Type", contentType)
				}
				_, _ = w.Write([]byte(successBody(t, [][]byte{want}, "")))
			}))
			defer server.Close()

			client := newTestClient(t, Config{
				BaseURL: server.URL + "/v1", APIModel: "gpt-image-2", Driver: "openai",
				RetryBaseDelay: time.Millisecond,
			})
			resp, err := client.GenerateImage(context.Background(), Request{Prompt: "x"})
			if err != nil {
				t.Fatalf("a JSON body must decode regardless of how the gateway labelled it: %v", err)
			}
			if len(resp.Images) != 1 || !bytes.Equal(resp.Images[0].Bytes, want) {
				t.Error("image bytes did not survive the relabelled response")
			}
			if resp.Images[0].MIMEType != "image/png" {
				t.Errorf("MIMEType = %q, want image/png — sniffed from the bytes", resp.Images[0].MIMEType)
			}
		})
	}
}

// TestNonJSONSuccessBodyStillFails guards the tolerance above from becoming
// "accept anything". Relabelling exists so a mislabelled JSON body decodes; a
// success body that is genuinely not JSON must still be an error rather than a
// silent zero-image response.
func TestNonJSONSuccessBodyStillFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body>gateway login page</body></html>`))
	}))
	defer server.Close()

	client := newTestClient(t, Config{
		BaseURL: server.URL + "/v1", APIModel: "gpt-image-2", Driver: "openai",
		RetryBaseDelay: time.Millisecond,
	})
	if _, err := client.GenerateImage(context.Background(), Request{Prompt: "x"}); err == nil {
		t.Fatal("an HTML success body must be an error, not an empty image list")
	}
}

// TestTransportFailureIsNotReportedAsAnAPIError pins the boundary the retry
// ladder depends on. retryDelay only repeats an *APIError, so a connection
// failure — a request that never reached the provider — must NOT be dressed up
// as one with an invented status code.
func TestTransportFailureIsNotReportedAsAnAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	baseURL := server.URL + "/v1"
	server.Close() // nothing is listening now

	client := newTestClient(t, Config{
		BaseURL: baseURL, APIModel: "gpt-image-2", Driver: "openai",
		RetryBaseDelay: time.Millisecond,
	})
	_, err := client.GenerateImage(context.Background(), Request{Prompt: "x"})
	if err == nil {
		t.Fatal("expected a transport error")
	}

	var apiErr *APIError
	if errors.As(err, &apiErr) {
		t.Errorf("a connection failure was reported as *APIError with status %d; "+
			"it never reached the provider, so there is no provider status to report", apiErr.StatusCode)
	}
}

// TestManagedQuotaUpgradeURLSurvivesTheSDK pins that upgrade_url is still read
// out of the provider's error body.
//
// The SDK's typed error has no field for it — it is a control-plane extension,
// not part of the OpenAI schema — so it is recovered from the raw JSON the SDK
// preserved. The frontend opens its upgrade modal off this value, so losing it
// silently downgrades a "buy more credit" prompt into an opaque 429.
func TestManagedQuotaUpgradeURLSurvivesTheSDK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"Free tier quota exceeded","type":"insufficient_quota","code":"insufficient_quota","upgrade_url":"/billing/custom-plan"}}`))
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
	if !strings.Contains(err.Error(), "/billing/custom-plan") {
		t.Errorf("the provider's upgrade_url must reach the frontend so it can open the upgrade modal; got: %v", err)
	}
}

// TestNewRequiresAnSDKConstructor mirrors NewGemini's contract. The constructor
// is injected because this package cannot import internal/llm to reach the
// sanctioned one (that would be a cycle), so a nil constructor is a wiring bug
// worth naming rather than a nil-pointer panic at the first request.
func TestNewRequiresAnSDKConstructor(t *testing.T) {
	if _, err := New(Config{BaseURL: "https://example.test/v1"}, nil); err == nil {
		t.Fatal("expected an error when no SDK client constructor is supplied")
	}
}

// TestNewUsesTheInjectedSDKConstructor proves the seam is real: the constructor
// this package is handed is the one it builds its client from. That is what
// makes drivers' injection of llm.NewOpenAISDKClient — the only constructor
// that installs a response-body idle timeout — actually load-bearing rather
// than decorative.
func TestNewUsesTheInjectedSDKConstructor(t *testing.T) {
	called := false
	factory := func(opts ...openaiopt.RequestOption) openaisdk.Client {
		called = true
		return openaisdk.NewClient(opts...)
	}

	if _, err := New(Config{BaseURL: "https://example.test/v1"}, factory); err != nil {
		t.Fatalf("New: %v", err)
	}
	if !called {
		t.Error("New bypassed the injected constructor; the idle-timeout guard would be silently lost")
	}
}

// TestGenerateImage_FakeSDKNeedsNoNetwork pins that openAIImages is a real
// seam. Together with the Gemini equivalent this is what keeps a live, billed
// API call out of the test suite.
type fakeOpenAIImages struct {
	params   openaisdk.ImageGenerateParams
	response *openaisdk.ImagesResponse
	err      error
}

func (f *fakeOpenAIImages) Generate(ctx context.Context, body openaisdk.ImageGenerateParams, opts ...openaiopt.RequestOption) (*openaisdk.ImagesResponse, error) {
	f.params = body
	return f.response, f.err
}

func TestGenerateImage_FakeSDKNeedsNoNetwork(t *testing.T) {
	want := pngBytes(t)
	fake := &fakeOpenAIImages{response: &openaisdk.ImagesResponse{
		Data: []openaisdk.Image{{
			B64JSON:       base64.StdEncoding.EncodeToString(want),
			RevisedPrompt: "a tidier prompt",
		}},
	}}

	client := newWithImages(Config{
		ModelID: "gpt-image-2", APIModel: "gpt-image-2", Driver: "openai",
	}, fake)

	resp, err := client.GenerateImage(context.Background(), Request{
		Prompt: "a red cube", Size: "1024x1024", Count: 2, OutputFormat: "png",
	})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if len(resp.Images) != 1 || !bytes.Equal(resp.Images[0].Bytes, want) {
		t.Fatal("the faked SDK response did not round-trip")
	}
	if resp.Images[0].RevisedPrompt != "a tidier prompt" {
		t.Errorf("RevisedPrompt = %q", resp.Images[0].RevisedPrompt)
	}

	// The params the SDK was handed are the other half of the contract.
	if fake.params.Prompt != "a red cube" {
		t.Errorf("Prompt = %q", fake.params.Prompt)
	}
	if string(fake.params.Model) != "gpt-image-2" {
		t.Errorf("Model = %q, want the provider-side APIModel", fake.params.Model)
	}
	if fake.params.N.Value != 2 {
		t.Errorf("N = %d, want 2", fake.params.N.Value)
	}
	if string(fake.params.Size) != "1024x1024" {
		t.Errorf("Size = %q", fake.params.Size)
	}
	// An unset optional must stay unset so the SDK omits it and the provider
	// applies its own default rather than rejecting an empty string.
	if string(fake.params.Quality) != "" {
		t.Errorf("Quality = %q; an omitted hint must not be sent", fake.params.Quality)
	}
	if string(fake.params.Background) != "" {
		t.Errorf("Background = %q; an omitted hint must not be sent", fake.params.Background)
	}
}

// TestGenerateParams_ArbitrarySizeReachesTheWire pins that a provider-specific
// value with no SDK constant is still forwarded.
//
// gpt-image-2 accepts arbitrary WIDTHxHEIGHT resolutions, and the SDK's Size is
// a named string type rather than a closed enum, so converting is correct and
// switching on the known constants would not be: an allow-list here would
// silently drop a size the provider supports.
func TestGenerateParams_ArbitrarySizeReachesTheWire(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(successBody(t, [][]byte{pngBytes(t)}, "")))
	}))
	defer server.Close()

	client := newTestClient(t, Config{
		BaseURL: server.URL + "/v1", APIModel: "gpt-image-2", Driver: "openai",
	})
	if _, err := client.GenerateImage(context.Background(), Request{
		Prompt: "x", Size: "1536x864", OutputFormat: "webp",
	}); err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}

	if gotBody["size"] != "1536x864" {
		t.Errorf("size = %v, want 1536x864 forwarded verbatim; the SDK's enum constants are not an allow-list", gotBody["size"])
	}
	if gotBody["output_format"] != "webp" {
		t.Errorf("output_format = %v, want webp", gotBody["output_format"])
	}
}
