package mcp

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/config"
)

// spyDelegate is a fake MCP Client that records lifecycle calls so tests can
// assert when (and whether) the lazy wrapper spawns/closes the underlying
// subprocess.
type spyDelegate struct {
	mu         sync.Mutex
	initCalls  int
	closeCalls int
	toolCalls  int
	listCalls  int
	initErr    error
	connected  bool
}

func (s *spyDelegate) Initialize(ctx context.Context) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initCalls++
	if s.initErr != nil {
		return s.initErr
	}
	s.connected = true
	return nil
}

func (s *spyDelegate) ListTools() ([]Tool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listCalls++
	return []Tool{{Name: "take_screenshot"}, {Name: "navigate_page"}}, nil
}

func (s *spyDelegate) CallTool(name string, arguments map[string]interface{}) (*ToolResult, error) {
	_ = name
	_ = arguments
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolCalls++
	return &ToolResult{Content: []ToolContent{{Type: "text", Text: "ok"}}}, nil
}

func (s *spyDelegate) ListResources() ([]Resource, error) { return nil, nil }

func (s *spyDelegate) ReadResource(uri string) (*ResourceContent, error) {
	_ = uri
	return &ResourceContent{}, nil
}

func (s *spyDelegate) ListPrompts() ([]Prompt, error) { return nil, nil }

func (s *spyDelegate) GetPrompt(name string, arguments map[string]interface{}) (*PromptResult, error) {
	_ = name
	_ = arguments
	return &PromptResult{}, nil
}

func (s *spyDelegate) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeCalls++
	s.connected = false
	return nil
}

func (s *spyDelegate) IsConnected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connected
}

func (s *spyDelegate) ServerInfo() *ServerInfo {
	return &ServerInfo{Name: "chrome_devtools", Version: "1.6.0"}
}

func (s *spyDelegate) counts() (init, closes, calls, lists int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.initCalls, s.closeCalls, s.toolCalls, s.listCalls
}

func TestLazyClient_InitializeDoesNotSpawn(t *testing.T) {
	var created int32
	c := newLazyClientWithFactory("chrome-devtools", config.MCPServer{}, func() (Client, error) {
		atomic.AddInt32(&created, 1)
		return &spyDelegate{}, nil
	})

	if err := c.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize returned error: %v", err)
	}
	if got := atomic.LoadInt32(&created); got != 0 {
		t.Fatalf("Initialize spawned a delegate (%d created); expected lazy no-op", got)
	}
	if !c.IsConnected() {
		t.Fatal("lazy client should report connected (available) after Initialize")
	}
}

func TestLazyClient_ListToolsProbesWithoutStayingResident(t *testing.T) {
	spy := &spyDelegate{}
	var created int32
	c := newLazyClientWithFactory("chrome-devtools", config.MCPServer{}, func() (Client, error) {
		atomic.AddInt32(&created, 1)
		return spy, nil
	})

	tools, err := c.ListTools()
	if err != nil {
		t.Fatalf("ListTools error: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools from probe, got %d", len(tools))
	}
	init, closes, _, lists := spy.counts()
	if init != 1 || lists != 1 || closes != 1 {
		t.Fatalf("probe should spawn, list, then close exactly once: init=%d list=%d close=%d", init, lists, closes)
	}

	// Second call is served from cache: no new spawn.
	if _, err := c.ListTools(); err != nil {
		t.Fatalf("cached ListTools error: %v", err)
	}
	if got := atomic.LoadInt32(&created); got != 1 {
		t.Fatalf("second ListTools should use cache, but factory ran %d times", got)
	}
}

func TestLazyClient_CallToolSpawnsAndReuses(t *testing.T) {
	spy := &spyDelegate{}
	var created int32
	c := newLazyClientWithFactory("chrome-devtools", config.MCPServer{}, func() (Client, error) {
		atomic.AddInt32(&created, 1)
		return spy, nil
	})
	c.idleTimeout = 0 // disable idle shutdown for this test

	if _, err := c.CallTool("navigate_page", nil); err != nil {
		t.Fatalf("first CallTool error: %v", err)
	}
	if _, err := c.CallTool("take_screenshot", nil); err != nil {
		t.Fatalf("second CallTool error: %v", err)
	}

	init, closes, calls, _ := spy.counts()
	if got := atomic.LoadInt32(&created); got != 1 {
		t.Fatalf("CallTool should spawn once and reuse; factory ran %d times", got)
	}
	if init != 1 || calls != 2 || closes != 0 {
		t.Fatalf("expected init=1 calls=2 close=0, got init=%d calls=%d close=%d", init, calls, closes)
	}
}

