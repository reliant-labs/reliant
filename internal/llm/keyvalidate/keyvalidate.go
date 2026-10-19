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

// Validate reports whether apiKey works for provider, with a message fit to show the user.
func Validate(ctx context.Context, provider models.Family, apiKey string) (bool, string) {
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
	driver, err := drivers.GetDriverForModel(testModel, provider, driverOpts...)
	if err != nil {
		logging.Error("Failed to create driver for validation", "provider", provider, "error", err)
		return false, fmt.Sprintf("Failed to initialize provider: %v", err)
	}

	err = driver.ValidateKey(ctx)
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "unauthorized") ||
			strings.Contains(errStr, "authentication") ||
			strings.Contains(errStr, "invalid") ||
			strings.Contains(errStr, "api key") ||
			strings.Contains(errStr, "401") {
			return false, "Invalid API key"
		}
		logging.Warn("API key validation error (may not be auth-related)", "provider", provider, "error", err)
		return false, fmt.Sprintf("Validation failed: %v", err)
	}

	return true, "API key is valid"
}
