// Copyright (c) 2025 Reliant Labs

// Package keyvalidate checks that a pasted provider API key works, by asking the provider's driver
// to validate it. Settings and the account service both call it before storing a key.
//
//forge:exclude-contract: one stateless function over the driver registry; callers declare their own one-method func dep
package keyvalidate

import (
	"context"
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

// redacted replaces a key wherever a log line would otherwise carry it.
const redacted = "[redacted]"

// redactRun is the shortest run of a key's characters a log line may not carry: a provider that
// echoes part of the key (a prefix, a masked middle) still leaves no run this long.
const redactRun = 8

// buildDriver builds the driver a key is checked with: drivers.GetDriverForModel in production,
// a stand-in in tests (validate's seam).
type buildDriver func(model models.Model, driverID models.Family, opts ...llm.DriverOption) (llm.Driver, error)

// Validate reports whether apiKey works for provider, with one of the fixed Msg* messages, fit to
// show the user (Reason maps it to a reason code). A driver's or provider's error is never
// returned; it is logged with the key redacted.
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
		logging.ErrorContext(ctx, "Failed to create driver for validation", "provider", provider,
			"error", redact(err.Error(), apiKey, resolvedAPIKey))
		return false, MsgInitFailed
	}

	err = driver.ValidateKey(ctx)
	if err != nil {
		msg := classify(err.Error())
		logging.WarnContext(ctx, "API key validation failed", "provider", provider, "reason", Reason(msg),
			"error", redact(err.Error(), apiKey, resolvedAPIKey))
		return false, msg
	}

	return true, MsgValid
}

// redact is s with every run of redactRun or more characters of any key in keys replaced by
// redacted. Both the key as pasted and as sent (the resolved bearer) are named, since an error may
// quote either, whole or in part; surrounding whitespace is trimmed first.
func redact(s string, keys ...string) string {
	trimmed := make([]string, 0, len(keys))
	for _, k := range keys {
		if k = strings.TrimSpace(k); k != "" {
			trimmed = append(trimmed, k)
		}
	}
	// Whole keys first, then their runs: a run of one key that another shares must not split the
	// other before it is matched whole.
	for _, k := range trimmed {
		s = strings.ReplaceAll(s, k, redacted)
	}
	for _, k := range trimmed {
		for i := 0; i+redactRun <= len(k); i++ {
			s = strings.ReplaceAll(s, k[i:i+redactRun], redacted)
		}
	}
	return s
}
