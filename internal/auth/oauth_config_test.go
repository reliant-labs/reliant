package auth

import (
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/builddefaults"
)

func TestRequireAuthConfigUsesCompiledDefaults(t *testing.T) {
	t.Setenv("RELIANT_AUTH_URL", "")
	t.Setenv("RELIANT_AUTH_KEY", "")
	withCompiledAuthDefaults(t, "http://auth.localhost/", "compiled-key")

	serverURL, anonKey, err := requireAuthConfig()
	if err != nil {
		t.Fatalf("requireAuthConfig() returned error: %v", err)
	}
	if serverURL != "http://auth.localhost" {
		t.Fatalf("serverURL = %q, want %q", serverURL, "http://auth.localhost")
	}
	if anonKey != "compiled-key" {
		t.Fatalf("anonKey = %q, want %q", anonKey, "compiled-key")
	}
}

func TestRequireAuthConfigPrefersEnvironment(t *testing.T) {
	t.Setenv("RELIANT_AUTH_URL", "http://env.localhost/")
	t.Setenv("RELIANT_AUTH_KEY", "env-key")
	withCompiledAuthDefaults(t, "http://auth.localhost/", "compiled-key")

	serverURL, anonKey, err := requireAuthConfig()
	if err != nil {
		t.Fatalf("requireAuthConfig() returned error: %v", err)
	}
	if serverURL != "http://env.localhost" {
		t.Fatalf("serverURL = %q, want %q", serverURL, "http://env.localhost")
	}
	if anonKey != "env-key" {
		t.Fatalf("anonKey = %q, want %q", anonKey, "env-key")
	}
}

func TestRequireAuthConfigErrorsWithoutAuthProvider(t *testing.T) {
	t.Setenv("RELIANT_AUTH_URL", "")
	t.Setenv("RELIANT_AUTH_KEY", "")
	withCompiledAuthDefaults(t, "", "")

	_, _, err := requireAuthConfig()
	if !errors.Is(err, ErrAuthNotConfigured) {
		t.Fatalf("requireAuthConfig() error = %v, want ErrAuthNotConfigured", err)
	}
}

func withCompiledAuthDefaults(t *testing.T, authURL, authKey string) {
	t.Helper()
	previousAuthURL := builddefaults.AuthURL
	previousAuthKey := builddefaults.AuthKey
	builddefaults.AuthURL = authURL
	builddefaults.AuthKey = authKey
	t.Cleanup(func() {
		builddefaults.AuthURL = previousAuthURL
		builddefaults.AuthKey = previousAuthKey
	})
}
