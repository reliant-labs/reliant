// Copyright (c) 2025 Reliant Labs

// Package keyvalidate checks that a pasted provider API key works, by asking the provider's driver
// to validate it. Settings and the account service both call it before storing a key.
//
//forge:exclude-contract: one stateless function over the driver registry; callers declare their own one-method func dep
package keyvalidate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
)

// The messages Validate returns. They are fixed text, never a provider's or driver's error:
// Validate's message is returned to the caller of an rpc (AddOrgProviderAPIKey, AddProviderAPIKey,
// ValidateProviderAPIKey), and a provider diagnostic can quote the key it was sent. Each class keeps
// the words the web's key-error classifier (frontends/web/.../onboardingErrors.ts describeKeyError)
// matches, so a client of any version still tells "no credit" from "invalid" from "rate limited".
const (
	MsgValid       = "API key is valid"
	MsgInvalid     = "Invalid API key"
	MsgNoCredit    = "The provider accepted the key, but its account has no credit or quota left (402 Payment Required)"
	MsgRateLimited = "The provider's rate limit was reached while checking the key (429); try again shortly"
	MsgDataPolicy  = "No endpoints found matching your data policy"
	MsgCheckFailed = "Could not check the API key with the provider; try again later"
	MsgInitFailed  = "Could not set up the provider to check the API key"
	MsgNoModels    = "This provider has no models to check the API key with"
)

// Reasons, one per failure message (Reason): what a caller attaches to its InvalidArgument.
//
//nolint:gosec // G101: reason codes that name the API-key check's outcome, not credentials.
const (
	ReasonInvalid     = "api_key_invalid"
	ReasonNoCredit    = "api_key_no_credit"
	ReasonRateLimited = "api_key_rate_limited"
	ReasonDataPolicy  = "api_key_data_policy"
	ReasonCheckFailed = "api_key_check_failed"
	ReasonInitFailed  = "api_key_check_unavailable"
	ReasonNoModels    = "api_key_provider_unsupported"
)

// Reason is the reason code of a message Validate returned, "" for MsgValid or any other text.
func Reason(msg string) string {
	switch msg {
	case MsgInvalid:
		return ReasonInvalid
	case MsgNoCredit:
		return ReasonNoCredit
	case MsgRateLimited:
		return ReasonRateLimited
	case MsgDataPolicy:
		return ReasonDataPolicy
	case MsgCheckFailed:
		return ReasonCheckFailed
	case MsgInitFailed:
		return ReasonInitFailed
	case MsgNoModels:
		return ReasonNoModels
	}
	return ""
}

// classify is the fixed message for a provider's refusal of the key check, read from its error
// text, which is never returned. "No credit" is read before "invalid" because providers report an
// unfunded account in bodies that also say invalid_request_error, and before "rate limit" because
// OpenAI reports exhausted quota as a 429 (the same order as the web's classifier).
func classify(errText string) string {
	e := strings.ToLower(errText)
	has := func(subs ...string) bool {
		for _, sub := range subs {
			if strings.Contains(e, sub) {
				return true
			}
		}
		return false
	}
	switch {
	case has("no endpoints found matching your data policy"):
		return MsgDataPolicy
	case has("402", "payment required", "insufficient credits", "insufficient_quota", "credit balance is too low", "quota", "billing"):
		return MsgNoCredit
	case has("unauthorized", "authentication", "invalid", "api key", "401"):
		return MsgInvalid
	case has("rate limit", "429"):
		return MsgRateLimited
	}
	return MsgCheckFailed
}

// buildDriver builds the driver a key is checked with: drivers.GetDriverForModel in production,
// a stand-in in tests (validate's seam).
type buildDriver func(model models.Model, driverID models.Family, opts ...llm.DriverOption) (llm.Driver, error)

// Validate reports whether apiKey works for provider, with one of the fixed Msg* messages, fit to
// show the user (Reason maps it to a reason code). A driver's or provider's error is never
// returned, and never logged either: the log line names only the reason, the HTTP status and the
// error's types (errorFacts), so no key, whole or in part, can reach it.
func Validate(ctx context.Context, provider models.Family, apiKey string) (bool, string) {
	return validate(ctx, provider, apiKey, drivers.GetDriverForModel)
}

func validate(ctx context.Context, provider models.Family, apiKey string, build buildDriver) (bool, string) {
	registry := models.MustGetRegistry()
	providerDefs := registry.ListModelsByProvider(string(provider))
	if len(providerDefs) == 0 {
		return false, MsgNoModels
	}
	testModel := providerDefs[0].ToModel()

	resolvedAPIKey := apiKey
	driverOpts := []llm.DriverOption{
		llm.WithMaxTokens(100),
	}
	if provider == "reliant" {
		baseURL := drivers.ResolveReliantBaseURL(apiKey)
		var extraHeaders map[string]string
		resolvedAPIKey, extraHeaders = drivers.ResolveReliantAPIKey(apiKey, baseURL)
		driverOpts = append(driverOpts, llm.WithBaseURL(baseURL))
		if len(extraHeaders) > 0 {
			driverOpts = append(driverOpts, llm.WithExtraHeaders(extraHeaders))
		}
	}
	driverOpts = append(driverOpts, llm.WithAPIKey(resolvedAPIKey))
	driver, err := build(testModel, provider, driverOpts...)
	if err != nil {
		logging.ErrorContext(ctx, "Failed to create driver for validation",
			append([]any{"provider", provider, "reason", Reason(MsgInitFailed)}, errorFacts(err)...)...)
		return false, MsgInitFailed
	}

	err = driver.ValidateKey(ctx)
	if err != nil {
		msg := classify(err.Error())
		logging.WarnContext(ctx, "API key validation failed",
			append([]any{"provider", provider, "reason", Reason(msg)}, errorFacts(err)...)...)
		return false, msg
	}

	return true, MsgValid
}

// httpStatuses are the statuses errorFacts reports: the ones a provider answers a key check with.
var httpStatuses = []string{"400", "401", "402", "403", "404", "408", "409", "413", "422", "429", "500", "502", "503", "504", "529"}

// errorFacts is what a log line may say about a failed key check: the HTTP status the error names
// (one of httpStatuses, or none) and the Go types of its chain. Never the error's text: a provider
// diagnostic can quote the key, whole or in part, in any encoding, and no fragment matching makes
// that safe.
func errorFacts(err error) []any {
	status := ""
	text := err.Error()
	for _, code := range httpStatuses {
		if containsWord(text, code) {
			status = code
			break
		}
	}
	var types []string
	for e := err; e != nil && len(types) < 8; e = errors.Unwrap(e) {
		types = append(types, fmt.Sprintf("%T", e))
	}
	return []any{"http_status", status, "error_types", strings.Join(types, " > ")}
}

// containsWord reports whether s holds w with no letter or digit on either side.
func containsWord(s, w string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], w)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(w)
		if (start == 0 || !isWordByte(s[start-1])) && (end == len(s) || !isWordByte(s[end])) {
			return true
		}
		i = start + 1
	}
}

func isWordByte(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}
