// Copyright (c) 2025 Reliant Labs
// Regression tests for tool_choice on OpenRouter's custom request paths.
//
// OpenRouter embeds the OpenAI client, which pins tool_choice through the SDK
// params. But its Gemini and cached-Anthropic paths marshal their own JSON and
// never touch those params, so they used to drop the pin silently. That is not
// hypothetical for chat titling: the "fast" tag resolves to
// anthropic/claude-haiku-4.5 on OpenRouter, which takes the cache-control
// branch. An unpinned title request lets the model answer the user's first
// message in prose instead of calling set_title.
package openrouter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func toolMap(name string) map[string]interface{} {
	return map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name": name,
		},
	}
}

func TestForcedToolChoice(t *testing.T) {
	tests := []struct {
		name      string
		forceName string
		tools     []map[string]interface{}
		wantPin   bool
	}{
		{
			name:      "pins when the named tool is present",
			forceName: "set_title",
			tools:     []map[string]interface{}{toolMap("set_title")},
			wantPin:   true,
		},
		{
			name:      "pins the named tool among several",
			forceName: "set_title",
			tools:     []map[string]interface{}{toolMap("view"), toolMap("set_title")},
			wantPin:   true,
		},
		{
			name:      "no pin when the option is unset",
			forceName: "",
			tools:     []map[string]interface{}{toolMap("set_title")},
			wantPin:   false,
		},
		{
			name:      "no pin when there are no tools",
			forceName: "set_title",
			tools:     nil,
			wantPin:   false,
		},
		{
			// A tool_choice naming a function the request never sent is
			// rejected by the provider, so dropping the pin is correct here.
			name:      "no pin when the named tool is absent",
			forceName: "set_title",
			tools:     []map[string]interface{}{toolMap("view")},
			wantPin:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := forcedToolChoice(tt.forceName, tt.tools)
			if !tt.wantPin {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, "function", got.Type)
			assert.Equal(t, tt.forceName, got.Function.Name)
		})
	}
}
