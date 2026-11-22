package shadowdb

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/redact"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// A shadow server that goes away.
//
// Resolve probes each candidate server before it returns it, but a probe is a
// moment: the postgres it found may be another checkout's dev stack (the
// localhost:5432 convention, or a port block that happens to land there),
// and that stack can be stopped while this run is still applying migrations
// to it. The reader then failed with "connection refused" or "terminating
// connection due to administrator command" (SQLSTATE 57P01), and generate
// failed a step over a server it never needed: embedded postgres was always
// available. So a reader whose PROBED server went away retries once on
// embedded postgres, and inside a run every later reader goes there too.
//
// The FORGE_TEST_POSTGRES_URL override is never replaced: it is honored
// unprobed precisely so that a wrong one fails loudly (Resolve).

// applyTemplate and applyShadow apply a migration set to a server; variables
// so a test can stand in for a server that went away.
var (
	applyTemplate = (*schemadef.MigrationSet).ApplyTemplateAt
	applyShadow   = schemadef.ApplyAndIntrospectSetShadowAt
)

// wentAway reports whether err says the probed shadow server is gone, so the
// reader should retry on embedded postgres.
func wentAway(server string, err error) bool {
	if err == nil || server == "" || getenv(pgtest.EnvBaseURL) != "" {
		return false
	}
	return serverGone(err)
}

// goneErrnos are the socket errors of a server that is not there (refused) or
// went away (reset, broken pipe). Windows reports Winsock codes instead of
// the POSIX ones, so it adds its own (fallback_windows.go).
var goneErrnos = append([]syscall.Errno{syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.EPIPE}, platformGoneErrnos...)

// serverGone reports whether err is the server being unreachable or shutting
// down, as opposed to a migration it rejected or credentials it refused.
func serverGone(err error) bool {
	for _, errno := range goneErrnos {
		if errors.Is(err, errno) {
			return true
		}
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "57P01", // admin_shutdown
			"57P02", // crash_shutdown
			"57P03": // cannot_connect_now
			return true
		}
	}
	return false
}

// demoteServer moves the open run off server, onto embedded postgres, when
// server is still the run's choice for projectDir.
func demoteServer(projectDir, server string, err error) {
	key := projectKey(projectDir)
	run.mu.Lock()
	demoted := run.depth > 0 && run.servers[key] == server
	if demoted {
		run.servers[key] = ""
	}
	run.mu.Unlock()
	if demoted {
		warnFallback(server, err)
	}
}

// warnFallback says the shadow moved, once per move.
func warnFallback(server string, err error) {
	fmt.Fprintf(os.Stderr, "  ⚠️  the postgres at %s went away while forge read the applied schema (%v); using embedded postgres instead\n", redact.DSN(server), err)
}
