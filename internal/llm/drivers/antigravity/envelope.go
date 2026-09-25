// Copyright (c) 2025 Reliant Labs
//
// Wire types for the Antigravity (Google cloudcode) endpoint.
//
// The types themselves live in the leaf package agywire, and this file only
// names them locally. They moved because the image-generation client
// (internal/llm/drivers/imagegen) speaks the same double envelope and cannot
// import this package: this one imports internal/llm/tools, which imports
// imagegen, so the edge back would be a cycle. Two hand-maintained copies of a
// wire format drift, so the format went to a leaf both sides can reach.
//
// These are ALIASES, not new types — `type content = agywire.Content` — so
// there is exactly one set of structs and a value crosses the boundary without
// conversion. The short lowercase names are kept because every use site in this
// package reads better unqualified.
//
// Why the format is hand-rolled at all: Antigravity double-wraps a standard
// Gemini exchange in BOTH directions, and google.golang.org/genai unmarshals a
// body straight into GenerateContentResponse, so every Antigravity body would
// decode to an EMPTY struct and the call would appear to succeed while
// producing nothing. See envelope_test.go, which pins that failure.
//
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: a provider driver IS an outbound adapter behind llm.Driver (the contract lives in internal/llm); a per-driver contract.go would duplicate it; tracked in H-RELIANT-CI-lint follow-ups
//forge:exclude-contract: llm.Driver implementation for Antigravity (Google cloudcode); its contract is llm.Driver
package antigravity

import (
	"strings"

	"github.com/reliant-labs/reliant/internal/llm/drivers/agywire"
)

// Wire constants captured from antigravity/cli/1.2.5. These look like
// client-identity gating, so they are sent verbatim rather than guessed.
const (
	endpointURL = agywire.StreamEndpointURL

	userAgentHeader = agywire.UserAgentHeader

	// envelopeProject, envelopeUserAgent and envelopeRequestType are the
	// top-level identity fields the capture sends alongside the wrapped
	// request. They are not model configuration.
	envelopeProject     = agywire.EnvelopeProject
	envelopeUserAgent   = agywire.EnvelopeUserAgent
	envelopeRequestType = agywire.RequestTypeAgent
)

type (
	requestEnvelope       = agywire.RequestEnvelope
	generateReq           = agywire.GenerateReq
	content               = agywire.Content
	part                  = agywire.Part
	functionCall          = agywire.FunctionCall
	functionResponse      = agywire.FunctionResponse
	blob                  = agywire.Blob
	toolDecls             = agywire.ToolDecls
	functionDeclaration   = agywire.FunctionDeclaration
	schema                = agywire.Schema
	toolConfig            = agywire.ToolConfig
	functionCallingConfig = agywire.FunctionCallingConfig
	generationConfig      = agywire.GenerationConfig
	thinkingConfig        = agywire.ThinkingConfig

	streamFrame   = agywire.Envelope
	generateResp  = agywire.GenerateResp
	candidate     = agywire.Candidate
	usageMetadata = agywire.UsageMetadata
)

// unboundedThinkingBudget is the capture's thinkingBudget: let the server
// decide how much thinking the selected effort level warrants.
const unboundedThinkingBudget = agywire.UnboundedThinkingBudget

// --- model id / effort suffix ---

// effortVariants lists, per base model id, the effort suffixes Antigravity
// advertises as distinct model ids.
//
// Antigravity expresses reasoning effort as a model-id SUFFIX rather than a
// request field: the client picks "gemini-3.8-flash-high" instead of sending
// an effort parameter. A base id absent from this map has no advertised
// variants, so its id is sent unchanged whatever effort the caller asked for —
// inventing a suffix would name a model that does not exist.
//
// Image models are absent on purpose, and must stay absent: effort is a
// thinking concern, and the captured image request sends a bare
// "gemini-3.1-flash-image".
var effortVariants = map[string][]string{
	"gemini-3.8-flash": {"low", "medium", "high"},
}

// modelIDForEffort maps a base api_model plus a caller effort level onto the
// model id to put in the envelope's top-level "model" field.
//
// Returns the base id unchanged when: the effort is empty or "disabled", the
// base advertises no variants, or the requested level is not one of them.
func modelIDForEffort(baseModel, effort string) string {
	if baseModel == "" {
		return ""
	}
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" || effort == "disabled" {
		return baseModel
	}
	for _, variant := range effortVariants[baseModel] {
		if variant == effort {
			return baseModel + "-" + variant
		}
	}
	return baseModel
}
