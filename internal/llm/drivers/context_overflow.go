// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"errors"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3"
	"google.golang.org/genai"

	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
)

// markContextOverflow marks err as drivererrors.ErrContextOverflow when it is
// a provider refusing the prompt as larger than the model's window, and
// returns it unchanged otherwise. The text stays the provider's.
//
// Every production driver's errors pass through here (baseDriver), so one
// place covers Anthropic, Claude Code, Codex, OpenAI, OpenRouter, Copilot,
// Gemini, Vertex, Antigravity and the local servers. The structured facts —
// the HTTP status and the provider's error code — are read from the SDK error
// types where a driver kept them; drivers that flatten their errors to text
// still print the status, which drivererrors reads back.
func markContextOverflow(err error) error {
	if err == nil || errors.Is(err, drivererrors.ErrContextOverflow) {
		return err
	}
	return drivererrors.MarkContextOverflow(err, providerFailureOf(err))
}

// providerFailureOf collects what an error says about the failed request.
func providerFailureOf(err error) drivererrors.ProviderFailure {
	f := drivererrors.ProviderFailure{Message: err.Error()}
	var anth *anthropic.Error
	var oai *openai.Error
	var gen genai.APIError
	var genPtr *genai.APIError
	switch {
	case errors.As(err, &anth):
		f.Status = anth.StatusCode
		f.Codes = []string{string(anth.Type())}
	case errors.As(err, &oai):
		f.Status = oai.StatusCode
		f.Codes = []string{oai.Code, oai.Type}
	case errors.As(err, &gen):
		f.Status = gen.Code
		f.Codes = []string{gen.Status}
	case errors.As(err, &genPtr) && genPtr != nil:
		f.Status = genPtr.Code
		f.Codes = []string{genPtr.Status}
	default:
		f.Status = drivererrors.StatusFromText(f.Message)
	}
	return f
}
