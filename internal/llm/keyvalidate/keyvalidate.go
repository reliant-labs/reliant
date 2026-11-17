// Copyright (c) 2025 Reliant Labs

// Package keyvalidate checks that a pasted provider API key works, by asking the provider's driver
// to validate it. Settings and the account service both call it before storing a key.
//
//forge:exclude-contract: one stateless function over the driver registry; callers declare their own one-method func dep
package keyvalidate

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
)

// redacted replaces a key wherever a message or log line would otherwise carry it.
const redacted = "[redacted]"

// buildDriver builds the driver a key is checked with: drivers.GetDriverForModel in production,
// a stand-in in tests (validate's seam).
type buildDriver func(model models.Model, driverID models.Family, opts ...llm.DriverOption) (llm.Driver, error)

// Validate reports whether apiKey works for provider, with a message fit to show the user. The
// message is returned to the caller of an rpc (AddOrgProviderAPIKey, the settings and account
// services), so it never carries the key: a provider or driver error that quotes it is redacted,
// in the message and in the log line alike.
func Validate(ctx context.Context, provider models.Family, apiKey string) (bool, string) {
	return validate(ctx, provider, apiKey, drivers.GetDriverForModel)
}

func validate(ctx context.Context, provider models.Family, apiKey string, build buildDriver) (bool, string) {
	registry := models.MustGetRegistry()
	providerDefs := registry.ListModelsByProvider(string(provider))
	if len(providerDefs) == 0 {
		return false, fmt.Sprintf("No models found for provider: %s", provider)
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
		msg := redact(err.Error(), apiKey, resolvedAPIKey)
		logging.ErrorContext(ctx, "Failed to create driver for validation", "provider", provider, "error", msg)
		return false, "Failed to initialize provider: " + msg
	}

	err = driver.ValidateKey(ctx)
	if err != nil {
		msg := redact(err.Error(), apiKey, resolvedAPIKey)
		errStr := strings.ToLower(msg)
		if strings.Contains(errStr, "unauthorized") ||
			strings.Contains(errStr, "authentication") ||
			strings.Contains(errStr, "invalid") ||
			strings.Contains(errStr, "api key") ||
			strings.Contains(errStr, "401") {
			return false, "Invalid API key"
		}
		logging.WarnContext(ctx, "API key validation error (may not be auth-related)", "provider", provider, "error", msg)
		return false, "Validation failed: " + msg
	}

	return true, "API key is valid"
}

// redact is s with every non-empty key in keys replaced by redacted. Both the key as pasted and as
// sent (the resolved bearer) are named, since an error may quote either; surrounding whitespace is
// trimmed so a pasted key with a trailing newline still matches what the driver sent.
func redact(s string, keys ...string) string {
	for _, k := range keys {
		if k = strings.TrimSpace(k); k != "" {
			s = strings.ReplaceAll(s, k, redacted)
		}
	}
	return s
}
