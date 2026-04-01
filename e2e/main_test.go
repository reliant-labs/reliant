// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/temporal"
)

// sharedTemporal holds the shared Temporal server for all tests
var sharedTemporal struct {
	sync.Mutex
	server  *temporal.EmbeddedServer
	tmpDir  string
	started bool
}

// setupQuietLogging suppresses verbose logging during tests.
// Set E2E_VERBOSE=1 to enable full logging for debugging.
func setupQuietLogging() {
	if os.Getenv("E2E_VERBOSE") == "1" {
		return
	}

	// Suppress standard library log output
	log.SetOutput(io.Discard)

	// Set slog to only show errors
	silentHandler := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelError + 10, // Effectively silent
	})
	slog.SetDefault(slog.New(silentHandler))

	// Also set the logging package's default output
	logging.DefaultOutput = io.Discard
}

// TestMain runs once before all tests
func TestMain(m *testing.M) {
	// Suppress verbose logging unless E2E_VERBOSE=1
	setupQuietLogging()

	// Create shared temp directory for Temporal DB
	tmpDir, err := os.MkdirTemp("", "e2e-shared-temporal-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)
		os.Exit(1)
	}
	sharedTemporal.tmpDir = tmpDir

	// Start shared Temporal server with retries
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		cfg := &temporal.ServerConfig{
			LogLevel:   "silent", // Suppress all Temporal server logs
			Namespaces: []string{"reliant"},
			Ephemeral:  true, // Use in-memory for faster tests
		}

		server, err := temporal.NewEmbeddedServer(cfg)
		if err != nil {
			lastErr = fmt.Errorf("attempt %d: failed to create temporal server: %w", attempt, err)
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
			continue
		}

		// Start with timeout
		startCtx, startCancel := context.WithTimeout(context.Background(), 30*time.Second)
		startDone := make(chan error, 1)
		go func() {
			startDone <- server.Start()
		}()

		select {
		case err := <-startDone:
			startCancel()
			if err != nil {
				lastErr = fmt.Errorf("attempt %d: failed to start temporal server: %w", attempt, err)
				time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
				continue
			}
		case <-startCtx.Done():
			startCancel()
			lastErr = fmt.Errorf("attempt %d: timeout starting temporal server", attempt)
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
			continue
		}

		// Success!
		sharedTemporal.server = server
		sharedTemporal.started = true
		break
	}

	if !sharedTemporal.started {
		fmt.Fprintf(os.Stderr, "[TestMain] Failed to start shared Temporal server after 3 attempts: %v\n", lastErr)
		os.Exit(1)
	}

	// Run tests
	code := m.Run()

	// Cleanup
	if sharedTemporal.server != nil {
		sharedTemporal.server.Stop()
	}
	os.RemoveAll(tmpDir)

	os.Exit(code)
}

// GetSharedTemporalServer returns the shared Temporal server for tests.
// This allows all tests to share a single Temporal instance.
func GetSharedTemporalServer() *temporal.EmbeddedServer {
	sharedTemporal.Lock()
	defer sharedTemporal.Unlock()

	if !sharedTemporal.started {
		panic("GetSharedTemporalServer called but TestMain hasn't started the server yet")
	}
	return sharedTemporal.server
}
