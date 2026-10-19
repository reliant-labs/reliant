// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/assert"
	"google.golang.org/genai"
)

func anthropicStatus(code int) error {
	req, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	return &anthropic.Error{StatusCode: code, Request: req, Response: &http.Response{StatusCode: code}}
}

func openaiStatus(code int) error {
	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", nil)
	return &openai.Error{StatusCode: code, Request: req, Response: &http.Response{StatusCode: code}}
}

func TestCredentialRejected(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"anthropic 401":          {anthropicStatus(401), true},
		"anthropic 403 wrapped":  {fmt.Errorf("stream llm response: %w", anthropicStatus(403)), true},
		"anthropic 429":          {anthropicStatus(429), false},
		"anthropic 500":          {anthropicStatus(500), false},
		"openai 401":             {openaiStatus(401), true},
		"openai 404":             {openaiStatus(404), false},
		"gemini 403":             {genai.APIError{Code: 403}, true},
		"gemini 400":             {genai.APIError{Code: 400}, false},
		"openrouter 401":         {errors.New("API request failed with status 401: no auth"), true},
		"openrouter 4010 digits": {errors.New("API request failed with status 4010: x"), false},
		"quota is not a verdict": {fmt.Errorf("403 insufficient_quota: %w", openaiStatus(403)), false},
		"plain network error":    {errors.New("dial tcp: connection refused"), false},
		"nil":                    {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, CredentialRejected(tc.err))
		})
	}
}
