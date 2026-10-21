package auth

import (
	"errors"
	"testing"
)

func TestRequireAuthConfigTrimsTheProvider(t *testing.T) {
	serverURL, anonKey, err := requireAuthConfig(Provider{URL: " http://auth.localhost/ ", Key: " configured-key "})
	if err != nil {
		t.Fatalf("requireAuthConfig() returned error: %v", err)
	}
	if serverURL != "http://auth.localhost" {
		t.Fatalf("serverURL = %q, want %q", serverURL, "http://auth.localhost")
	}
	if anonKey != "configured-key" {
		t.Fatalf("anonKey = %q, want %q", anonKey, "configured-key")
	}
}

func TestRequireAuthConfigErrorsWithoutAuthProvider(t *testing.T) {
	for _, p := range []Provider{{}, {URL: "http://auth.localhost"}, {Key: "configured-key"}} {
		if _, _, err := requireAuthConfig(p); !errors.Is(err, ErrAuthNotConfigured) {
			t.Fatalf("requireAuthConfig(%+v) error = %v, want ErrAuthNotConfigured", p, err)
		}
	}
}
