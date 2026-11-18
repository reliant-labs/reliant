// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3"
	"google.golang.org/genai"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
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
	// An org policy's verdict (orgpolicy.Refusal) is the org's, never the
	// key's: a member's key is not rejected because their org forbids it.
	if err == nil || drivererrors.IsProviderCreditExhaustion(err) || orgpolicy.IsRefusal(err) {
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

// OAuthRefreshedStillRejected reports whether err is a provider refusing an
// OAuth session AFTER the driver's own recovery refreshed it: the driver marked
// the response (llm.MarkOAuthRefreshedStillRejected), which today only the
// Claude subscription driver's request path does. Any other refusal — a driver
// that does not refresh on a 401, or a 401 its recovery never got to — is
// false, and keeps whatever handling it had.
func OAuthRefreshedStillRejected(err error) bool {
	if !CredentialRejected(err) {
		return false
	}
	var anth *anthropic.Error
	if errors.As(err, &anth) && llm.OAuthRefreshedStillRejectedResponse(anth.Response) {
		return true
	}
	var oai *openai.Error
	return errors.As(err, &oai) && llm.OAuthRefreshedStillRejectedResponse(oai.Response)
}
