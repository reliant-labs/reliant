// Copyright (c) 2025 Reliant Labs
package drivererrors

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Real refusals, verbatim as each driver surfaces them (the SDKs' `POST "url":
// <status> {body}`, the hand-rolled drivers' "status N: body", genai's "Error
// N, Message: ..."). Wording sources: the providers' own error bodies as
// recorded in opencode packages/ai/src/provider-error.ts and its tests, and in
// hermes-agent agent/error_classifier.py; the Anthropic one is a prod error.
const (
	anthropicPromptTooLong = `streaming failed: POST "https://api.anthropic.com/v1/messages": 400 Bad Request (Request-ID: req_011CgRKXPxkD2vDeNMhFvqXb) {"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 1114231 tokens > 1000000 maximum"},"request_id":"req_011CgRKXPxkD2vDeNMhFvqXb"}`
	anthropicInputPlusMax  = "POST \"https://api.anthropic.com/v1/messages\": 400 Bad Request {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"input length and `max_tokens` exceed context limit: 188240 + 21333 > 200000, decrease input length or `max_tokens` and try again\"}}"
	anthropicTooLarge      = `POST "https://api.anthropic.com/v1/messages": 413 Request Entity Too Large {"type":"error","error":{"type":"request_too_large","message":"Request exceeds the maximum allowed number of bytes."}}`
	bedrockInputTooLong    = `ValidationException: Input is too long for requested model.`
	openaiChatOverflow     = `POST "https://api.openai.com/v1/chat/completions": 400 Bad Request {"error":{"message":"This model's maximum context length is 128000 tokens. However, your messages resulted in 130000 tokens. Please reduce the length of the messages.","type":"invalid_request_error","param":"messages","code":"context_length_exceeded"}}`
	openaiResponsesOverfl  = `POST "https://api.openai.com/v1/responses": 400 Bad Request {"error":{"message":"Your input exceeds the context window of this model. Please adjust your input and try again.","type":"invalid_request_error","param":"input","code":"context_length_exceeded"}}`
	geminiOverflow         = `Error 400, Message: The input token count (1200000) exceeds the maximum number of tokens allowed (1048576)., Status: INVALID_ARGUMENT, Details: []`
	openrouterOverflow     = `API request failed with status 400: {"error":{"message":"This endpoint's maximum context length is 200000 tokens. However, you requested about 234543 tokens (202543 of text input, 32000 in the output). Please reduce the length of either one, or use the \"middle-out\" transform to compress your prompt automatically.","code":400}}`
	copilotOverflow        = `API error (status 400): {"error":{"message":"prompt token count of 140000 exceeds the limit of 128000","code":"model_max_prompt_tokens_exceeded"}}`
	llamaCppOverflow       = `POST "http://127.0.0.1:8080/v1/chat/completions": 400 Bad Request {"error":{"code":400,"message":"the request exceeds the available context size, try increasing it","type":"exceed_context_size_error","n_prompt_tokens":40000,"n_ctx":32768}}`
	lmStudioOverflow       = `400 Bad Request: The number of tokens to keep from the initial prompt is greater than the context length.`
	vllmOverflow           = `POST "http://vllm:8000/v1/chat/completions": 400 Bad Request {"object":"error","message":"This model's maximum context length is 32768 tokens. However, you requested 33000 tokens (32000 in the messages, 1000 in the completion). Please reduce the length of the messages or completion.","type":"BadRequestError","code":400}`
	vllmModelLength        = `400 Bad Request: The decoder prompt (length 40000) is longer than the maximum model length of 32768.`

	// Not overflows, though several mention tokens or limits.
	anthropicRateLimit  = `POST "https://api.anthropic.com/v1/messages": 429 Too Many Requests {"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed the rate limit for your organization of 400,000 input tokens per minute."}}`
	anthropicOverloaded = `POST "https://api.anthropic.com/v1/messages": 529 {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
	anthropicOutputCap  = `POST "https://api.anthropic.com/v1/messages": 400 Bad Request {"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: 128000 > 64000, which is the maximum allowed number of output tokens for claude-sonnet-4-5"}}`
	openaiTPM           = `POST "https://api.openai.com/v1/chat/completions": 429 Too Many Requests {"error":{"message":"Request too large for gpt-4o in organization org-abc on tokens per min (TPM): Limit 30000, Requested 50000. The input or output tokens must be reduced in order to run successfully.","type":"tokens","param":null,"code":"rate_limit_exceeded"}}`
	geminiExhausted     = `Error 429, Message: Resource has been exhausted (e.g. check quota)., Status: RESOURCE_EXHAUSTED, Details: []`
	bedrockThrottle     = `ThrottlingException: Too many tokens, please wait before trying again.`
	localOOM            = `400 Bad Request: prefill would require 48 GiB: out of memory (context length 131072)`
	unrelated400        = `POST "https://api.openai.com/v1/chat/completions": 400 Bad Request {"error":{"message":"Invalid value for 'model'","type":"invalid_request_error","code":"invalid_value"}}`
)

func TestIsContextOverflow_ProviderErrors(t *testing.T) {
	t.Parallel()
	for name, tt := range map[string]struct {
		text string
		want bool
	}{
		"anthropic prompt too long":         {anthropicPromptTooLong, true},
		"anthropic input plus max_tokens":   {anthropicInputPlusMax, true},
		"bedrock claude input too long":     {bedrockInputTooLong, true},
		"openai chat context length":        {openaiChatOverflow, true},
		"openai responses context window":   {openaiResponsesOverfl, true},
		"gemini input token count":          {geminiOverflow, true},
		"openrouter endpoint context":       {openrouterOverflow, true},
		"copilot prompt token count":        {copilotOverflow, true},
		"llama.cpp available context size":  {llamaCppOverflow, true},
		"lm studio context length":          {lmStudioOverflow, true},
		"vllm maximum context length":       {vllmOverflow, true},
		"vllm maximum model length":         {vllmModelLength, true},
		"anthropic 429 per-minute limit":    {anthropicRateLimit, false},
		"anthropic 529 overloaded":          {anthropicOverloaded, false},
		"anthropic output cap is not input": {anthropicOutputCap, false},
		"openai 429 TPM request too large":  {openaiTPM, false},
		"gemini 429 resource exhausted":     {geminiExhausted, false},
		"bedrock throttle too many tokens":  {bedrockThrottle, false},
		"local server out of memory":        {localOOM, false},
		"an unrelated 400":                  {unrelated400, false},
		"empty":                             {"", false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsContextOverflow(errors.New(tt.text)), tt.text)
		})
	}
}

// A 429 is never an overflow, and neither is a 5xx, whatever the body says:
// the status decides before the code and the wording (hermes-agent #52261's
// lesson, and opencode's clientScoped rule).
func TestProviderFailure_StatusDecidesFirst(t *testing.T) {
	t.Parallel()
	overflowBody := "prompt is too long: 210000 tokens > 200000 maximum"
	for _, status := range []int{429, 500, 503, 529, 200} {
		assert.False(t, ProviderFailure{Status: status, Codes: []string{"context_length_exceeded"}, Message: overflowBody}.IsContextOverflow(),
			"status %d", status)
	}
	assert.True(t, ProviderFailure{Status: 400, Message: overflowBody}.IsContextOverflow())
	assert.True(t, ProviderFailure{Status: 413, Codes: []string{"request_too_large"}}.IsContextOverflow())
	assert.True(t, ProviderFailure{Message: overflowBody}.IsContextOverflow(), "no status: a mid-stream error")
}

// A structured code decides without any wording, and with wording that would
// not match on its own (Codex's response.failed carries the code; a proxy
// may reword the message).
func TestProviderFailure_CodeDecides(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"context_length_exceeded", "model_context_window_exceeded", "request_too_large",
		"model_max_prompt_tokens_exceeded", "exceed_context_size_error", " Context_Length_Exceeded "} {
		assert.True(t, ProviderFailure{Status: 400, Codes: []string{"invalid_request_error", code}, Message: "bad request"}.IsContextOverflow(), code)
	}
	assert.False(t, ProviderFailure{Status: 400, Codes: []string{"invalid_request_error"}, Message: "bad request"}.IsContextOverflow())
}

func TestMarkContextOverflow_KeepsTextAndIdentity(t *testing.T) {
	t.Parallel()
	cause := errors.New(anthropicPromptTooLong)
	marked := MarkContextOverflow(cause, ProviderFailure{Status: 400, Message: cause.Error()})

	assert.ErrorIs(t, marked, ErrContextOverflow)
	assert.ErrorIs(t, marked, cause, "the provider's own error stays reachable")
	assert.Equal(t, cause.Error(), marked.Error(), "the text is the provider's, unchanged")
	var coe *ContextOverflowError
	if assert.ErrorAs(t, marked, &coe) {
		assert.Equal(t, 1114231, coe.Actual)
		assert.Equal(t, 1000000, coe.Limit)
	}
	// Wrapped by layers above, identity survives.
	assert.True(t, IsContextOverflow(fmt.Errorf("LLM streaming error: %w", marked)))
	// Marking twice is a no-op.
	assert.Same(t, marked, MarkContextOverflow(marked, ProviderFailure{Message: cause.Error()}))
	// Anything else passes through untouched.
	other := errors.New(anthropicRateLimit)
	assert.Same(t, other, MarkContextOverflow(other, ProviderFailure{Status: 429, Message: other.Error()}))
}

// Identity wins over wording: a marked error is an overflow even when a layer
// above wrapped it in text that, read alone, would say otherwise.
func TestIsContextOverflow_IdentityBeatsText(t *testing.T) {
	t.Parallel()
	marked := &ContextOverflowError{Err: errors.New("responses failed: context_length_exceeded: ")}
	assert.True(t, IsContextOverflow(fmt.Errorf("rate limit bookkeeping failed while reporting: %w", marked)))
}

func TestOverflowTokenCounts(t *testing.T) {
	t.Parallel()
	for name, tt := range map[string]struct {
		text          string
		actual, limit int
	}{
		"anthropic":                  {anthropicPromptTooLong, 1114231, 1000000},
		"anthropic input+max":        {anthropicInputPlusMax, 188240, 200000 - 21333},
		"openai chat":                {openaiChatOverflow, 130000, 128000},
		"openrouter requested about": {openrouterOverflow, 234543, 200000},
		"vllm requested":             {vllmOverflow, 33000, 32768},
		"gemini":                     {geminiOverflow, 1200000, 1048576},
		"copilot":                    {copilotOverflow, 140000, 128000},
		"codex states no counts":     {openaiResponsesOverfl, 0, 0},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			actual, limit := OverflowTokenCounts(tt.text)
			assert.Equal(t, tt.actual, actual)
			assert.Equal(t, tt.limit, limit)
		})
	}
}

func TestStatusFromText(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 400, StatusFromText(openaiChatOverflow))
	assert.Equal(t, 429, StatusFromText(anthropicRateLimit))
	assert.Equal(t, 400, StatusFromText(openrouterOverflow))
	assert.Equal(t, 400, StatusFromText(copilotOverflow))
	assert.Equal(t, 429, StatusFromText(geminiExhausted))
	assert.Equal(t, 0, StatusFromText("prompt is too long: 204291 tokens > 200000 maximum"),
		"a token count is not a status")
}
