// Copyright (c) 2025 Reliant Labs
package codex

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3"
)

// The Codex backend reports refusals in a top-level `detail` field:
//
//	{"detail":"The 'gpt-5.3-codex-spark' model is not supported when using
//	 Codex with a ChatGPT account."}
//
// The OpenAI SDK only unwraps `error` (see requestconfig.go: it does
// gjson.GetBytes(contents, "error")), so for a Codex 400 it decodes nothing and
// its Error() renders as a bare:
//
//	POST "https://chatgpt.com/backend-api/codex/responses": 400 Bad Request
//
// with an empty message. That is not a cosmetic problem — it is a debugging
// trap. It cost a full misdiagnosis: a titling failure carrying this exact
// empty 400 was read as "the backend rejects non-streaming requests", and the
// resulting streaming fix shipped while the real cause (an unservable model)
// went untouched and titling kept failing.
//
// AugmentAPIError must surface the detail so the reason travels with the error.
func TestAugmentAPIError_SurfacesCodexDetailBody(t *testing.T) {
	body := `{"detail":"The 'gpt-5.3-codex-spark' model is not supported when using Codex with a ChatGPT account."}`
	err := codexAPIErrorWithBody(t, http.StatusBadRequest, body)

	augmented := AugmentAPIError(err)
	msg := augmented.Error()

	if !strings.Contains(msg, "not supported when using Codex with a ChatGPT account") {
		t.Fatalf("expected the backend's detail to appear in the error, got: %q", msg)
	}
}

// A body the SDK already decodes must not be double-reported.
func TestAugmentAPIError_LeavesStandardErrorBodiesAlone(t *testing.T) {
	body := `{"error":{"message":"invalid api key","type":"invalid_request_error"}}`
	err := codexAPIErrorWithBody(t, http.StatusUnauthorized, body)

	msg := AugmentAPIError(err).Error()
	if strings.Count(msg, "invalid api key") > 1 {
		t.Fatalf("detail must not be appended when the SDK already decoded the body, got: %q", msg)
	}
}

// A non-API error passes through untouched.
func TestAugmentAPIError_PassesThroughNonAPIErrors(t *testing.T) {
	if got := AugmentAPIError(nil); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

// codexAPIErrorWithBody builds the *openai.Error the SDK would produce for a
// Codex response with the given status and body, including the unread body the
// SDK leaves attached for debugging.
func codexAPIErrorWithBody(t *testing.T, status int, body string) *openai.Error {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	resp := &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
		Request:    req,
	}

	apiErr := &openai.Error{Request: req, Response: resp, StatusCode: status}
	// Mirror the SDK: it unwraps only the `error` key, so a Codex `detail`
	// body decodes to nothing.
	_ = apiErr.UnmarshalJSON([]byte(`{}`))
	return apiErr
}
