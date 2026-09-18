// Copyright (c) 2025 Reliant Labs
package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The codex driver talks to chatgpt.com/backend-api/codex, which serves a
// ChatGPT ACCOUNT rather than an API key. That backend refuses models it does
// not serve on that plan with a 400 whose body is:
//
//	{"detail":"The '<model>' model is not supported when using Codex with a
//	 ChatGPT account."}
//
// Offering such a model in the catalog is not a cosmetic error: tag resolution
// hands it to real callers, and every request they make fails. That is exactly
// how chat titling broke — TagFast resolved to gpt-5.3-codex-spark for a codex
// user, so every title 400'd and silently fell back to the truncated first
// message.
//
// Verified live against the backend with a current ChatGPT account token, using
// the same version/originator headers the driver sends (codex-tui 0.153.4):
//
//	ACCEPTED  gpt-5.5, gpt-6-astra, gpt-5.6-sol, gpt-5.6-terra, gpt-5.6-luna
//	REFUSED   gpt-5.4, gpt-5.4-mini, gpt-5.3-codex, gpt-5.3-codex-spark,
//	          gpt-5.2-codex
//
// The refused models are ordinary OpenAI-platform models; they remain in the
// catalog served by the `openai` and `openrouter` drivers, which reach them
// with an API key. Only the `codex` provider mapping is wrong.
//
// gpt-5.4-mini was listed ACCEPTED here on the strength of the family pattern
// rather than an individual probe, and it was the only codex model tagged
// `fast` — so it is what TagFast resolved to, and every chat title 400'd. The
// mini shares its parent's refusal; do not assume a `-mini` follows the family.
func TestCodexProvidersAreServedByTheChatGPTAccountBackend(t *testing.T) {
	reg := MustGetRegistry()

	// Models the backend refuses for a ChatGPT account. A codex provider
	// mapping for any of these is unreachable at runtime.
	refusedByBackend := codexRefusedByBackend()

	for _, def := range reg.ListAll() {
		for _, provider := range def.Providers {
			if provider.Driver != "codex" {
				continue
			}
			assert.Falsef(t, refusedByBackend[def.ID],
				"model %q declares a codex provider, but the ChatGPT-account Codex "+
					"backend refuses it with a 400. Drop the codex provider mapping "+
					"(the openai/openrouter mappings still reach this model).", def.ID)
		}
	}
}

// Chat titling and compaction resolve a [fast, moderate] preference ladder.
// For a user whose only configured provider is codex, that resolution must
// land on a model the backend will actually serve — otherwise titling fails
// for every chat, which is invisible because it falls back to a truncated
// first message that looks like a real title.
//
// This mirrors titleModelSelector in
// internal/workflow/runtime/activities/handlers/compact.go. Keep them in step:
// the codex driver ships no fast-tagged model, so the ladder is what makes a
// codex-only user resolvable at all.
func TestResolve_CodexTitlingDegradesToAServableModel(t *testing.T) {
	reg := MustGetRegistry()

	resolved, err := reg.Resolve(ModelSelector{
		Tags:                  []string{TagFast, TagModerate},
		RequireOutputModality: ModalityText,
	}, []string{"codex"})
	require.NoError(t, err, "a codex-only user must be able to resolve a titling model")

	refusedByBackend := codexRefusedByBackend()
	assert.Falsef(t, refusedByBackend[resolved.Definition.ID],
		"TagFast resolved to %q for a codex user, which the backend refuses; "+
			"chat titling would 400 on every chat", resolved.Definition.ID)
	assert.Truef(t, resolved.Definition.Capabilities.CanOutput(ModalityText),
		"TagFast resolved to %q, which cannot emit text", resolved.Definition.ID)
}

// Several image-generation models carry latency/cost tags that text callers
// also ask for — gpt-image-2.5-flare is [image-gen, fast] and
// gemini-3.1-flash-lite-image is [image-gen, cheap, fast]. Tag scoring degrades
// gracefully, so a text caller asking for "fast" alone can land on one of them
// once the text candidates are filtered out by provider availability. The
// modality requirement is the hard filter that prevents it; the tag is only a
// preference.
//
// This is the general form of the titling bug: a capability mismatch must be
// expressed as a requirement, never inferred from a tag.
func TestResolve_TextTagsNeverDegradeOntoImageModels(t *testing.T) {
	reg := MustGetRegistry()

	// openai serves both fast text models and image-gen models, so provider
	// availability alone does not rule the image models out.
	for _, tag := range []string{TagFast, TagCheap} {
		resolved, err := reg.Resolve(ModelSelector{
			Tags:                  []string{tag},
			RequireOutputModality: ModalityText,
		}, []string{"openai"})
		require.NoErrorf(t, err, "resolving %q for text", tag)
		assert.Truef(t, resolved.Definition.Capabilities.CanOutput(ModalityText),
			"tag %q with RequireOutputModality=text resolved to %q, which emits %v",
			tag, resolved.Definition.ID, resolved.Definition.Capabilities.EffectiveOutputModalities())
	}
}

// codexRefusedByBackend is the single list of models the ChatGPT-account Codex
// backend rejects. Both tests above read it, so a newly-discovered refusal
// closes the catalog mapping and the resolution path together.
func codexRefusedByBackend() map[string]bool {
	return map[string]bool{
		"gpt-5.4":             true,
		"gpt-5.4-mini":        true,
		"gpt-5.3-codex":       true,
		"gpt-5.3-codex-spark": true,
		"gpt-5.2-codex":       true,
	}
}
