// Copyright (c) 2025 Reliant Labs
package llm

import (
	"context"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	anthropicopt "github.com/anthropics/anthropic-sdk-go/option"
	openaisdk "github.com/openai/openai-go/v3"
	openaiopt "github.com/openai/openai-go/v3/option"
	"google.golang.org/genai"
)

// Every LLM driver reaches its provider through one of three vendor SDKs. Each
// SDK defaults to an http.Client with no DNS resilience, no header timeout and
// — the expensive one — no idle timeout on the response body, so a stream that
// goes silent after headers hangs until a middlebox resets the connection.
//
// The constructors below install StreamingHTTPClient FIRST, then apply the
// caller's options. Vendor options are last-wins, so a driver that genuinely
// needs its own transport chain (claude_code, which layers token refresh and
// lowercase headers) still overrides it — and must itself call
// WrapWithIdleTimeout, which it does.
//
// Drivers MUST NOT call the vendor NewClient directly. That is enforced
// statically by TestDriversUseSanctionedSDKConstructors in
// internal/llm/drivers, so the protection is opt-out, not opt-in: a new driver
// gets it without anyone remembering, and one that skips it fails the build's
// test run rather than silently costing wall clock in production.

// NewOpenAISDKClient builds an openai-go client that streams safely.
func NewOpenAISDKClient(opts ...openaiopt.RequestOption) openaisdk.Client {
	withDefaults := make([]openaiopt.RequestOption, 0, len(opts)+1)
	withDefaults = append(withDefaults, openaiopt.WithHTTPClient(StreamingHTTPClient()))
	withDefaults = append(withDefaults, opts...)
	return openaisdk.NewClient(withDefaults...)
}

// NewAnthropicSDKClient builds an anthropic-sdk-go client that streams safely.
func NewAnthropicSDKClient(opts ...anthropicopt.RequestOption) anthropicsdk.Client {
	withDefaults := make([]anthropicopt.RequestOption, 0, len(opts)+1)
	withDefaults = append(withDefaults, anthropicopt.WithHTTPClient(StreamingHTTPClient()))
	withDefaults = append(withDefaults, opts...)
	return anthropicsdk.NewClient(withDefaults...)
}

// NewGenAISDKClient builds a google genai client that streams safely.
//
// genai takes a config struct rather than options, so the default is applied by
// filling HTTPClient when the caller left it nil. Never set genai's own request
// timeout here: it covers the whole request including body reads and would kill
// long, healthy streams.
func NewGenAISDKClient(ctx context.Context, cfg *genai.ClientConfig) (*genai.Client, error) {
	if cfg == nil {
		cfg = &genai.ClientConfig{}
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = StreamingHTTPClient()
	}
	return genai.NewClient(ctx, cfg)
}
