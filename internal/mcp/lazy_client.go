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
	// before the triggering tool call fails.
	//
	// It is twice the eager project-server budget because a lazy start is
	// paid inside a tool call the agent is waiting on, where waiting longer
	// beats failing: a failed start costs the call AND a retry cooldown.
	// Measured on a loaded cloud machine (load average ~90 on 31 CPUs): the
	// chrome-devtools initialize handshake took 30-50s through npx, so the
	// old 45s budget failed it about half the time with "context deadline
	// exceeded" (chromeDevtoolsLaunch avoids npx where it can).
	lazyStartTimeout = 2 * defaultProjectServerLoadTimeout

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

	// started records that a delegate has run at least once, so losing it
	// lost whatever state a caller built up in it.
	started bool
	// restartReason, when set, says why the delegate a caller was using is
	// gone. Tool calls report it (restartNote) until one delivers a result
	// carrying it, so an agent learns its browser restarted instead of chasing
	// "No page found". Only stateful servers carry one (announcesRestarts).
	restartReason string
	// restartGen counts the restart reasons recorded, so a call clears only
	// the reason it delivered and never a newer one recorded meanwhile.
	restartGen uint64
}

// announcesRestarts reports whether losing this server's process loses state a
// caller can see. It is the session-scoped servers — the browser — whose pages,
// tabs and storage live in the process; every other server answers the same
// from a fresh process, so a restart note would be noise.
func (c *lazyClient) announcesRestarts() bool {
	return isSessionScopedServer(c.name, c.cfg)
}

// noteLostLocked records why the running delegate is gone, if a caller could
// have depended on it. c.mu must be held.
func (c *lazyClient) noteLostLocked(reason string) {
	if c.started && c.announcesRestarts() {
		c.recordRestartLocked(reason)
	}
}

// recordRestartLocked makes reason the pending restart reason, superseding any
// earlier one. c.mu must be held.
func (c *lazyClient) recordRestartLocked(reason string) {
	c.restartReason = reason
	c.restartGen++
}

// setRestartReason carries a loss recorded outside this client — the manager
// replaced a session's client — into the client that replaces it.
func (c *lazyClient) setRestartReason(reason string) {
	c.mu.Lock()
	c.recordRestartLocked(reason)
	c.mu.Unlock()
}

// pendingRestart returns the restart reason no caller has been shown yet, and
// its generation for restartDelivered.
func (c *lazyClient) pendingRestart() (reason string, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.restartReason, c.restartGen
}

// restartDelivered clears the pending restart reason once a caller has been
// handed a result carrying it — unless a newer restart was recorded since,
// which the next call must still report.
func (c *lazyClient) restartDelivered(gen uint64) {
	c.mu.Lock()
	if c.restartGen == gen {
		c.restartReason = ""
	}
	c.mu.Unlock()
}

// takeRestartReason returns and clears the pending restart reason.
func (c *lazyClient) takeRestartReason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	reason := c.restartReason
	c.restartReason = ""
	return reason
}

// hasStarted reports whether a delegate has ever run.
func (c *lazyClient) hasStarted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.started
}

