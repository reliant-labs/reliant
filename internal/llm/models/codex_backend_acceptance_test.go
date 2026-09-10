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
//	ACCEPTED  gpt-5.5, gpt-6-astra, gpt-5.6-sol, gpt-5.6-terra, gpt-5.6-luna,
//	          gpt-5.4-mini
//	REFUSED   gpt-5.4, gpt-5.3-codex, gpt-5.3-codex-spark, gpt-5.2-codex
//
// The refused four are ordinary OpenAI-platform models; they remain in the
// catalog served by the `openai` and `openrouter` drivers, which reach them
// with an API key. Only the `codex` provider mapping is wrong.
func TestCodexProvidersAreServedByTheChatGPTAccountBackend(t *testing.T) {
	reg := MustGetRegistry()

	// Models the backend refuses for a ChatGPT account. A codex provider
	// mapping for any of these is unreachable at runtime.
	refusedByBackend := map[string]bool{
		"gpt-5.4":             true,
		"gpt-5.3-codex":       true,
		"gpt-5.3-codex-spark": true,
		"gpt-5.2-codex":       true,
	}

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

// Chat titling and compaction both resolve TagFast. For a user whose only
// configured provider is codex, that resolution must land on a model the
// backend will actually serve — otherwise titling fails for every chat, which
// is invisible because it falls back to a truncated first message that looks
// like a real title.
func TestResolve_TagFastForCodexUserIsServable(t *testing.T) {
	reg := MustGetRegistry()

	resolved, err := reg.Resolve(ModelSelector{Tags: []string{TagFast}}, []string{"codex"})
	require.NoError(t, err, "a codex-only user must be able to resolve TagFast for titling")

	refusedByBackend := map[string]bool{
		"gpt-5.4":             true,
		"gpt-5.3-codex":       true,
		"gpt-5.3-codex-spark": true,
		"gpt-5.2-codex":       true,
	}
	assert.Falsef(t, refusedByBackend[resolved.Definition.ID],
		"TagFast resolved to %q for a codex user, which the backend refuses; "+
			"chat titling would 400 on every chat", resolved.Definition.ID)
}
