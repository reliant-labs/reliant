// Copyright (c) 2025 Reliant Labs
package services

import (
	"os"
	"sync"
	"testing"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
)

// One ephemeral Temporal for this package's trigger integration tests. Booting
// a server per test would dominate the runtime, and the tests use distinct
// task queues so they do not interfere.
var (
	triggerDevServerOnce sync.Once
	triggerDevServer     *testsuite.DevServer
	triggerDevClient     client.Client
	triggerDevServerErr  error
)

// TestMain stops the dev server before the binary exits.
//
// Without this the `temporal server start-dev` child outlives the test run
// holding the inherited stdout pipe open, and `go test` reports "Test I/O
// incomplete 30s after exiting" — FAILING a run whose tests all passed.
// Observed while building these tests, not hypothetical.
func TestMain(m *testing.M) {
	code := m.Run()
	if triggerDevServer != nil {
		_ = triggerDevServer.Stop()
	}
	os.Exit(code)
}
