// Copyright (c) 2025 Reliant Labs
// Model ID constants - used throughout the codebase
package models

// Claude/Anthropic model IDs
const (
	Claude45Haiku  ModelID = "claude-4.5-haiku"
	Claude45Sonnet ModelID = "claude-4.5-sonnet"
	Claude46Sonnet ModelID = "claude-4.6-sonnet"
	Claude45Opus   ModelID = "claude-4.5-opus"
	Claude46Opus   ModelID = "claude-4.6-opus"
)

// OpenAI/GPT model IDs
const (
	GPT52           ModelID = "gpt-5.2"
	GPT54Mini       ModelID = "gpt-5.4-mini"
	GPT52Pro        ModelID = "gpt-5.2-pro"
	GPT54           ModelID = "gpt-5.4"
	GPT54Pro        ModelID = "gpt-5.4-pro"
	GPT55           ModelID = "gpt-5.5"
	GPT52Codex      ModelID = "gpt-5.2-codex"
	GPT53Codex      ModelID = "gpt-5.3-codex"
	GPT53CodexSpark ModelID = "gpt-5.3-codex-spark"
)

// Google/Gemini model IDs
const (
	Gemini31ProPreview            ModelID = "gemini-3.1-pro-preview"
	Gemini31ProPreviewCustomTools ModelID = "gemini-3.1-pro-preview-customtools"
	Gemini31FlashLitePreview      ModelID = "gemini-3.1-flash-lite-preview"
	Gemini3ProPreview             ModelID = "gemini-3-pro-preview"
	Gemini3FlashPreview           ModelID = "gemini-3-flash-preview"
	Gemini25Pro                   ModelID = "gemini-2.5-pro"
	Gemini25Flash                 ModelID = "gemini-2.5-flash"
	Gemini25FlashLite             ModelID = "gemini-2.5-flash-lite"
)

// XAI/Grok model IDs
const (
	Grok3Beta         ModelID = "grok-3-beta"
	Grok3MiniBeta     ModelID = "grok-3-mini-beta"
	Grok3FastBeta     ModelID = "grok-3-fast-beta"
	Grok3MiniFastBeta ModelID = "grok-3-mini-fast-beta"
	Grok4             ModelID = "grok-4"
	GrokCodeFast      ModelID = "grok-code-fast"
)

// Vertex AI model IDs
const (
	VertexGemini25Pro    ModelID = "vertex-gemini-2.5-pro"
	VertexGemini25Flash  ModelID = "vertex-gemini-2.5-flash"
	VertexClaude45Sonnet ModelID = "vertex-claude-4.5-sonnet"
	VertexClaude46Opus   ModelID = "vertex-claude-4.6-opus"
	VertexClaude45Haiku  ModelID = "vertex-claude-4.5-haiku"
)

// Test/Mock model IDs
const (
	MockModel         ModelID = "mock"
	TinyContextModel  ModelID = "tiny-context"
	SmallContextModel ModelID = "small-context"
)