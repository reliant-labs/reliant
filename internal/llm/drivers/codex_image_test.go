// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/drivers/codex"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// codexImageModelID is the model the Codex backend actually serves on
// /images/generations, verified by a live HTTP 200. The platform API
// (api.openai.com) serves it too, but as a different surface with a different
// credential — and it additionally serves the gpt-image-2.5 tier, which the
// Codex backend is not known to. Assuming a surface serves a model produces a
// config that resolves cleanly and then 404s at call time.
const codexImageModelID = "gpt-image-2"

// TestCodexImageModel_IsDeclaredForImageOutput pins the registry contribution:
// gpt-image-2 must exist, produce images, and carry the image-gen tag. A model
// with the tag but not the modality escapes the hard filter that keeps text
// models off the image endpoint; a model with the modality but not the tag can
// never be selected by a default request.
func TestCodexImageModel_IsDeclaredForImageOutput(t *testing.T) {
	registry := models.MustGetRegistry()

	definition, ok := registry.GetDefinition(codexImageModelID)
	if !ok {
		t.Fatalf("model %q is not defined in models.yaml", codexImageModelID)
	}

	if !definition.Capabilities.CanOutput(models.ModalityImage) {
		t.Errorf("model %s produces %v; an image-generation model must declare output_modalities: [image]",
			codexImageModelID, definition.Capabilities.EffectiveOutputModalities())
	}

	sawImageGenTag := false
	for _, tag := range definition.Tags {
		if tag == DefaultImageGenTag {
			sawImageGenTag = true
		}
	}
	if !sawImageGenTag {
		t.Errorf("model %s has tags %v; it must carry %q to be selectable by a default image request",
			codexImageModelID, definition.Tags, DefaultImageGenTag)
	}
}

// TestCodexImageModel_MapsToCodexFirst guards the half of this entry that the
// Codex work owns: the ChatGPT backend must be a mapping, with the api_model
// the live call used, and it must come FIRST in YAML order.
//
// gpt-image-2 is now mapped to three surfaces, because all three genuinely
// serve it (see the comment on the entry in models.yaml). Codex and openai are
// both priority-1 BYO drivers, so a user with BOTH credentials has a tie that
// findBestProvider breaks on YAML order — and codex is the surface with a
// verified HTTP 200 behind it, so it should win.
func TestCodexImageModel_MapsToCodexFirst(t *testing.T) {
	registry := models.MustGetRegistry()

	definition, ok := registry.GetDefinition(codexImageModelID)
	if !ok {
		t.Fatalf("model %q is not defined in models.yaml", codexImageModelID)
	}

	if len(definition.Providers) == 0 {
		t.Fatalf("model %s has no providers and can never resolve", codexImageModelID)
	}
	provider := definition.Providers[0]
	if provider.Driver != "codex" {
		t.Errorf("first provider driver = %q, want codex; on a priority-1 tie the earlier YAML entry wins, "+
			"and codex is the surface verified to serve this model", provider.Driver)
	}
	if provider.APIModel != codexImageModelID {
		t.Errorf("api_model = %q, want %q", provider.APIModel, codexImageModelID)
	}
}

// TestCodexImageModel_ResolvesForACodexUser is the end of the selection path a
// user with only Codex connected actually travels: the default tag plus the
// image modality filter, against codex as the sole configured provider.
//
// The assertion is deliberately on the SURFACE rather than on a specific model
// id. Every image-gen model ties on tag score, so the default is whichever
// comes first in models.yaml — and that ordering is a product decision that
// moves as better models ship (it was gpt-image-2, it is now
// gpt-image-2.5-flare). Pinning the id here would make this test fail every
// time the default legitimately improved, which teaches the next person to
// edit the assertion rather than think. What must NOT change is that a
// Codex-only user resolves something Codex can actually serve.
func TestCodexImageModel_ResolvesForACodexUser(t *testing.T) {
	registry := models.MustGetRegistry()

	selector := models.ModelSelector{
		Tags:                  []string{DefaultImageGenTag},
		RequireOutputModality: models.ModalityImage,
	}
	resolved, err := registry.Resolve(selector, []string{"codex"})
	if err != nil {
		t.Fatalf("resolve with only codex configured: %v", err)
	}
	if resolved.Provider.Driver != "codex" {
		t.Errorf("provider = %q, want codex — a Codex-only user must not be routed to a surface they have no credential for", resolved.Provider.Driver)
	}
	if !resolved.Definition.Capabilities.CanOutput(models.ModalityImage) {
		t.Errorf("resolved %q, which does not declare image output", resolved.Definition.ID)
	}
}

