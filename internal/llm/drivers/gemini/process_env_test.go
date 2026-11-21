// Copyright (c) 2025 Reliant Labs
package gemini

import (
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
)

// TestNewClientWithNoKeyIgnoresProcessCredentials pins that a Gemini driver
// built with no key of the account's is refused with the "connect Gemini"
// error rather than served by GOOGLE_API_KEY / GEMINI_API_KEY from the server
// process. Drivers run on the multi-tenant server roles.
func TestNewClientWithNoKeyIgnoresProcessCredentials(t *testing.T) {
	t.Setenv("GOOGLE_API_KEY", "PROCESS_CREDENTIAL_SENTINEL_gemini")
	t.Setenv("GEMINI_API_KEY", "PROCESS_CREDENTIAL_SENTINEL_gemini")

	if _, err := NewClient(llm.DriverOptions{}); !errors.Is(err, llm.ErrGeminiAPIKeyRequired) {
		t.Fatalf("NewClient with no key: err = %v, want llm.ErrGeminiAPIKeyRequired", err)
	}
	if _, err := NewClient(llm.DriverOptions{ApiKey: "account-key"}); err != nil {
		t.Fatalf("NewClient with the account's key: %v", err)
	}
}
