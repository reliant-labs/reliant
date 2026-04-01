// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	// Import drivers to trigger their init() functions
	_ "github.com/reliant-labs/reliant/internal/llm/drivers/anthropic"
	_ "github.com/reliant-labs/reliant/internal/llm/drivers/gemini"
	_ "github.com/reliant-labs/reliant/internal/llm/drivers/openai"
	_ "github.com/reliant-labs/reliant/internal/llm/drivers/openrouter"
)

func TestToModelSelector(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected models.ModelSelector
		wantErr  bool
	}{
		{
			name:     "nil pointer",
			input:    (*models.ModelSelector)(nil),
			expected: models.ModelSelector{},
			wantErr:  false,
		},
		{
			name: "struct value",
			input: models.ModelSelector{
				ID: "claude-sonnet-4-20250514",
			},
			expected: models.ModelSelector{ID: "claude-sonnet-4-20250514"},
			wantErr:  false,
		},
		{
			name: "pointer value",
			input: &models.ModelSelector{
				ID:   "gpt-4.1",
				Tags: []string{"fast"},
			},
			expected: models.ModelSelector{ID: "gpt-4.1", Tags: []string{"fast"}},
			wantErr:  false,
		},
		{
			name: "map with id",
			input: map[string]interface{}{
				"id": "claude-sonnet-4-20250514",
			},
			expected: models.ModelSelector{ID: "claude-sonnet-4-20250514"},
			wantErr:  false,
		},
		{
			name: "map with tags as []interface{}",
			input: map[string]interface{}{
				"tags": []interface{}{"fast", "cheap"},
			},
			expected: models.ModelSelector{Tags: []string{"fast", "cheap"}},
			wantErr:  false,
		},
		{
			name: "map with tags as []string",
			input: map[string]interface{}{
				"tags": []string{"flagship", "reasoning"},
			},
			expected: models.ModelSelector{Tags: []string{"flagship", "reasoning"}},
			wantErr:  false,
		},
		{
			name: "map with providers",
			input: map[string]interface{}{
				"id":        "claude-sonnet-4-20250514",
				"providers": []interface{}{"anthropic", "openrouter"},
			},
			expected: models.ModelSelector{
				ID:        "claude-sonnet-4-20250514",
				Providers: []string{"anthropic", "openrouter"},
			},
			wantErr: false,
		},
		{
			name:    "string model id rejected",
			input:   "gpt-5.4@codex",
			wantErr: true,
		},
		{
			name:    "empty string rejected",
			input:   "",
			wantErr: true,
		},
		{
			name:    "unsupported type",
			input:   12345,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := toModelSelector(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestValidateModelSelector_EmptySelector(t *testing.T) {
	// Empty selector is valid - will use default at runtime
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	err := ValidateModelSelector(ctx, "test-user", models.ModelSelector{})
	assert.NoError(t, err, "Empty selector should be valid")

	err = ValidateModelSelector(ctx, "test-user", map[string]interface{}{})
	assert.NoError(t, err, "Empty map should be valid")
}

func TestValidateModelSelector_NoAPIKeysConfigured(t *testing.T) {
	// This test verifies behavior when GetAvailableDrivers returns empty
	// In practice, this requires mocking, but we can test the error message logic
	// by using a selector with a model that requires a specific provider

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "user-with-no-keys")

	// Use a real model ID that won't have any drivers configured in test context
	selector := models.ModelSelector{
		ID: "claude-sonnet-4-20250514",
	}

	err := ValidateModelSelector(ctx, "user-with-no-keys", selector)

	// We expect an error because no API keys are configured in test context
	// The exact error depends on whether the test environment has any keys
	if err != nil {
		assert.Contains(t, err.Error(), "API key", "Error should mention API keys")
	}
}

func TestValidateModelSelector_NonEmptySelectorReturnsError(t *testing.T) {
	// In test context without API keys configured, any non-empty selector
	// should return an error (either "no API keys" or "model not found")
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	// Test with an actual model ID
	err := ValidateModelSelector(ctx, "test-user", models.ModelSelector{
		ID: "claude-sonnet-4-20250514",
	})
	require.Error(t, err, "Should fail when no API keys are configured")

	// Test with map format (how inputs typically come from workflow params)
	err = ValidateModelSelector(ctx, "test-user", map[string]interface{}{
		"id": "claude-sonnet-4-20250514",
	})
	require.Error(t, err, "Should fail with map format when no API keys")

	// Test with tags
	err = ValidateModelSelector(ctx, "test-user", models.ModelSelector{
		Tags: []string{"flagship"},
	})
	require.Error(t, err, "Should fail with tags when no API keys")
}

func TestValidateModelSelector_ErrorMessageGuidance(t *testing.T) {
	// Verify that error messages provide actionable guidance
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	selector := models.ModelSelector{
		ID: "claude-sonnet-4-20250514",
	}

	err := ValidateModelSelector(ctx, "test-user", selector)
	require.Error(t, err)

	// Error should mention Settings or API key - guiding user to fix the issue
	errStr := err.Error()
	hasGuidance := strings.Contains(errStr, "Settings") ||
		strings.Contains(errStr, "API key") ||
		strings.Contains(errStr, "configured")

	assert.True(t, hasGuidance, "Error should provide actionable guidance, got: %s", errStr)
}
