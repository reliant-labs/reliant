// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/connectorgrant"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

// stubResolver returns a session that a test can mutate mid-flight, standing
// in for a grant edited while an MCP session is open.
type stubResolver struct {
	session *Session
	err     error
}

func (s *stubResolver) Resolve(context.Context, string) (*Session, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.session, nil
}

// TestGrantNarrowingTakesEffectMidSession is the gap that per-request
// authentication alone does not close.
//
// The MCP SDK builds one server per session and reuses it, so the tool list
// and policy captured at connect time would otherwise stay authoritative for
// the life of the connection. Revoking already works (the HTTP layer
// re-authenticates every request); narrowing has to be caught here.
func TestGrantNarrowingTakesEffectMidSession(t *testing.T) {
	sender := &fakeSender{}
	sess := testSession()

	// The grant as it stood when the session opened.
	live := testSession()
	resolver := &stubResolver{session: live}
	deps := Deps{Sender: sender, Resolver: resolver}

	res := callTool(t, sess, deps, "read_file", map[string]any{"path": "/workspace/a.txt"})
	require.False(t, res.IsError, "call should succeed before the grant is narrowed: %s", resultText(t, res))
	require.Len(t, sender.calls, 1)

	// The user removes read_file from the connector.
	narrowed := testSession()
	narrowed.ToolNames = []string{"search"}
	narrowed.Policy = &daemonpolicy.Policy{
		GrantID:  "grant-1",
		Tools:    toSet(CommandsForTools([]string{"search"})),
		PathRoot: "/workspace",
		ExecMode: daemonpolicy.ExecDenied,
	}
	resolver.session = narrowed

	res = callTool(t, sess, deps, "read_file", map[string]any{"path": "/workspace/a.txt"})
	require.True(t, res.IsError, "a tool removed from the grant must stop working immediately")
	require.Contains(t, resultText(t, res), "no longer allowed")
	require.Len(t, sender.calls, 1, "the narrowed call must not have reached the daemon")
}

func TestRevokedGrantEndsAnOpenSession(t *testing.T) {
	sender := &fakeSender{}
	sess := testSession()
	resolver := &stubResolver{err: connectorgrant.ErrNotFound}

	res := callTool(t, sess, Deps{Sender: sender, Resolver: resolver}, "read_file",
		map[string]any{"path": "/workspace/a.txt"})

	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "no longer valid")
	require.Contains(t, resultText(t, res), "do not retry")
	require.Empty(t, sender.calls)
}

// TestAuditRecordsIntentBeforeDispatch is the property that makes the audit
// log trustworthy across a crash: the row exists before the command is sent,
// so a server that dies mid-call still leaves evidence it was attempted.
func TestAuditRecordsIntentBeforeDispatch(t *testing.T) {
	audit := &fakeAudit{}
	sender := &fakeSender{}
	sess := testSession()

	callTool(t, sess, Deps{Sender: sender, Audit: audit}, "read_file",
		map[string]any{"path": "/workspace/a.txt"})

	require.Len(t, audit.entries, 1)
	entry := audit.entries[0]

	// Begun before dispatch, then completed.
	require.NotEmpty(t, entry.auditID)
	require.True(t, audit.completed[entry.auditID], "the intent row must be resolved after the call returns")
	require.Equal(t, connectorgrant.AuditCompleted, entry.Status)
	require.False(t, entry.Denied)
}

// TestUnresolvedAuditRowSurvivesACrash simulates the process dying between
// dispatch and completion: the row stays in 'started', which is exactly the
// residue an investigator needs.
func TestUnresolvedAuditRowSurvivesACrash(t *testing.T) {
	store := newMemStore()
	sink := &storeAudit{store: store, logger: testLogger()}

	id := sink.Begin(context.Background(), AuditEntry{
		GrantID:  "grant-1",
		UserID:   "user-1",
		DaemonID: "daemon-1",
		ToolName: "run_command",
		Command:  "exec.run",
		At:       time.Now(),
		Status:   connectorgrant.AuditStarted,
	})
	require.NotEmpty(t, id)

	// No Complete call — the process "died" here.
	records := store.records()
	require.Len(t, records, 1)
	require.Equal(t, connectorgrant.AuditStarted, records[0].Status,
		"a dispatched-but-unresolved call must remain visible as started")
}

// TestDeniedCallIsRecordedInOneShot: a refusal never reaches the daemon, so
// there is no window in which the record could be lost and no intent row is
// needed.
func TestDeniedCallIsRecordedInOneShot(t *testing.T) {
	audit := &fakeAudit{}
	sender := &fakeSender{}
	sess := testSession()

	callTool(t, sess, Deps{Sender: sender, Audit: audit}, "read_file",
		map[string]any{"path": "/etc/passwd"})

	require.Len(t, audit.entries, 1)
	require.True(t, audit.entries[0].Denied)
	require.Equal(t, connectorgrant.AuditDenied, audit.entries[0].Status)
	require.Empty(t, sender.calls)
}

// testLogger returns a logger that discards output, so a test exercising an
// error path does not spray the test log.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
