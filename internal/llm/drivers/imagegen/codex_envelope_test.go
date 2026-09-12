// Copyright (c) 2025 Reliant Labs
package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The Codex image backend returns fields the OpenAI platform API does not
// (generation_id, background, output_format, a token-shaped usage block) and
// omits revised_prompt entirely. This file pins that we parse a REAL capture of
// that envelope rather than a hand-built one that happens to match our struct.
//
// Source: a live HTTP 200 from
// POST https://chatgpt.com/backend-api/codex/images/generations
// with model gpt-image-2. The b64_json payload is substituted with a locally
// generated PNG (the captured one was 992KB), so the shape is verbatim and the
// bytes are still a genuine image for MIME sniffing to work on.
const codexCapturedEnvelope = `{
  "created": 1789013910,
  "background": "opaque",
  "data": [
    {
      "b64_json": "%s",
      "generation_id": "gen_01a08986da017d53b0996c37ce16f14a"
    }
  ],
  "output_format": "png",
  "quality": "low",
  "size": "1024x1024",
  "usage": {
    "input_tokens": 13,
    "output_tokens": 515,
    "output_tokens_details": {"image_tokens": 515},
    "total_tokens": 528
  }
}`

// TestGenerateImage_ParsesCodexCapturedEnvelope is the highest-value test in
// this package: it replays real captured provider output. The unfamiliar
// sibling fields must be ignored rather than rejected, and the absence of
// revised_prompt must not be read as a missing image.
func TestGenerateImage_ParsesCodexCapturedEnvelope(t *testing.T) {
	want := pngBytes(t)
	body := fmt.Sprintf(codexCapturedEnvelope, base64.StdEncoding.EncodeToString(want))

	var gotPath, gotAuth, gotAccountID, gotOriginator string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccountID = r.Header.Get("chatgpt-account-id")
		gotOriginator = r.Header.Get("originator")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	client := newTestClient(t, Config{
		// Mirrors the real base URL's shape: it ends in /codex, not /v1, and
		// the client's own path suffix is what completes it.
		BaseURL: server.URL + "/backend-api/codex",
		APIKey:  "codex-oauth-access-token",
		ExtraHeaders: map[string]string{
			"chatgpt-account-id": "3eddf627-dcc9-461a-98b2-cd84140abf91",
			"originator":         "Codex Desktop",
		},
		ModelID:  "gpt-image-2",
		APIModel: "gpt-image-2",
		Driver:   "codex",
	})

	resp, err := client.GenerateImage(context.Background(), Request{
		Prompt:     "a beautiful, friendly dog",
		Size:       "auto",
		Quality:    "auto",
		Background: "auto",
	})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}

	if gotPath != "/backend-api/codex/images/generations" {
		t.Errorf("posted to %q, want /backend-api/codex/images/generations — the verified live path", gotPath)
	}
	if gotAuth != "Bearer codex-oauth-access-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotAccountID != "3eddf627-dcc9-461a-98b2-cd84140abf91" {
		t.Errorf("chatgpt-account-id = %q; Codex rejects a request without it", gotAccountID)
	}
	if gotOriginator != "Codex Desktop" {
		t.Errorf("originator = %q", gotOriginator)
	}

	if len(resp.Images) != 1 {
		t.Fatalf("got %d images, want 1", len(resp.Images))
	}
	if !bytes.Equal(resp.Images[0].Bytes, want) {
		t.Errorf("image bytes round-tripped incorrectly: got %d bytes, want %d",
			len(resp.Images[0].Bytes), len(want))
	}
	if resp.Images[0].MIMEType != "image/png" {
		t.Errorf("MIMEType = %q, want image/png", resp.Images[0].MIMEType)
	}
	if resp.Images[0].RevisedPrompt != "" {
		t.Errorf("RevisedPrompt = %q; Codex sends no revised_prompt", resp.Images[0].RevisedPrompt)
	}
	if resp.ModelID != "gpt-image-2" || resp.Driver != "codex" {
		t.Errorf("resp model/driver = %q/%q", resp.ModelID, resp.Driver)
	}
}

// TestCodexCapturedEnvelope_KeepsTheVerifiedShape guards the fixture itself.
// If someone "tidies" the capture into the fields our struct happens to read,
// the test above stops proving that unknown provider fields are tolerated —
// which is the only thing it exists to prove.
func TestCodexCapturedEnvelope_KeepsTheVerifiedShape(t *testing.T) {
	body := fmt.Sprintf(codexCapturedEnvelope, base64.StdEncoding.EncodeToString(pngBytes(t)))

	var envelope map[string]any
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("captured envelope is not valid JSON: %v", err)
	}
	for _, field := range []string{"created", "background", "output_format", "quality", "size", "usage"} {
		if _, ok := envelope[field]; !ok {
			t.Errorf("captured envelope lost the %q field; it must stay verbatim from the live response", field)
		}
	}
	if !strings.Contains(body, "generation_id") {
		t.Error("captured envelope lost data[].generation_id, a Codex-only field")
	}
	if strings.Contains(body, "revised_prompt") {
		t.Error("captured envelope gained revised_prompt; the live Codex response has none")
	}
}
