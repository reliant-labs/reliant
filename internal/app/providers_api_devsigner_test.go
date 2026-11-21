package app

import (
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/runenv"
	"github.com/reliant-labs/reliant/pkg/config"
)

// The api-server's settings are where a dev signer (AUTH_DEV_JWT_SECRET) is
// refused outside the dev tier: every non-dev configuration of reliant —
// prod, an unset or unrecognised RELIANT_ENV, test — fails to build them, so
// the process stops before it serves. Dev builds them with the signer on.
func TestNewAPISettingsRefusesDevSignerOutsideDev(t *testing.T) {
	api := &config.ApiServerConfig{
		JwksUrl:          "https://example.supabase.co/auth/v1/.well-known/jwks.json",
		AuthDevJwtSecret: "dev-local-hmac-shared-secret-do-not-use-in-prod",
	}
	for _, env := range []string{"", "prod", "production", "staging", "test", "nonprod"} {
		t.Run("RELIANT_ENV="+env, func(t *testing.T) {
			runenv.ConfigureForTest(t, env)
			s, err := NewAPISettings(APISettingsDeps{API: api})
			if !errors.Is(err, auth.ErrDevSignerOutsideDev) {
				t.Fatalf("NewAPISettings = (%v, %v), want ErrDevSignerOutsideDev", s, err)
			}
		})
	}

	t.Run("RELIANT_ENV=dev", func(t *testing.T) {
		runenv.ConfigureForTest(t, "dev")
		s, err := NewAPISettings(APISettingsDeps{API: api})
		if err != nil {
			t.Fatalf("NewAPISettings in dev: %v", err)
		}
		if s.Auth.DevJWTSecret != api.GetAuthDevJwtSecret() {
			t.Errorf("Auth.DevJWTSecret = %q, want the configured secret", s.Auth.DevJWTSecret)
		}
	})

	t.Run("unset in prod", func(t *testing.T) {
		runenv.ConfigureForTest(t, "prod")
		if _, err := NewAPISettings(APISettingsDeps{API: &config.ApiServerConfig{JwksUrl: api.GetJwksUrl()}}); err != nil {
			t.Fatalf("NewAPISettings in prod without a dev signer: %v", err)
		}
	})
}
