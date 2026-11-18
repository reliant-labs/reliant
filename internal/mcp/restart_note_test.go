// Copyright (c) 2025 Reliant Labs
package mcp

import (
	"context"
	"strings"
	"testing"
	"time"
)

// #2818: the browser came back fresh every minute or two mid-session, and the
// agent learned it only from "No page found". The call that starts a new
// browser for a session that had one must say so, once.

func restartNoteOf(t *testing.T, result *ToolResult) string {
	t.Helper()
	if result == nil || len(result.Content) == 0 {
		t.Fatal("tool call returned no content")
	}
	if first := result.Content[0].Text; strings.HasPrefix(first, "Note: ") {
		return first
	}
	return ""
}

func TestManager_SessionTellsItsCallerTheBrowserRestartedAfterRemoval(t *testing.T) {
	m, _ := newFactoryManager()
	defer func() { _ = m.Close() }()
	ctx := context.Background()

	if err := m.AddServer(ctx, chromeDevtoolsServerName, stdioServer()); err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	first, err := m.CallTool("thread-a", chromeDevtoolsServerName, "new_page", nil)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if note := restartNoteOf(t, first); note != "" {
		t.Fatalf("a session's first browser is not a restart, got note %q", note)
	}

	if err := m.RemoveServer(chromeDevtoolsServerName, "disabled in the MCP config of project /p"); err != nil {
		t.Fatalf("RemoveServer: %v", err)
	}
	if err := m.AddServer(ctx, chromeDevtoolsServerName, stdioServer()); err != nil {
		t.Fatalf("AddServer again: %v", err)
	}

	after, err := m.CallTool("thread-a", chromeDevtoolsServerName, "list_pages", nil)
	if err != nil {
		t.Fatalf("call after re-add: %v", err)
	}
	note := restartNoteOf(t, after)
	if !strings.Contains(note, "browser restarted") || !strings.Contains(note, "disabled in the MCP config of project /p") {
		t.Fatalf("expected a browser-restart note naming the removal, got %q", note)
	}
	if len(after.Content) != 2 || after.Content[1].Text != "ok" {
		t.Fatalf("the note must precede the tool's own output, not replace it: %+v", after.Content)
	}

	again, err := m.CallTool("thread-a", chromeDevtoolsServerName, "list_pages", nil)
	if err != nil {
		t.Fatalf("third call: %v", err)
	}
	if note := restartNoteOf(t, again); note != "" {
		t.Fatalf("the restart is reported once, got it again: %q", note)
	}

	other, err := m.CallTool("thread-b", chromeDevtoolsServerName, "new_page", nil)
	if err != nil {
		t.Fatalf("other session: %v", err)
	}
	if note := restartNoteOf(t, other); note != "" {
		t.Fatalf("a session that never had a browser lost nothing, got %q", note)
	}
}

func TestManager_SessionTellsItsCallerTheBrowserWasReapedIdle(t *testing.T) {
	m, _ := newFactoryManager()
	defer func() { _ = m.Close() }()

	if err := m.AddServer(context.Background(), chromeDevtoolsServerName, stdioServer()); err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	if _, err := m.CallTool("thread-a", chromeDevtoolsServerName, "new_page", nil); err != nil {
		t.Fatalf("call: %v", err)
	}
	key := sessionClientKey(chromeDevtoolsServerName, "thread-a")
	m.mu.RLock()
	lc := m.sessionClients[key].(*lazyClient)
	m.mu.RUnlock()

	lc.idleShutdown() // evicts the session client synchronously through onReap

	m.mu.RLock()
	_, stillThere := m.sessionClients[key]
	m.mu.RUnlock()
	if stillThere {
		t.Fatal("the reaped session client should have been evicted")
	}

	result, err := m.CallTool("thread-a", chromeDevtoolsServerName, "list_pages", nil)
	if err != nil {
		t.Fatalf("call after reap: %v", err)
	}
	if note := restartNoteOf(t, result); !strings.Contains(note, "idle") {
		t.Fatalf("expected a note saying the browser was reaped idle, got %q", note)
	}
}

func TestLazyClient_ReplacesADeadDelegateAndSaysSo(t *testing.T) {
	var spawned []*spyDelegate
	lc := newLazyClientWithFactory(chromeDevtoolsServerName, stdioServer(), func() (Client, error) {
		d := &spyDelegate{}
		spawned = append(spawned, d)
		return d, nil
	})
	lc.idleTimeout = time.Hour
	defer func() { _ = lc.Close() }()

	if _, err := lc.CallTool("new_page", nil); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// The process exits on its own: the client stops reporting connected.
	spawned[0].mu.Lock()
	spawned[0].connected = false
	spawned[0].mu.Unlock()

	result, err := lc.CallTool("list_pages", nil)
	if err != nil {
		t.Fatalf("call after the process died: %v", err)
	}
	if len(spawned) != 2 {
		t.Fatalf("a dead delegate must be replaced by a new process, spawned %d", len(spawned))
	}
	if note := restartNoteOf(t, result); !strings.Contains(note, "process exited") {
		t.Fatalf("expected a note saying the process exited, got %q", note)
	}
}

func TestLazyClient_StatelessServerRestartsSilently(t *testing.T) {
	lc := newLazyClientWithFactory("lsp", stdioServer(), func() (Client, error) {
		return &spyDelegate{}, nil
	})
	lc.idleTimeout = time.Hour
	defer func() { _ = lc.Close() }()

	if _, err := lc.CallTool("hover", nil); err != nil {
		t.Fatalf("first call: %v", err)
	}
	lc.idleShutdown()
	result, err := lc.CallTool("hover", nil)
	if err != nil {
		t.Fatalf("call after reap: %v", err)
	}
	if note := restartNoteOf(t, result); note != "" {
		t.Fatalf("a server with no caller-visible state restarts silently, got %q", note)
	}
}
