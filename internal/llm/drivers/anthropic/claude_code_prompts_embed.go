// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	_ "embed"
)

// Byte-exact Claude Code system-prompt blocks, extracted verbatim from real
// claude-cli/2.1.204 traffic. These are embedded (not retyped) because they
// contain backticks, which Go raw-string literals cannot hold. Do NOT edit the
// .txt files by hand — they must stay byte-identical to the captured prompts so
// the spoof is not flagged and prompt-cache keys match Claude Code's.

//go:embed ccprompts/identity.txt
var ccIdentityPrompt string // block[1], identical across all models

//go:embed ccprompts/agent_verbose.txt
var ccAgentVerbose string // block[2] verbose (haiku-4.5, sonnet-5, older models)

//go:embed ccprompts/output_verbose.txt
var ccOutputVerbose string // block[3] verbose

//go:embed ccprompts/agent_lean.txt
var ccAgentLean string // block[2] lean (opus-4.8, fable-5)

//go:embed ccprompts/output_lean.txt
var ccOutputLean string // block[3] lean

// leanPromptModels are the api_model values that use the "lean" prompt variant.
// Confirmed by sha256 across captures: opus-4.8 and fable-5 share the lean blocks,
// while haiku-4.5 and sonnet-5 share the verbose blocks. Everything else (older
// opus/sonnet) falls back to the verbose variant.
var leanPromptModels = map[string]bool{
	"claude-opus-4-8": true,
	"claude-fable-5":  true,
}

// claudeCodeAgentOutputBlocks returns the (agent, output) prompt pair for the
// given api_model, selecting the lean variant for opus-4.8/fable-5 and the verbose
// variant for everything else.
func claudeCodeAgentOutputBlocks(apiModel string) (agent, output string) {
	if leanPromptModels[apiModel] {
		return ccAgentLean, ccOutputLean
	}
	return ccAgentVerbose, ccOutputVerbose
}
