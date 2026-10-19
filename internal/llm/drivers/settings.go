// Copyright (c) 2025 Reliant Labs

package drivers

import (
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/openai"
	"github.com/reliant-labs/reliant/internal/llm/drivers/vertexai"
)

// Settings is the LLM layer's process configuration: the LLMConfig of the
// server role that calls models.
type Settings struct {
	// StreamTimeouts are the stream guards' deadlines (RELIANT_LLM_STREAM_*).
	StreamTimeouts llm.StreamTimeouts
	// ReliantBaseURL is the Reliant LLM gateway (RELIANT_API_BASE_URL).
	ReliantBaseURL string
	// VertexProject and VertexLocation place Vertex AI calls
	// (VERTEXAI_PROJECT, VERTEXAI_LOCATION).
	VertexProject  string
	VertexLocation string
	// DebugOpenAIResponsesInput and DebugOpenAIToolSchemas log the OpenAI
	// Responses payloads (RELIANT_DEBUG_OPENAI_*).
	DebugOpenAIResponsesInput bool
	DebugOpenAIToolSchemas    bool
}

// Configure applies s to the process. A server role calls it once at boot,
// before any LLM call.
func Configure(s Settings) {
	llm.ConfigureStreamTimeouts(s.StreamTimeouts)
	ConfigureReliantBaseURL(s.ReliantBaseURL)
	vertexai.Configure(s.VertexProject, s.VertexLocation)
	openai.ConfigureDebug(s.DebugOpenAIResponsesInput, s.DebugOpenAIToolSchemas)
}
