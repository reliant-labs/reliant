// Copyright (c) 2025 Reliant Labs
//
//go:build e2e

// Package stories contains story-based end-to-end tests that exercise the
// full Reliant backend hermetically:
//
//	real Postgres (internal/db's test database: forge pgtest's embedded
//	  server, or FORGE_TEST_POSTGRES_URL)
//	real Temporal (ephemeral dev server owned by TestMain)
//	real worker (workersetup.NewWorker — production registration path)
//	real Connect service handlers (chat.Service etc., called in-process)
//	real local tool execution (LocalToolExecutor + daemon.LocalClient — the
//	  same execution path the daemon runtime uses, minus the NATS transport)
//	scripted LLM (injected drivers.DriverResolver — no network, no API keys)
//
// Run with:
//
//	go test -tags e2e ./e2e/stories/ -v     # or: task test:e2e
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
	"strings"
	"sync"
	"testing"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"

	"github.com/reliant-labs/reliant/internal/logging"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
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
//
// The server's ERROR records are kept, in serverErrors, and printed when the
// run fails: a story whose handler failed sees only the redacted wire error
// ("internal: failed to create chat"), and the cause is in the ERROR the
// handler logged. Discarding those left a release-gate failure with no
// evidence to find its cause by.
func setupQuietLogging() {
	if os.Getenv("E2E_VERBOSE") == "1" {
		return
	}
	log.SetOutput(io.Discard)
	silent := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelError + 10, // effectively silent
	}))
	slog.SetDefault(silent)
	logging.DefaultOutput = serverErrors
	logging.Setup(logging.Settings{}, slog.LevelError)
}

// serverErrors keeps the last serverErrorLines lines the server logged at
// ERROR and above.
var serverErrors = &lineTail{max: serverErrorLines}

const serverErrorLines = 300

type lineTail struct {
	mu    sync.Mutex
	max   int
	lines []string
}

func (l *lineTail) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, strings.Split(strings.TrimRight(string(p), "\n"), "\n")...)
	if over := len(l.lines) - l.max; over > 0 {
		l.lines = append([]string(nil), l.lines[over:]...)
	}
	return len(p), nil
}

func (l *lineTail) dump(w io.Writer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.lines) == 0 {
		return
	}
	fmt.Fprintf(w, "e2e/stories: the last %d server ERROR lines of this run:\n", len(l.lines))
	for _, line := range l.lines {
		fmt.Fprintln(w, "  "+line)
	}
}

func TestMain(m *testing.M) {
	setupQuietLogging()

	// The stories always run: their database is internal/db's test database,
	// which needs no configuration. (They used to skip without DATABASE_URL,
	// and CI reported green for months while running 1 of 11 stories — which
	// is how three of them stopped compiling and three more collided on shared
	// tool-call ids with nobody noticing.)
	if err := startTemporalDevServer(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e/stories: failed to start Temporal dev server: %v\n", err)
		os.Exit(1)
	}

	// Before any story starts a workflow: every breaker reads the grace when
	// its workflow starts. See storyDaemonOfflinePauseGrace.
	restoreGrace := v2.SetDaemonOfflinePauseGraceForTest(storyDaemonOfflinePauseGrace)
	code := m.Run()
	restoreGrace()
	if code != 0 {
		serverErrors.dump(os.Stderr)
	}

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