// TestImageGenConfig_CodexTargetsTheBackendAPI is the wire contract, verified
// against a real HTTP 200 from the Codex backend. The base URL already ends in
// /codex, and the imagegen client appends /images/generations, composing the
// exact path the live call used.
func TestImageGenConfig_CodexTargetsTheBackendAPI(t *testing.T) {
	config, err := imageGenConfig(codexResolvedModel(), codexDriverConfig())
	if err != nil {
		t.Fatalf("imageGenConfig: %v", err)
	}

	if config.BaseURL != codex.CodexBaseURL {
		t.Errorf("BaseURL = %q, want %q", config.BaseURL, codex.CodexBaseURL)
	}
	if config.APIKey != codexTestAccessToken {
		t.Errorf("APIKey = %q; the Codex OAuth access token must be sent unchanged as the bearer", config.APIKey)
	}
	if config.APIModel != codexImageModelID || config.Driver != "codex" {
		t.Errorf("config = %+v", config)
	}
}

// TestImageGenConfig_CodexSendsAccountIDAndClientIdentity pins the header set
// the verified call carried. chatgpt-account-id selects which ChatGPT account
// the request bills against and is rejected when absent; originator / version /
// user-agent are the client identity the Codex backend gates on, and are the
// same three headers our Codex CHAT driver already sends successfully.
func TestImageGenConfig_CodexSendsAccountIDAndClientIdentity(t *testing.T) {
	config, err := imageGenConfig(codexResolvedModel(), codexDriverConfig())
	if err != nil {
		t.Fatalf("imageGenConfig: %v", err)
	}

	want := map[string]string{
		"chatgpt-account-id": codexTestAccountID,
		"originator":         codexImageOriginator,
		"version":            codexImageVersion,
		"user-agent":         codexImageUserAgent,
	}
	for header, wantValue := range want {
		if got := config.ExtraHeaders[header]; got != wantValue {
			t.Errorf("header %s = %q, want %q", header, got, wantValue)
		}
	}

	// Cloudflare bot-management cookies were present in the capture but are
	// issued per-connection by the edge, not by us. Sending a stale one is at
	// best inert and at worst a fingerprint mismatch.
	if _, ok := config.ExtraHeaders["cookie"]; ok {
		t.Error("cookie header must not be sent; the captured values are per-connection Cloudflare state")
	}
}

// TestImageGenConfig_CodexRecoversAccountIDFromTheToken covers the real
// failure mode of a nullable column: codex_auth_tokens.account_id may be empty
// on a row written before the id was extracted. The account id is a claim
// inside the access token itself, so it is recoverable rather than fatal.
func TestImageGenConfig_CodexRecoversAccountIDFromTheToken(t *testing.T) {
	driverConfig := codexDriverConfig()
	driverConfig.AccountUUID = ""

	config, err := imageGenConfig(codexResolvedModel(), driverConfig)
	if err != nil {
		t.Fatalf("imageGenConfig: %v", err)
	}
	if got := config.ExtraHeaders["chatgpt-account-id"]; got != codexTestAccountID {
		t.Errorf("chatgpt-account-id = %q, want %q recovered from the JWT claim", got, codexTestAccountID)
	}
}

// codexTestAccountID is the chatgpt_account_id encoded in codexTestAccessToken.
const codexTestAccountID = "3eddf627-dcc9-461a-98b2-cd84140abf91"

// codexTestAccessToken is an unsigned JWT carrying only the claim the account
// id is read from. It is not a credential: the signature is a placeholder and
// nothing in this path verifies it.
var codexTestAccessToken = "eyJhbGciOiJub25lIn0." +
	// {"https://api.openai.com/auth":{"chatgpt_account_id":"3eddf627-dcc9-461a-98b2-cd84140abf91"}}
	"eyJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOnsiY2hhdGdwdF9hY2NvdW50X2lkIjoiM2VkZGY2MjctZGNjOS00NjFhLTk4YjItY2Q4NDE0MGFiZjkxIn19" +
	".signature"

func codexResolvedModel() *models.ResolvedModel {
	return &models.ResolvedModel{
		Definition: models.ModelDefinition{ID: codexImageModelID},
		Provider:   models.ProviderMapping{Driver: "codex", APIModel: codexImageModelID},
	}
}

func codexDriverConfig() models.DriverConfig {
	return models.DriverConfig{
		DriverID:    models.DriverID("codex"),
		APIKey:      codexTestAccessToken,
		AccountUUID: codexTestAccountID,
		Enabled:     true,
	}
}