// restartNote is the text a tool result carries when the server it ran on was
// started fresh after the caller's previous one was lost.
func restartNote(name, reason string) string {
	if name == chromeDevtoolsServerName {
		return fmt.Sprintf("Note: the browser restarted before this call (%s). "+
			"Pages, tabs and storage from earlier calls (cookies, localStorage, sign-ins) were reset; "+
			"open the page again and re-establish any session state.", reason)
	}
	return fmt.Sprintf("Note: MCP server %q restarted before this call (%s); state from earlier calls was reset.", name, reason)
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
func (c *lazyClient) begin() (d Client, err error) {
	c.dropDeadDelegate()

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
	again := c.started
	c.mu.Unlock()

	if again {
		logging.Info("Starting MCP server again after its previous process ended", "name", c.name)
	} else {
		logging.Info("Lazily starting MCP server on first use", "name", c.name)
	}
	d, err = c.factory()
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
	c.started = true
	if info := d.ServerInfo(); info != nil {
		c.serverInfo = info
	}
	c.inFlight++
	c.stopIdleLocked()
	c.mu.Unlock()
	return d, nil
}

// dropDeadDelegate discards a delegate whose process has exited on its own, so
// the next call starts a new one rather than failing against a closed pipe
// until the idle reaper happens by. The process exit itself is logged by the
// client's exit watcher; this records what the caller lost.
//
// IsConnected is asked with c.mu released: a delegate may hold its own lock
// across a slow re-initialization, and nothing here may block other callers
// behind it.
func (c *lazyClient) dropDeadDelegate() {
	c.mu.Lock()
	d := c.delegate
	busy := c.starting || c.inFlight > 0
	c.mu.Unlock()
	if d == nil || busy || d.IsConnected() {
		return
	}

	c.mu.Lock()
	if c.delegate != d || c.starting || c.inFlight > 0 {
		c.mu.Unlock()
		return
	}
	c.delegate = nil
	c.stopIdleLocked()
	c.noteLostLocked("its process exited")
	c.mu.Unlock()

	logging.Warn("Discarding MCP server whose process exited; the next call starts a new one", "name", c.name)
	_ = d.Close()
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
	c.noteLostLocked(fmt.Sprintf("it was idle for %s and was shut down to free memory", c.idleTimeout))
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

// busy reports whether the client is mid-call or mid-spawn. The manager's LRU
// eviction uses it to skip a client that is actively serving someone, so the
// ceiling never costs a caller its in-flight tool call.
//
// It is safe to call while the manager holds its own lock: nothing here takes
// m.mu, and no lazyClient path holds c.mu across a spawn.
func (c *lazyClient) busy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.starting || c.inFlight > 0
}

// reapNow tears the delegate subprocess down immediately, exactly as the idle
// timer would, and leaves the wrapper usable — a later tool call transparently
// respawns it.
//
// The manager uses it for LRU eviction so an evicted dir client is
// indistinguishable from one the idle reaper took: same teardown, same onReap
// callback, same "the next call just works" behaviour. Close() would be wrong
// here, because a caller that resolved the client a moment earlier would get a
// permanent "client is closed" rather than a respawn.
//
// Must be called with the manager's lock released: it runs onReap, which takes
// it. It is a no-op while a call is in flight, which is the second of the two
// guards against evicting a live client.
func (c *lazyClient) reapNow() {
	c.idleShutdown()
}

// cachedManifest is the tool manifest a probe or a live delegate already
// produced, if any. It never spawns: a status report must not pay a cold
// start (Manager.ProjectToolCount).
func (c *lazyClient) cachedManifest() ([]Tool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.manifest, c.manifestCached
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
//
// When the caller's previous process was lost, the result says so, so an agent
// whose browser restarted learns it from the first answer rather than from a
// puzzling "No page found". The reason stays pending until a result carrying it
// is returned: a failed call — a failed start, or an error from the new process
// — mentions it in its error too, but an error can be lost on its way to the
// model, so the next successful call reports it again.
func (c *lazyClient) CallTool(name string, arguments map[string]interface{}) (*ToolResult, error) {
	d, err := c.begin()
	if err != nil {
		return nil, err
	}
	defer c.end()
	restarted, gen := c.pendingRestart()
	result, err := d.CallTool(name, arguments)
	if restarted == "" {
		return result, err
	}
	note := restartNote(c.name, restarted)
	if err != nil {
		return nil, fmt.Errorf("%w\n%s", err, note)
	}
	if result == nil {
		result = &ToolResult{}
	}
	result.Content = append([]ToolContent{{Type: "text", Text: note}}, result.Content...)
	c.restartDelivered(gen)
	return result, nil
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
