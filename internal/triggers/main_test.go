// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"os"
	"testing"
)

// TestMain stops the shared dev server before the binary exits.
//
// Without this the `temporal server start-dev` child process outlives the test
// run holding the inherited stdout pipe open, and `go test` reports "Test I/O
// incomplete 30s after exiting" and FAILS a run whose tests all passed.
func TestMain(m *testing.M) {
	code := m.Run()
	if devServer.server != nil {
		_ = devServer.server.Stop()
	}
	os.Exit(code)
}
