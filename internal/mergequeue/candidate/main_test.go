// Copyright (c) 2025 Reliant Labs
package candidate_test

import (
	"os"
	"testing"

	"github.com/reliant-labs/reliant/internal/credsandbox"
)

// TestMain lets this test binary stand in for the reliant binary as the
// credential sandbox's helper, so regen runs under credentials: none exactly
// as it does on a daemon (or, on a host that does not allow the sandbox, with
// only the scrubbed environment).
func TestMain(m *testing.M) {
	credsandbox.DispatchForTest()
	os.Exit(m.Run())
}
