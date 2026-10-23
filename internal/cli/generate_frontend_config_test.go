package cli

import (
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// oidcFrontendConfig is the shape the scaffolded dev environment produces: an
// issuer and client id both read from the committed idp_identity_gen.k the
// idp-provision job publishes into — which carries empty strings until that
// job has converged at least once.
func oidcFrontendConfig() codegen.FrontendConfig {
	return codegen.FrontendConfig{
		Frontend: "web", MessageName: "WebConfig",
		Fields: []codegen.ConfigField{
			{Name: "api_url", EnvVar: "API_URL", GoType: "string", ProtoType: "string",
				DefaultValue: "http://localhost:8080"},
			{Name: "oidc_issuer", EnvVar: "OIDC_ISSUER", GoType: "string", ProtoType: "string"},
			{Name: "oidc_client_id", EnvVar: "OIDC_CLIENT_ID", GoType: "string", ProtoType: "string"},
			{Name: "oidc_redirect_uri", EnvVar: "OIDC_REDIRECT_URI", GoType: "string", ProtoType: "string"},
		},
	}
}

// A half-configured OIDC pair is the one combination the frontend cannot
// survive: readOidcConfig() treats "neither set" as the no-auth posture and
// "both set" as real auth, but THROWS on one-without-the-other — and that
// throw happens inside the auth provider that wraps the whole app, so every
// page renders "Application error" instead of any UI at all.
//
// The scaffolded dev env produces exactly that pair before the idp-provision
// job has ever converged: config.k pins oidc_issuer to a literal, while
// idp_identity_gen.k's client_id is still the empty-string stub
// EnsureIDPIdentityStub seeds. Empty is a valid, working answer for the
// PAIR (no-auth posture), but only when the issuer is empty too — so
// projection is the last point where that invariant can be enforced, and it
// is enforced here.
func TestFrontendRuntimeValues_DropsHalfConfiguredOIDC(t *testing.T) {
	got := frontendRuntimeValues(oidcFrontendConfig(), map[string]any{
		"OIDC_ISSUER":    "http://localhost:8080",
		"OIDC_CLIENT_ID": "", // the IdP was unreachable
	})

	if v, ok := got["OIDC_ISSUER"]; ok && v != "" {
		t.Errorf("OIDC_ISSUER = %q with an empty client id: the frontend throws on a "+
			"half-configured pair and the whole app fails to render", v)
	}
	// The unrelated values must survive untouched.
	if got["API_URL"] != "http://localhost:8080" {
		t.Errorf("API_URL = %#v, want the projected value to be left alone", got["API_URL"])
	}
}

// The complete pair is real configuration and must pass through: this is the
// case where the dev IdP answered, or a deployed env pinned both values.
func TestFrontendRuntimeValues_KeepsCompleteOIDC(t *testing.T) {
	got := frontendRuntimeValues(oidcFrontendConfig(), map[string]any{
		"OIDC_ISSUER":    "http://localhost:8080",
		"OIDC_CLIENT_ID": "319845...@roofworks",
	})

	if got["OIDC_ISSUER"] != "http://localhost:8080" {
		t.Errorf("OIDC_ISSUER = %#v, want it preserved when the pair is complete", got["OIDC_ISSUER"])
	}
	if got["OIDC_CLIENT_ID"] != "319845...@roofworks" {
		t.Errorf("OIDC_CLIENT_ID = %#v, want it preserved", got["OIDC_CLIENT_ID"])
	}
}

// Neither set is the deliberate no-auth posture — the mock provider for
// UI-only development. Nothing to do, and nothing to warn about.
func TestFrontendRuntimeValues_NoAuthPostureIsUntouched(t *testing.T) {
	got := frontendRuntimeValues(oidcFrontendConfig(), map[string]any{})

	if v, ok := got["OIDC_ISSUER"]; ok && v != "" {
		t.Errorf("OIDC_ISSUER = %q, want empty/absent for the no-auth posture", v)
	}
	if v, ok := got["OIDC_CLIENT_ID"]; ok && v != "" {
		t.Errorf("OIDC_CLIENT_ID = %q, want empty/absent for the no-auth posture", v)
	}
}

// The browser reports the forge env as deployment.environment.name, the key the
// backend uses. It rides the runtime DOCUMENT (config.js), the one artifact that
// differs per environment, so a bundle built once and promoted reports the
// environment it is running in, not the one it was built for.
func TestWithRuntimeEnvironment(t *testing.T) {
	t.Run("adds the env the document is rendered for", func(t *testing.T) {
		got := withRuntimeEnvironment(frontendRuntimeValues(oidcFrontendConfig(), nil), "staging")
		if got["ENVIRONMENT"] != "staging" {
			t.Errorf("ENVIRONMENT = %#v, want staging", got["ENVIRONMENT"])
		}
		if got["API_URL"] != "http://localhost:8080" {
			t.Errorf("the other values must survive: %#v", got)
		}
	})
	t.Run("a declared environment wins", func(t *testing.T) {
		got := withRuntimeEnvironment(map[string]any{"ENVIRONMENT": "qa-eu"}, "staging")
		if got["ENVIRONMENT"] != "qa-eu" {
			t.Errorf("ENVIRONMENT = %#v, want the declared qa-eu", got["ENVIRONMENT"])
		}
	})
	t.Run("an empty declared environment is unset", func(t *testing.T) {
		if got := withRuntimeEnvironment(map[string]any{"ENVIRONMENT": ""}, "dev"); got["ENVIRONMENT"] != "dev" {
			t.Errorf("ENVIRONMENT = %#v, want dev", got["ENVIRONMENT"])
		}
	})
	t.Run("nil values and no env", func(t *testing.T) {
		if got := withRuntimeEnvironment(nil, "dev"); got["ENVIRONMENT"] != "dev" {
			t.Errorf("nil map: %#v", got)
		}
		if got := withRuntimeEnvironment(map[string]any{"A": "b"}, ""); len(got) != 1 {
			t.Errorf("no env must add nothing: %#v", got)
		}
	})
}
