// Copyright (c) 2025 Reliant Labs
package mcp

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/config"
)

// newFactoryManager returns a Manager whose clients are fakes, plus a recorder
// of every delegate the manager spawned. Nothing here launches a subprocess.
func newFactoryManager() (*Manager, *spawnRecorder) {
	rec := &spawnRecorder{}
	m := NewManager()
	m.clientFactory = func(name string, cfg config.MCPServer) (Client, error) {
		return rec.next(name), nil
	}
	return m, rec
}

type spawnRecorder struct {
	mu       sync.Mutex
	spawned  []*spyDelegate
	byServer []string
}

func (r *spawnRecorder) next(name string) *spyDelegate {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := &spyDelegate{}
	r.spawned = append(r.spawned, d)
	r.byServer = append(r.byServer, name)
	return d
}

func (r *spawnRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.spawned)
}

// lastCalledDelegate returns the single delegate that has recorded tool calls,
// so a test can tell WHICH subprocess a call landed on.
func (r *spawnRecorder) calledDelegates() []*spyDelegate {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*spyDelegate
	for _, d := range r.spawned {
		if _, _, calls, _ := d.counts(); calls > 0 {
			out = append(out, d)
		}
	}
	return out
}

func stdioServer() config.MCPServer {
	return config.MCPServer{Type: config.MCPStdio, Enabled: true, Command: "true"}
}

// The bug this guards, measured on real-workflow run 1: five fan-out threads
// drove ONE chrome-devtools subprocess, whose selected page is a single mutable
// process-global pointer. A thread's `select_page` silently repointed its
// siblings' next calls — no error, wrong page. Concurrent threads must land on
// different subprocesses.
func TestManager_SessionScopedServerIsolatesConcurrentThreads(t *testing.T) {
	m, rec := newFactoryManager()
	defer func() { _ = m.Close() }()

	if err := m.AddServer(context.Background(), chromeDevtoolsServerName, stdioServer()); err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	if rec.count() != 0 {
		t.Fatalf("registering a lazy server must not spawn anything, spawned %d", rec.count())
	}

	if _, err := m.CallTool("thread-a", chromeDevtoolsServerName, "select_page", nil); err != nil {
		t.Fatalf("thread-a call: %v", err)
	}
	if _, err := m.CallTool("thread-b", chromeDevtoolsServerName, "select_page", nil); err != nil {
		t.Fatalf("thread-b call: %v", err)
	}

	called := rec.calledDelegates()
	if len(called) != 2 {
		t.Fatalf("two threads must drive two separate subprocesses, got %d", len(called))
	}

	// The same thread must keep its own subprocess, or its own page selection
	// would not survive its next call either.
	if _, err := m.CallTool("thread-a", chromeDevtoolsServerName, "take_screenshot", nil); err != nil {
		t.Fatalf("thread-a second call: %v", err)
	}
	if _, _, calls, _ := called[0].counts(); calls != 2 {
		t.Fatalf("thread-a's second call went to a different subprocess: first delegate saw %d calls", calls)
	}
	if _, _, calls, _ := called[1].counts(); calls != 1 {
		t.Fatalf("thread-a's second call leaked onto thread-b's subprocess: it saw %d calls", calls)
	}
}

// Session isolation is opt-in per server. Spawning a subprocess per thread for
// every MCP server would be a resource bug, and stateless servers do not need it.
func TestManager_NonSessionScopedServerStaysShared(t *testing.T) {
	m, rec := newFactoryManager()
	defer func() { _ = m.Close() }()

	if err := m.AddServer(context.Background(), "fetch", stdioServer()); err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("a non-lazy server spawns eagerly, spawned %d", rec.count())
	}

	for _, session := range []string{"thread-a", "thread-b", ""} {
		if _, err := m.CallTool(session, "fetch", "get", nil); err != nil {
			t.Fatalf("call for session %q: %v", session, err)
		}
	}
	if rec.count() != 1 {
		t.Fatalf("a non-session-scoped server must serve every caller from one client, spawned %d", rec.count())
	}
}

// A caller with no thread — the CLI, a one-off daemon command — gets the shared
// client, which is the behaviour that existed before session scoping.
func TestManager_EmptySessionUsesTheSharedClient(t *testing.T) {
	m, rec := newFactoryManager()
	defer func() { _ = m.Close() }()

	if err := m.AddServer(context.Background(), chromeDevtoolsServerName, stdioServer()); err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	for _, session := range []string{"", "   "} {
		if _, err := m.CallTool(session, chromeDevtoolsServerName, "list_pages", nil); err != nil {
			t.Fatalf("call for session %q: %v", session, err)
		}
	}
	if got := rec.count(); got != 1 {
		t.Fatalf("empty session keys must share one client, spawned %d", got)
	}
}

// Session clients are the SAME server spawned again. The model, the UI and the
// health reporting must keep seeing exactly one chrome-devtools.
func TestManager_SessionClientsAreInvisibleToListings(t *testing.T) {
	m, _ := newFactoryManager()
	defer func() { _ = m.Close() }()

	if err := m.AddServer(context.Background(), chromeDevtoolsServerName, stdioServer()); err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	for _, session := range []string{"thread-a", "thread-b", "thread-c"} {
		if _, err := m.CallTool(session, chromeDevtoolsServerName, "navigate_page", nil); err != nil {
			t.Fatalf("call: %v", err)
		}
	}

	if got := len(m.GetAllClients()); got != 1 {
		t.Fatalf("GetAllClients must report one server, got %d", got)
	}
	if got := len(m.GetHealthStatus()); got != 1 {
		t.Fatalf("GetHealthStatus must report one server, got %d", got)
	}
	for name := range m.GetAllClients() {
		if name != chromeDevtoolsServerName {
			t.Fatalf("a session key leaked into a server name: %q", name)
		}
	}
}

// A daemon runs for weeks and serves a new thread id per workflow node. The map
// entry has to go when the session stops driving the browser, or it is a leak.
func TestManager_SessionClientEvictsItselfWhenReaped(t *testing.T) {
	m, _ := newFactoryManager()
	defer func() { _ = m.Close() }()

	if err := m.AddServer(context.Background(), chromeDevtoolsServerName, stdioServer()); err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	if _, err := m.CallTool("thread-a", chromeDevtoolsServerName, "navigate_page", nil); err != nil {
		t.Fatalf("call: %v", err)
	}

	m.mu.RLock()
	lc, ok := m.sessionClients[sessionClientKey(chromeDevtoolsServerName, "thread-a")].(*lazyClient)
	m.mu.RUnlock()
	if !ok {
		t.Fatal("expected a lazy session client to be registered")
	}

	// Reap as the idle timer would.
	lc.idleShutdown()

	deadline := time.Now().Add(2 * time.Second)
	for {
		m.mu.RLock()
		n := len(m.sessionClients)
		m.mu.RUnlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("session client was not evicted after its subprocess was reaped (%d left)", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The composite key must never be mistakable for a server name: the adapter
// layer rejects "::" in a logical name, so a session key can never reach the
// model inside an mcp__<server>__<tool> tool name.
func TestSessionClientKeyCannotCollideWithAServerName(t *testing.T) {
	key := sessionClientKey(chromeDevtoolsServerName, "thread-a")
	if key == chromeDevtoolsServerName {
		t.Fatal("session key must differ from the logical server name")
	}
	if !strings.Contains(key, "::") {
		t.Fatalf("session key %q must carry the '::' marker that logical server names forbid", key)
	}
}
