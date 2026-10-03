// Copyright (c) 2025 Reliant Labs
package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// The gate is asserted on the marshaled request body rather than the Go struct:
// the field is `omitzero`, so "absent" is a property of the wire body and a
// struct comparison would not distinguish it from the zero value.
const retention24h = `"prompt_cache_retention":"24h"`

func marshaledBody(t *testing.T, params any) string {
	t.Helper()
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return string(body)
}

func clientFor(apiModel string, mutate func(*llm.DriverOptions)) *OpenaiClient {
	opts := llm.DriverOptions{Model: models.Model{APIModel: apiModel}}
	if mutate != nil {
		mutate(&opts)
	}
	return &OpenaiClient{Options: opts}
}

func TestPromptCacheRetention(t *testing.T) {
	tests := []struct {
		name     string
		apiModel string
		mutate   func(*llm.DriverOptions)
		want     bool
	}{
		{name: "allowlisted model on the OpenAI platform", apiModel: "gpt-5.5", want: true},

		// Not on OpenAI's documented extended-retention list.
		{name: "model off the allowlist", apiModel: "gpt-5.4-pro"},
		{name: "model that replaced the field", apiModel: "gpt-6-sol"},

		{name: "caching disabled", apiModel: "gpt-5.5", mutate: func(o *llm.DriverOptions) {
			o.DisableCache = true
		}},
		// Copilot's OpenAI dialect reaches this client with its own host set.
		{name: "custom host", apiModel: "gpt-5.5", mutate: func(o *llm.DriverOptions) {
			o.BaseURL = "https://api.individual.githubcopilot.com"
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := clientFor(tt.apiModel, tt.mutate)

			chat := marshaledBody(t, client.preparedParams(nil, nil))
			if got := strings.Contains(chat, retention24h); got != tt.want {
				t.Errorf("chat completions body contains %s = %v, want %v", retention24h, got, tt.want)
			}

			// The Responses input carries the full system prompt, so the body
			// is not worth printing on failure.
			resp := marshaledBody(t, client.responsesParams(nil, nil))
			if got := strings.Contains(resp, retention24h); got != tt.want {
				t.Errorf("responses body contains %s = %v, want %v", retention24h, got, tt.want)
			}
		})
	}
}