func TestLazyClient_IdleShutdownReapsDelegate(t *testing.T) {
	spy := &spyDelegate{}
	c := newLazyClientWithFactory("chrome-devtools", config.MCPServer{}, func() (Client, error) {
		return spy, nil
	})
	c.idleTimeout = 30 * time.Millisecond

	if _, err := c.CallTool("navigate_page", nil); err != nil {
		t.Fatalf("CallTool error: %v", err)
	}

	// Wait for the idle timer to fire and reap the subprocess.
	deadline := time.After(2 * time.Second)
	for {
		_, closes, _, _ := spy.counts()
		if closes == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("idle shutdown did not close the delegate within deadline")
		case <-time.After(5 * time.Millisecond):
		}
	}

	// A subsequent call transparently re-spawns.
	if _, err := c.CallTool("take_screenshot", nil); err != nil {
		t.Fatalf("re-spawn CallTool error: %v", err)
	}
	init, _, calls, _ := spy.counts()
	if init != 2 || calls != 2 {
		t.Fatalf("expected re-spawn (init=2 calls=2), got init=%d calls=%d", init, calls)
	}
}

func TestLazyClient_ConcurrentCallToolSpawnsOnce(t *testing.T) {
	spy := &spyDelegate{}
	var created int32
	c := newLazyClientWithFactory("chrome-devtools", config.MCPServer{}, func() (Client, error) {
		atomic.AddInt32(&created, 1)
		time.Sleep(20 * time.Millisecond) // widen the start race window
		return spy, nil
	})
	c.idleTimeout = 0

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if _, err := c.CallTool("navigate_page", nil); err != nil {
				t.Errorf("concurrent CallTool error: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&created); got != 1 {
		t.Fatalf("concurrent calls should spawn exactly one delegate, got %d", got)
	}
	_, _, calls, _ := spy.counts()
	if calls != n {
		t.Fatalf("expected %d tool calls to reach delegate, got %d", n, calls)
	}
}

func TestLazyClient_StartFailurePropagates(t *testing.T) {
	c := newLazyClientWithFactory("chrome-devtools", config.MCPServer{}, func() (Client, error) {
		return &spyDelegate{initErr: fmt.Errorf("boom")}, nil
	})
	c.idleTimeout = 0

	if _, err := c.CallTool("navigate_page", nil); err == nil {
		t.Fatal("expected CallTool to surface start failure")
	}
	// After a failed start, a later call should retry (not be wedged in starting).
	if _, err := c.CallTool("navigate_page", nil); err == nil {
		t.Fatal("expected second CallTool to retry and surface failure")
	}
}

func TestLazyClient_CloseStopsFurtherUse(t *testing.T) {
	c := newLazyClientWithFactory("chrome-devtools", config.MCPServer{}, func() (Client, error) {
		return &spyDelegate{}, nil
	})
	if err := c.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	if c.IsConnected() {
		t.Fatal("closed lazy client should report not connected")
	}
	if _, err := c.CallTool("navigate_page", nil); err == nil {
		t.Fatal("CallTool after Close should error")
	}
}

func TestIsLazyStartServer(t *testing.T) {
	if !isLazyStartServer("chrome-devtools", config.MCPServer{Type: config.MCPStdio}) {
		t.Fatal("chrome-devtools stdio server should be lazy-start")
	}
	if isLazyStartServer("chrome-devtools", config.MCPServer{Type: config.MCPHTTP}) {
		t.Fatal("non-stdio chrome-devtools should not be lazy-start")
	}
	if isLazyStartServer("some-user-server", config.MCPServer{Type: config.MCPStdio}) {
		t.Fatal("arbitrary user server should not be lazy-start")
	}

	// A dir-scoped server is a tree indexer by declaration, and its SHARED
	// client is the one entry no tool call ever routes to — every call resolves
	// through the project's own client. Starting it eagerly held an indexer
	// resident in whatever directory the daemon happened to be in, for the
	// daemon's lifetime, for nothing.
	if !isLazyStartServer("gopls", config.MCPServer{Type: config.MCPStdio, DirScoped: true}) {
		t.Fatal("a dir-scoped stdio server should be lazy-start")
	}
	if isLazyStartServer("gopls", config.MCPServer{Type: config.MCPHTTP, DirScoped: true}) {
		t.Fatal("lazy start is about a heavyweight subprocess; a non-stdio server has none")
	}
}
