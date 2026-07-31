// Copyright (c) 2025 Reliant Labs
package mcp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/logging"
)

const (
	// lazyStartTimeout bounds how long a first-use spawn (npx cold start +
	// handshake, and for chrome-devtools the headless Chrome launch) may take
	// before the triggering tool call fails. Matches the generous
	// project-server load budget so first-run package downloads don't time out.
	lazyStartTimeout = defaultProjectServerLoadTimeout

	// lazyIdleTimeout is how long a lazily-started server may sit idle (no tool
	// calls in flight) before its subprocess is reaped. For chrome-devtools this
	// reclaims the node + headless Chrome resident set (~345MB) once the agent
	// stops driving the browser. A later tool call transparently re-spawns it.
	lazyIdleTimeout = 5 * time.Minute
)

// lazyClient wraps a real MCP Client so the underlying subprocess is only
// spawned on first tool use, not at session/daemon startup, and is torn down
// after an idle period.
//
// Why this exists: the built-in chrome-devtools MCP launches a node process
// plus a headless Chrome that together hold a large resident set, yet most chat
// sessions never drive a browser. Spawning it eagerly (the default AddServer →
// Initialize path) wastes a meaningful fraction of the cloud workspace pod's
// memory budget sitting idle.
//
// Tool discovery is preserved without a resident process: ListTools captures
// the server's tool manifest once via a throwaway probe (node starts, responds
// to tools/list, and exits — for chrome-devtools with --isolated/--headless
// this never launches Chrome), caches it, and reuses the cache thereafter. That
// keeps the agent's progressive tool discovery (load_tool) working while the
// heavy subprocess stays down until a browser tool is actually invoked.
type lazyClient struct {
	name    string
	cfg     config.MCPServer
	factory func() (Client, error)

	startTimeout time.Duration
	idleTimeout  time.Duration

	mu        sync.Mutex
	startCond *sync.Cond

	// onReap, when set, is called after the delegate has been torn down for
	// inactivity. The manager uses it to drop a session-scoped client from its
	// map once the session has stopped driving the server, so a long-lived
	// daemon does not accumulate one entry per thread it has ever served.
	onReap func()

	delegate       Client // nil unless a runtime session is currently live
	starting       bool
	inFlight       int
	idleTimer      *time.Timer
	closed         bool
	manifest       []Tool
	manifestCached bool
	serverInfo     *ServerInfo
}

// newLazyClientWithFactory builds a lazy client around a delegate factory. The
// factory is where the real subprocess is spawned (Manager.clientFactory, or a
// fake in tests).
func newLazyClientWithFactory(name string, cfg config.MCPServer, factory func() (Client, error)) *lazyClient {
	c := &lazyClient{
		name:         name,
		cfg:          cfg,
		factory:      factory,
		startTimeout: lazyStartTimeout,
		idleTimeout:  lazyIdleTimeout,
		serverInfo: &ServerInfo{
			Name:    name,
			Version: "unknown",
			Capabilities: ServerCapabilities{
				Tools: &ToolsCapability{},
			},
		},
	}
	c.startCond = sync.NewCond(&c.mu)
	return c
}

// Initialize is intentionally a no-op: it does NOT spawn the subprocess. The
// manager treats the server as available; the real subprocess is started on
// first tool use (begin) or first manifest probe (ListTools).
func (c *lazyClient) Initialize(ctx context.Context) error {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("mcp client %q is closed", c.name)
	}
	return nil
}

// begin ensures the delegate subprocess is running, registers an in-flight tool
// call, and pauses the idle timer. Callers must pair it with end().
func (c *lazyClient) begin() (Client, error) {
	c.mu.Lock()
	for c.starting {
		c.startCond.Wait()
	}
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("mcp client %q is closed", c.name)
	}
	if c.delegate != nil {
		d := c.delegate
		c.inFlight++
		c.stopIdleLocked()
		c.mu.Unlock()
		return d, nil
	}

	// We are the goroutine responsible for starting the delegate. Release the
	// lock during the slow spawn/handshake so ListTools, IsConnected (health
	// monitor), and other callers aren't blocked; they wait on startCond.
	c.starting = true
	c.mu.Unlock()

	logging.Info("Lazily starting MCP server on first use", "name", c.name)
	d, err := c.factory()
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), c.startTimeout)
		err = d.Initialize(ctx)
		cancel()
	}

	c.mu.Lock()
	c.starting = false
	c.startCond.Broadcast()
	if err != nil {
		c.mu.Unlock()
		if d != nil {
			_ = d.Close()
		}
		return nil, err
	}
	if c.closed {
		c.mu.Unlock()
		_ = d.Close()
		return nil, fmt.Errorf("mcp client %q is closed", c.name)
	}
	c.delegate = d
	if info := d.ServerInfo(); info != nil {
		c.serverInfo = info
	}
	c.inFlight++
	c.stopIdleLocked()
	c.mu.Unlock()
	return d, nil
}

// end releases an in-flight tool call and re-arms the idle timer once the last
// concurrent call completes.
func (c *lazyClient) end() {
	c.mu.Lock()
	if c.inFlight > 0 {
		c.inFlight--
	}
	if c.inFlight == 0 && !c.closed {
		c.armIdleLocked()
	}
	c.mu.Unlock()
}

