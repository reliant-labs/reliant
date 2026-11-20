package settings

import (
	"sort"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/llm/models"

	"github.com/reliant-labs/reliant/internal/logging"
)

// ============================================================================
// Sub-phase 6d: Provider Settings (API Keys)
// ============================================================================

// providerOutputModalities returns the distinct output modalities the given
// provider's models can generate, derived from the model registry.
//
// Derived rather than hardcoded per provider: adding an image model to
// models.yaml makes its provider image-capable with no change here. A static
// per-provider map would drift the moment a provider ships a new endpoint, and
// the drift would be silent — the UI would keep claiming a capability the
// registry no longer backs, or hide one it does.
//
// Order is stable (text first, then the generative extras alphabetically) so
// the UI and any test see a deterministic list rather than map order.
func providerOutputModalities(provider string) []string {
	registry, err := models.GetRegistry()
	if err != nil {
		logging.Warn("Failed to load model registry for provider modalities", "error", err)
		return nil
	}

	seen := make(map[models.Modality]bool)
	for _, def := range registry.ListModelsByProvider(provider) {
		for _, m := range def.Capabilities.EffectiveOutputModalities() {
			seen[m] = true
		}
	}

	result := make([]string, 0, len(seen))
	if seen[models.ModalityText] {
		result = append(result, string(models.ModalityText))
	}
	extras := make([]string, 0, len(seen))
	for m := range seen {
		if m != models.ModalityText {
			extras = append(extras, string(m))
		}
	}
	sort.Strings(extras)
	return append(result, extras...)
}

func requireOAuthParams(code, codeVerifier, redirectURI string) error {
	switch {
	case code == "":
		return svcerr.InvalidArgument("authorization code is required")
	case codeVerifier == "":
		return svcerr.InvalidArgument("code_verifier is required")
	case redirectURI == "":
		return svcerr.InvalidArgument("redirect_uri is required")
	}
	return nil
}

// connectedMessage is the success line for a sign-in, naming what happened to the account.
func connectedMessage(provider string, res signInResult) string {
	name := providerName(provider)
	if res.created {
		return "Connected to " + name
	}
	return name + " account already connected; signed in again"
}
