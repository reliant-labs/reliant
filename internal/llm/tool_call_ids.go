// Copyright (c) 2025 Reliant Labs
package llm

import (
	"strings"

	"github.com/google/uuid"
)

// NewToolCallID mints the id reliant gives a tool call: call_<32 hex>.
//
// Drivers that relay a third-party server's ids (a local OpenAI-compatible
// server, an OpenRouter upstream, the LiteLLM gateway) mint every call's id
// rather than trusting the server's. Reliant keys a call's record, its result
// and a spawn's report by the id, and a server can omit it, repeat it within
// a response, or repeat it across responses: some local servers answer call_0
// every turn, and turn 2's call then matched turn 1's recorded result and was
// answered without running. A minted id is stored in the transcript, so the
// history sent back carries the same id on the tool call and on its result,
// which is all an OpenAI-compatible server needs: it pairs results with calls
// within one request.
//
// The shape is chosen to be accepted wherever the history may go next: at
// most 40 characters (OpenAI's limit on tool_calls[].id), only [a-zA-Z0-9_]
// (Anthropic's ^[a-zA-Z0-9_-]+$), and ending in at least nine alphanumerics
// (vLLM keeps the last nine for Mistral's tokenizer).
func NewToolCallID() string {
	return "call_" + strings.ReplaceAll(uuid.New().String(), "-", "")
}

// liteLLMThoughtSignatureSeparator is how the LiteLLM gateway carries a Gemini
// thought signature through the OpenAI tool call shape: it appends
// "__thought__<signature>" to the id it returns, and reads the signature back
// from the id of the tool call in the next request (litellm v1.104.0,
// prompt_templates/factory.py: _encode_tool_call_id_with_signature /
// _get_thought_signature_from_tool). Gemini 3 rejects a function call in
// history without its signature.
const liteLLMThoughtSignatureSeparator = "__thought__"

// NewToolCallIDKeepingThoughtSignature mints a tool call id like NewToolCallID,
// carrying over a Gemini thought signature the LiteLLM gateway embedded in
// providerID. The signature is the one part of a gateway-chosen id the next
// request needs; the part before it is the gateway's and is replaced.
func NewToolCallIDKeepingThoughtSignature(providerID string) string {
	minted := NewToolCallID()
	if _, signature, ok := strings.Cut(providerID, liteLLMThoughtSignatureSeparator); ok && signature != "" {
		return minted + liteLLMThoughtSignatureSeparator + signature
	}
	return minted
}