func (c *lazyClient) stopIdleLocked() {
	if c.idleTimer != nil {
		c.idleTimer.Stop()
		c.idleTimer = nil
	}
}

func (c *lazyClient) armIdleLocked() {
	if c.idleTimeout <= 0 {
		return
	}
	c.stopIdleLocked()
	c.idleTimer = time.AfterFunc(c.idleTimeout, c.idleShutdown)
}

// idleShutdown reaps the delegate subprocess after an idle period. It skips
// shutdown if a call is in flight or a start is racing, so it never tears down
// an active session.
func (c *lazyClient) idleShutdown() {
	c.mu.Lock()
	if c.starting || c.inFlight > 0 || c.closed || c.delegate == nil {
		c.mu.Unlock()
		return
	}
	d := c.delegate
	onReap := c.onReap
	c.delegate = nil
	c.idleTimer = nil
	c.mu.Unlock()

	logging.Info("Shutting down idle MCP server", "name", c.name, "idleTimeout", c.idleTimeout)
	if err := d.Close(); err != nil {
		logging.Warn("Failed to close idle MCP server", "name", c.name, "error", err)
	}

	// Called with c.mu released — the manager takes its own lock here.
	if onReap != nil {
		onReap()
	}
}

func (c *lazyClient) cacheManifest(tools []Tool) {
	c.mu.Lock()
	c.manifest = tools
	c.manifestCached = true
	c.mu.Unlock()
}

// ListTools returns the server's tool manifest. It serves a cached manifest
// when available, reuses a live delegate if one happens to be running, and
// otherwise runs a throwaway probe (spawn → tools/list → close) so discovery
// works without keeping the subprocess resident.
func (c *lazyClient) ListTools() ([]Tool, error) {
	c.mu.Lock()
	if c.manifestCached {
		m := c.manifest
		c.mu.Unlock()
		return m, nil
	}
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("mcp client %q is closed", c.name)
	}
	live := c.delegate
	c.mu.Unlock()

	if live != nil {
		tools, err := live.ListTools()
		if err != nil {
			return nil, err
		}
		c.cacheManifest(tools)
		return tools, nil
	}

	probe, err := c.factory()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.startTimeout)
	defer cancel()
	if err := probe.Initialize(ctx); err != nil {
		_ = probe.Close()
		return nil, err
	}
	tools, err := probe.ListTools()
	if info := probe.ServerInfo(); info != nil {
		c.mu.Lock()
		c.serverInfo = info
		c.mu.Unlock()
	}
	if closeErr := probe.Close(); closeErr != nil {
		logging.Warn("Failed to close MCP tool-manifest probe", "name", c.name, "error", closeErr)
	}
	if err != nil {
		return nil, err
	}
	c.cacheManifest(tools)
	return tools, nil
}

// CallTool spawns the subprocess on demand (waiting for readiness) and executes
// the tool. For chrome-devtools this is where headless Chrome is first launched.
func (c *lazyClient) CallTool(name string, arguments map[string]interface{}) (*ToolResult, error) {
	d, err := c.begin()
	if err != nil {
		return nil, err
	}
	defer c.end()
	return d.CallTool(name, arguments)
}

// ListResources returns no resources without spawning: chrome-devtools exposes
// none, and the base client also degrades to empty when resources are
// unsupported. Keeping this side-effect-free preserves laziness.
func (c *lazyClient) ListResources() ([]Resource, error) {
	return []Resource{}, nil
}

// ListPrompts returns no prompts without spawning, for the same reason as
// ListResources.
func (c *lazyClient) ListPrompts() ([]Prompt, error) {
	return []Prompt{}, nil
}

// ReadResource routes through an on-demand spawn for correctness in the rare
// case a lazy server does expose resources.
func (c *lazyClient) ReadResource(uri string) (*ResourceContent, error) {
	d, err := c.begin()
	if err != nil {
		return nil, err
	}
	defer c.end()
	return d.ReadResource(uri)
}

// GetPrompt routes through an on-demand spawn, mirroring ReadResource.
func (c *lazyClient) GetPrompt(name string, arguments map[string]interface{}) (*PromptResult, error) {
	d, err := c.begin()
	if err != nil {
		return nil, err
	}
	defer c.end()
	return d.GetPrompt(name, arguments)
}

// Close shuts down the lazy client and any live delegate, and stops the idle
// timer. Subsequent calls fail fast.
func (c *lazyClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.stopIdleLocked()
	d := c.delegate
	c.delegate = nil
	c.mu.Unlock()

	if d != nil {
		return d.Close()
	}
	return nil
}

// IsConnected reports logical availability rather than whether the subprocess
// is currently running. Returning true while open keeps the manager's health
// monitor from treating an idle (deliberately not-running) lazy server as
// unhealthy and forcing a reconnect/re-spawn.
func (c *lazyClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed
}

// ServerInfo returns cached server info (a sensible default before the first
// spawn, then the real handshake result once captured).
func (c *lazyClient) ServerInfo() *ServerInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.serverInfo
}
