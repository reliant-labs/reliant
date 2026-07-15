// Copyright (c) 2025 Reliant Labs
//
//go:build e2e

// Package stories contains story-based end-to-end tests that exercise the
// full Reliant backend hermetically:
//
//	real Postgres (DATABASE_URL, same conventions as internal/db tests)
//	real Temporal (ephemeral dev server owned by TestMain)
//	real worker (workersetup.StartWorker — production registration path)
//	real Connect service handlers (services.ChatService etc., called in-process)
//	real local tool execution (LocalToolExecutor + daemon.LocalClient — the
//	  same execution path the daemon runtime uses, minus the NATS transport)
//	scripted LLM (injected drivers.DriverResolver — no network, no API keys)
//
// Run with:
//
//	DATABASE_URL=postgres://postgres:postgres@localhost:5433/reliant?sslmode=disable \
//	  go test -tags e2e ./e2e/stories/ -v
//
// (make e2e brings up Postgres via docker compose and runs the suite.)
//
// Each story is independent and parallel-safe: it creates its own user,
// project, chat, and Temporal worker on a unique task queue. The Postgres
// database and Temporal dev server are shared per test binary.
//
// Set E2E_VERBOSE=1 to see full server/worker logging while debugging.
package stories

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"

	"github.com/reliant-labs/reliant/internal/logging"
)

// temporalDev holds the shared ephemeral Temporal dev server for the whole
// test binary. Started once in TestMain, stopped after all tests.
var temporalDev struct {
	server   *testsuite.DevServer
	hostPort string // host:port of the dev server frontend
}

const temporalNamespace = "reliant"

// setupQuietLogging suppresses the (very chatty) server logging during tests.
// Set E2E_VERBOSE=1 to keep full logging for debugging.
func setupQuietLogging() {
	if os.Getenv("E2E_VERBOSE") == "1" {
		return
	}
	log.SetOutput(io.Discard)
	silent := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelError + 10, // effectively silent
	}))
	slog.SetDefault(silent)
	logging.DefaultOutput = io.Discard
	logging.Setup(slog.LevelError + 10)
}

func TestMain(m *testing.M) {
	setupQuietLogging()

	// The stories are DB-backed the same way internal/db tests are: they skip
	// when DATABASE_URL is not set. In that case don't pay for a Temporal dev
	// server either — every story checks requireStack(t) and skips.
	if os.Getenv("DATABASE_URL") == "" {
		fmt.Fprintln(os.Stderr, "e2e/stories: DATABASE_URL not set — all stories will be skipped (run `make postgres-up` and set DATABASE_URL)")
		os.Exit(m.Run())
	}

	if err := startTemporalDevServer(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e/stories: failed to start Temporal dev server: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	if temporalDev.server != nil {
		_ = temporalDev.server.Stop()
	}
	os.Exit(code)
}

// startTemporalDevServer boots an ephemeral (in-memory) Temporal dev server
// on a free port using the `temporal` CLI. If the CLI is not on PATH, the SDK
// downloads a cached copy — so this works on CI without extra setup.
func startTemporalDevServer() error {
	opts := testsuite.DevServerOptions{
		LogLevel: "never", // silence the dev server; use E2E_VERBOSE for our own logs
		ExtraArgs: []string{
			// Speed up visibility-dependent assertions (not strictly needed,
			// but keeps DescribeWorkflowExecution fresh).
			"--dynamic-config-value", "system.forceSearchAttributesCacheRefreshOnRead=true",
		},
	}
	if path, err := exec.LookPath("temporal"); err == nil {
		opts.ExistingPath = path
	}
	// The provided namespace is auto-registered on startup. Stories dial their
	// own client (with the production FlexibleDataConverter) via
	// temporal.NewExternalClient against FrontendHostPort.
	opts.ClientOptions = &client.Options{Namespace: temporalNamespace}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	server, err := testsuite.StartDevServer(ctx, opts)
	if err != nil {
		return err
	}
	temporalDev.server = server
	temporalDev.hostPort = server.FrontendHostPort()

	// Belt and braces: make sure the frontend is actually accepting
	// connections before any story dials it.
	conn, err := net.DialTimeout("tcp", temporalDev.hostPort, 10*time.Second)
	if err != nil {
		_ = server.Stop()
		return fmt.Errorf("temporal dev server not reachable at %s: %w", temporalDev.hostPort, err)
	}
	_ = conn.Close()
	return nil
}
