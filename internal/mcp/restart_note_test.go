// Copyright (c) 2025 Reliant Labs
package mcp

import (
	"context"
	"errors"
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

// scriptedDelegate answers each tool call with the next step of its script; a
// step may also act while the call is in flight.
type scriptedDelegate struct {
	*spyDelegate
	steps []func() (*ToolResult, error)
}

func (s *scriptedDelegate) CallTool(name string, arguments map[string]interface{}) (*ToolResult, error) {
	s.mu.Lock()
	var step func() (*ToolResult, error)
	if len(s.steps) > 0 {
		step, s.steps = s.steps[0], s.steps[1:]
	}
	s.mu.Unlock()
	if step == nil {
		return s.spyDelegate.CallTool(name, arguments)
	}
	return step()
}

// browserReapedIdle returns a browser client whose first process was reaped
// idle, so its next call must report the restart, and whose replacement
// process answers with the given script.
func browserReapedIdle(t *testing.T, steps ...func() (*ToolResult, error)) *lazyClient {
	t.Helper()
	spawns := 0
	lc := newLazyClientWithFactory(chromeDevtoolsServerName, stdioServer(), func() (Client, error) {
		spawns++
		d := &scriptedDelegate{spyDelegate: &spyDelegate{}}
		if spawns == 2 {
			d.steps = steps
		}
		return d, nil
	})
	lc.idleTimeout = time.Hour
	t.Cleanup(func() { _ = lc.Close() })

	if _, err := lc.CallTool("new_page", nil); err != nil {
		t.Fatalf("first call: %v", err)
	}
	lc.idleShutdown()
	return lc
}

func ok() (*ToolResult, error) {
	return &ToolResult{Content: []ToolContent{{Type: "text", Text: "ok"}}}, nil
}

func TestLazyClient_RestartNoteSurvivesAFailedCall(t *testing.T) {
	lc := browserReapedIdle(t, func() (*ToolResult, error) {
		return nil, errors.New("navigation timed out")
	})

	_, err := lc.CallTool("navigate_page", nil)
	if err == nil {
		t.Fatal("the scripted failure must surface")
	}
	if !strings.Contains(err.Error(), "browser restarted") {
		t.Fatalf("a failed call still mentions the restart, got %q", err)
	}

	result, err := lc.CallTool("list_pages", nil)
	if err != nil {
		t.Fatalf("call after the failure: %v", err)
	}
	if note := restartNoteOf(t, result); !strings.Contains(note, "idle") {
		t.Fatalf("an error is not a delivered result: the next success must still report the restart, got %q", note)
	}

	again, err := lc.CallTool("list_pages", nil)
	if err != nil {
		t.Fatalf("third call: %v", err)
	}
	if note := restartNoteOf(t, again); note != "" {
		t.Fatalf("once delivered, the restart is not reported again, got %q", note)
	}
}

func TestLazyClient_RestartNoteIsDeliveredOnce(t *testing.T) {
	lc := browserReapedIdle(t)

	result, err := lc.CallTool("list_pages", nil)
	if err != nil {
		t.Fatalf("call after reap: %v", err)
	}
	if note := restartNoteOf(t, result); !strings.Contains(note, "idle") {
		t.Fatalf("expected the restart note, got %q", note)
	}
	again, err := lc.CallTool("list_pages", nil)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if note := restartNoteOf(t, again); note != "" {
		t.Fatalf("the restart is reported once, got it again: %q", note)
	}
}

func TestLazyClient_NewerRestartDuringACallIsKept(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail bool
	}{
		{"failed call", true},
		{"successful call", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var lc *lazyClient
			lc = browserReapedIdle(t, func() (*ToolResult, error) {
				// Another restart is recorded while this call runs.
				lc.setRestartReason("a newer restart")
				if tc.fail {
					return nil, errors.New("navigation timed out")
				}
				return ok()
			})

			first, err := lc.CallTool("navigate_page", nil)
			if tc.fail != (err != nil) {
				t.Fatalf("scripted outcome not surfaced: err=%v", err)
			}
			if !tc.fail {
				if note := restartNoteOf(t, first); !strings.Contains(note, "idle") {
					t.Fatalf("the in-flight call reports the restart it began with, got %q", note)
				}
			}

			next, err := lc.CallTool("list_pages", nil)
			if err != nil {
				t.Fatalf("next call: %v", err)
			}
			if note := restartNoteOf(t, next); !strings.Contains(note, "a newer restart") {
				t.Fatalf("the newer restart must still be reported, got %q", note)
			}
		})
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
