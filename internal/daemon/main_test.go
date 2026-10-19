// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"os"
	"testing"

	"github.com/reliant-labs/reliant/internal/heavyjob"
)

// TestMain lets this test binary stand in for the reliant binary as the
// heavy-slot wrapper, as cmd/reliant's main does (see local_exec_heavy_test.go).
func TestMain(m *testing.M) {
	heavyjob.Dispatch()
	os.Exit(m.Run())
}
