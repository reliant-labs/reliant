// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3"
	"google.golang.org/genai"

	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
)

// openrouterStatusRE matches the status the OpenRouter driver puts in its error text, which
// carries no typed error.
var openrouterStatusRE = regexp.MustCompile(`status (401|403)\b`)

// CredentialRejected reports whether err is a provider refusing the credential itself: a 401 or
// 403 from the Anthropic, OpenAI, Gemini or OpenRouter API. Retrying the same key cannot
// succeed. Billing and quota refusals are not credential verdicts and report false.
// A 401 that an OAuth session recovers by refreshing is the token coordinator's to judge, not
// this function's: callers apply it to API-key accounts.
func CredentialRejected(err error) bool {
	if err == nil || drivererrors.IsProviderCreditExhaustion(err) {
		return false
	}
	var anth *anthropic.Error
	if errors.As(err, &anth) {
		return rejectedStatus(anth.StatusCode)
	}
	var oai *openai.Error
	if errors.As(err, &oai) {
		return rejectedStatus(oai.StatusCode)
	}
	var gen genai.APIError
	if errors.As(err, &gen) {
		return rejectedStatus(gen.Code)
	}
	var genPtr *genai.APIError
	if errors.As(err, &genPtr) && genPtr != nil {
		return rejectedStatus(genPtr.Code)
	}
	return openrouterStatusRE.MatchString(err.Error())
}

func rejectedStatus(code int) bool {
	return code == http.StatusUnauthorized || code == http.StatusForbidden
}
