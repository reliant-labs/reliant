// Copyright (c) 2025 Reliant Labs
package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/daemon/configloader"
	"github.com/reliant-labs/reliant/internal/logging"
)

const (
	defaultProjectServerLoadTimeout = 45 * time.Second
	failedServerRetryCooldown       = 2 * time.Minute

	// chromeDevtoolsMCPVersion pins the chrome-devtools-mcp npm package the
	// daemon launches for the built-in browser MCP. Bump deliberately.
	// Docs: https://github.com/ChromeDevTools/chrome-devtools-mcp
	chromeDevtoolsMCPVersion = "1.6.0"

	// chromeDevtoolsServerName is the logical name of the built-in browser MCP.
	chromeDevtoolsServerName = "chrome-devtools"
)

// isLazyStartServer reports whether a server should be started on first tool
// use rather than eagerly at session/daemon startup. Currently limited to the
// built-in chrome-devtools MCP, whose node + headless Chrome subprocess holds a
// large resident set that most sessions never exercise. Kept name-scoped (not a
// config.MCPServer field) so the persisted MCP config schema is unchanged and
// user-defined servers keep their existing eager-start behavior.
func isLazyStartServer(name string, cfg config.MCPServer) bool {
	return name == chromeDevtoolsServerName && cfg.Type == config.MCPStdio
}

// systemChromePaths are on-disk locations a system Chrome/Chromium may live.
// The cloud workspace-base image installs google-chrome-stable and symlinks
// /usr/local/bin/reliant-chrome (control-plane docker/Dockerfile.workspace-base).
// Checked in order; first existing binary wins.
var systemChromePaths = []string{
	"/usr/local/bin/reliant-chrome",
	"/usr/bin/google-chrome-stable",
	"/usr/bin/google-chrome",
	"/usr/bin/chromium",
	"/usr/bin/chromium-browser",
	"/opt/google/chrome/chrome",
}

// detectSystemChrome returns the path to an installed system browser binary,
// or "" if none is present. Used to self-gate the built-in chrome-devtools MCP:
// the daemon advertises a browser tool only on images that actually ship Chrome
// (cloud/amd64), so it never spawns an MCP that would immediately fail on
// browser-less images (e.g. the arm64 local-dev image).
func detectSystemChrome() string {
	for _, p := range systemChromePaths {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// Manager manages multiple MCP server connections and aggregates their capabilities
type Manager struct {
	clients        map[string]Client
	projectServers map[string]map[string]bool // projectPath -> set of server names
	mu             sync.RWMutex

	// serverConfigs records the config each logical server was registered with
	// so a session-scoped server can be spawned again for a new session key
	// without re-resolving where its config came from.
	serverConfigs map[string]config.MCPServer

	// clientFactory constructs the underlying MCP client for a server. A field
	// rather than a direct NewClient call so tests can exercise the manager's
	// client lifecycle — session scoping, eviction — without spawning real
	// subprocesses.
	clientFactory func(name string, cfg config.MCPServer) (Client, error)

	// sessionClients holds per-session private clients for session-scoped
	// servers (see session.go), keyed by sessionClientKey. Deliberately a
	// SEPARATE map from clients: these are the same server spawned again so its
	// process-global state is not shared, and they must never appear in server
	// listings, health reporting, or tool discovery — the model sees one
	// chrome-devtools, not one per thread.
	sessionClients map[string]Client

	// dirClients holds per-project private clients for dir-scoped servers (see
	// dirscope.go), keyed by dirClientKey. Separate from clients for the same
	// reason sessionClients is: these are the same logical server spawned again
	// against a different tree, and the model must still see ONE server.
	//
	// The shared `clients` entry for such a server remains the one bound to
	// whichever project loaded it first; every project resolves through here
	// instead, so no caller silently reads another project's index.
	dirClients map[string]Client

	ctx    context.Context
	cancel context.CancelFunc

	// Optional resolver for provider-backed project config loading.
	// When set, project MCP server autoload prefers this resolver over direct filesystem reads.
	projectConfigResolver func(ctx context.Context, projectPath string) (*config.Config, error)

	// Health monitoring
	healthCheckInterval time.Duration
	healthChecks        map[string]*healthStatus
	healthMu            sync.RWMutex

	// Lazy initialization
	pendingServers map[string]config.MCPServer
	initOnce       sync.Once
	initErr        error

	// Retry backoff for project-server autoloading to avoid repeated noisy failures
	nextRetryAt map[string]time.Time
}

type healthStatus struct {
	lastCheck  time.Time
	healthy    bool
	errorCount int
	lastError  error
}

// NewManager creates a new MCP manager
func NewManager() *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		clients:             make(map[string]Client),
		projectServers:      make(map[string]map[string]bool),
		serverConfigs:       make(map[string]config.MCPServer),
		sessionClients:      make(map[string]Client),
		dirClients:          make(map[string]Client),
		clientFactory:       NewClient,
		healthChecks:        make(map[string]*healthStatus),
		healthCheckInterval: 30 * time.Second,
		ctx:                 ctx,
		cancel:              cancel,
		pendingServers:      make(map[string]config.MCPServer),
		nextRetryAt:         make(map[string]time.Time),
	}
}

// SetPendingServers stores servers for lazy initialization
func (m *Manager) SetPendingServers(servers map[string]config.MCPServer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pendingServers = servers
}

// SetProjectConfigResolver configures provider-backed project config resolution.
// If unset (or if resolver fails), manager falls back to legacy filesystem MCP loading.
func (m *Manager) SetProjectConfigResolver(resolver func(ctx context.Context, projectPath string) (*config.Config, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.projectConfigResolver = resolver
}

func normalizeProjectPath(projectPath string) string {
	trimmed := strings.TrimSpace(projectPath)
	if trimmed == "" {
		return ""
	}
	return filepath.Clean(trimmed)
}

func (m *Manager) isProjectServer(projectPath, serverName string) bool {
	normalizedProjectPath := normalizeProjectPath(projectPath)
	if normalizedProjectPath == "" {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	servers, ok := m.projectServers[normalizedProjectPath]
	return ok && servers[serverName]
}

func (m *Manager) snapshotProjectClients(projectPath string) map[string]Client {
	normalizedProjectPath := normalizeProjectPath(projectPath)
	if normalizedProjectPath == "" {
		return nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	servers, ok := m.projectServers[normalizedProjectPath]
	if !ok {
		return nil
	}

	clients := make(map[string]Client, len(servers))
	for name := range servers {
		if client, exists := m.clients[name]; exists {
			clients[name] = client
		}
	}
	return clients
}

func (m *Manager) trackProjectServer(projectPath, serverName string) {
	normalizedProjectPath := normalizeProjectPath(projectPath)
	m.mu.Lock()
	defer m.mu.Unlock()
	servers := m.projectServers[normalizedProjectPath]
	if servers == nil {
		servers = make(map[string]bool)
		m.projectServers[normalizedProjectPath] = servers
	}
	servers[serverName] = true
}

func (m *Manager) untrackProjectServer(serverName string) {
	// m.mu must be held by caller
	for projectPath, servers := range m.projectServers {
		delete(servers, serverName)
		if len(servers) == 0 {
			delete(m.projectServers, projectPath)
		}
	}
}

// ensureInitialized ensures MCP servers are initialized (called on first use)
func (m *Manager) ensureInitialized() error {
	m.initOnce.Do(func() {
		m.mu.RLock()
		servers := m.pendingServers
		m.mu.RUnlock()

		if len(servers) == 0 {
			return
		}

		m.initErr = m.Initialize(servers)

		// Clear pending servers after initialization attempt
		m.mu.Lock()
		m.pendingServers = nil
		m.mu.Unlock()
	})
	return m.initErr
}

// Initialize initializes all configured MCP servers.
// Uses a default 30-second timeout per server.
func (m *Manager) Initialize(servers map[string]config.MCPServer) error {
	if len(servers) == 0 {
		logging.Info("No MCP servers configured")
		return nil
	}

	logging.Info("Initializing MCP servers", "count", len(servers))

	var initErrors []error
	for name, cfg := range servers {
		// Use a 30-second timeout per server during startup
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := m.AddServer(ctx, name, cfg); err != nil {
			logging.Error("Failed to initialize MCP server", "name", name, "error", err)
			initErrors = append(initErrors, fmt.Errorf("%s: %w", name, err))

			// Track the error in health status so it can be retrieved later
			m.healthMu.Lock()
			m.healthChecks[name] = &healthStatus{
				lastCheck:  time.Now(),
				healthy:    false,
				errorCount: 1,
				lastError:  err,
			}
			m.healthMu.Unlock()
		}
		cancel()
	}

	// Start health monitoring
	go m.monitorHealth()

	if len(initErrors) > 0 {
		return fmt.Errorf("failed to initialize some MCP servers: %v", initErrors)
	}

	return nil
}

// AddServer adds and initializes a new MCP server.
// The context is used for timeout/cancellation during initialization.
func (m *Manager) AddServer(ctx context.Context, name string, cfg config.MCPServer) error {
	logging.Info("Adding MCP server", "name", name, "type", cfg.Type)

	spawn := func() (Client, error) { return m.clientFactory(name, cfg) }

	var (
		client Client
		err    error
	)
	if isLazyStartServer(name, cfg) {
		// Defer spawning heavy subprocesses (chrome-devtools node + headless
		// Chrome) until a browser tool is actually used. Initialize below is a
		// no-op for lazy clients, so the server is registered/healthy without
		// holding the ~345MB resident set idle for the pod's lifetime.
		client = newLazyClientWithFactory(name, cfg, spawn)
	} else {
		client, err = spawn()
	}
	if err != nil {
		return fmt.Errorf("failed to create MCP client: %w", err)
	}

	if err := client.Initialize(ctx); err != nil {
		logging.Error("MCP server initialization failed", "name", name, "raw_error", err.Error())
		classifiedErr := classifyMCPStartupError(name, cfg, err)

		if closeErr := client.Close(); closeErr != nil {
			logging.Warn("Failed to close client after initialization failure", "error", closeErr)
		}

		m.healthMu.Lock()
		m.healthChecks[name] = &healthStatus{
			lastCheck:  time.Now(),
			healthy:    false,
			errorCount: 1,
			lastError:  classifiedErr,
		}
		m.healthMu.Unlock()

		m.mu.Lock()
		m.nextRetryAt[name] = time.Now().Add(failedServerRetryCooldown)
		m.mu.Unlock()

		return fmt.Errorf("failed to initialize MCP client: %w", classifiedErr)
	}

	m.mu.Lock()
	m.clients[name] = client
	m.serverConfigs[name] = cfg
	delete(m.nextRetryAt, name)
	m.mu.Unlock()

	m.healthMu.Lock()
	m.healthChecks[name] = &healthStatus{
		lastCheck: time.Now(),
		healthy:   true,
	}
	m.healthMu.Unlock()

	if info := client.ServerInfo(); info != nil {
		logging.Info("MCP server connected",
			"name", name,
			"server", info.Name,
			"version", info.Version,
			"hasTools", info.Capabilities.Tools != nil,
			"hasResources", info.Capabilities.Resources != nil,
			"hasPrompts", info.Capabilities.Prompts != nil)
	}

	return nil
}

// RemoveServer removes and closes an MCP server connection, along with any
// per-session clients spawned from it.
func (m *Manager) RemoveServer(name string) error {
	m.mu.Lock()
	client, exists := m.clients[name]
	if exists {
		delete(m.clients, name)
	}
	delete(m.serverConfigs, name)
	sessionPrefix := name + sessionKeySeparator
	var sessionClients []Client
	for key, sc := range m.sessionClients {
		if strings.HasPrefix(key, sessionPrefix) {
			sessionClients = append(sessionClients, sc)
			delete(m.sessionClients, key)
		}
	}
	delete(m.nextRetryAt, name)
	m.untrackProjectServer(name)
	m.mu.Unlock()

	for _, sc := range sessionClients {
		if err := sc.Close(); err != nil {
			logging.Warn("Failed to close session-scoped MCP client", "server", name, "error", err)
		}
	}

	if !exists {
		return fmt.Errorf("server %s not found", name)
	}

	// Remove health check
	m.healthMu.Lock()
	delete(m.healthChecks, name)
	m.healthMu.Unlock()

	// Close the client
	return client.Close()
}

// ListAllTools returns tools from all connected MCP servers
func (m *Manager) ListAllTools() (map[string][]Tool, error) {
	// Ensure MCP servers are initialized on first call
	if err := m.ensureInitialized(); err != nil {
		return nil, fmt.Errorf("failed to initialize MCP servers: %w", err)
	}

	m.mu.RLock()
	clients := make(map[string]Client, len(m.clients))
	for k, v := range m.clients {
		clients[k] = v
	}
	m.mu.RUnlock()

	result := make(map[string][]Tool)
	var errors []error

	for name, client := range clients {
		// Check health status
		if !m.isHealthy(name) {
			logging.Warn("Skipping unhealthy MCP server", "name", name)
			continue
		}

		tools, err := client.ListTools()
		if err != nil {
			logging.Error("Failed to list tools from MCP server", "name", name, "error", err)
			errors = append(errors, fmt.Errorf("%s: %w", name, err))
			m.recordError(name, err)
			continue
		}

		if len(tools) > 0 {
			result[name] = tools
			logging.Debug("Listed tools from MCP server", "name", name, "count", len(tools))
		}
	}

	if len(errors) > 0 && len(result) == 0 {
		return nil, fmt.Errorf("failed to list tools from any server: %v", errors)
	}

	return result, nil
}

// sessionClientFor returns the private client for (serverName, session),
// creating it on first use. ok is false when the call should route to the
// shared client: an empty session key, an unregistered server, or a server that
// is not session-scoped (see session.go — nearly all of them).
//
// Session clients are always lazy: the subprocess is spawned by the first tool
// call and reaped after an idle period, at which point the client evicts itself
// from the map so a long-lived daemon does not accumulate one entry per thread
// it has ever served.
func (m *Manager) sessionClientFor(serverName, session string) (Client, bool) {
	session = normalizeSessionKey(session)
	if session == "" {
		return nil, false
	}
	key := sessionClientKey(serverName, session)

	m.mu.RLock()
	client, exists := m.sessionClients[key]
	cfg, registered := m.serverConfigs[serverName]
	m.mu.RUnlock()

	if exists {
		return client, true
	}
	if !registered || !isSessionScopedServer(serverName, cfg) {
		return nil, false
	}

	m.mu.Lock()
	if client, exists := m.sessionClients[key]; exists {
		m.mu.Unlock()
		return client, true
	}
	// The delegate is constructed with the LOGICAL server name so its logs,
	// handshake and ServerInfo read as chrome-devtools, not as the composite key.
	lc := newLazyClientWithFactory(serverName, cfg, func() (Client, error) {
		return m.clientFactory(serverName, cfg)
	})
	lc.onReap = func() { m.evictSessionClient(key, lc) }
	m.sessionClients[key] = lc
	m.mu.Unlock()

	logging.Info("Created session-scoped MCP client",
		"server", serverName,
		"session", session,
		"reason", "server keeps per-caller state that concurrent threads must not share")
	return lc, true
}

// dirClientFor returns the private client for (serverName, projectPath),
// creating it on first use. ok is false when the call should route to the shared
// client: an empty project path, an unregistered server, or a server that is not
// dir-scoped (see dirscope.go — nearly all of them).
//
// Dir clients are always lazy, for the same reason session clients are: a
// language server holds a real index, and a long-lived daemon must not carry one
// per project it has ever served. The reap callback evicts the map entry.
func (m *Manager) dirClientFor(serverName, projectPath string) (Client, bool) {
	projectPath = normalizeProjectPath(projectPath)
	if projectPath == "" {
		return nil, false
	}
	key := dirClientKey(serverName, projectPath)

	m.mu.RLock()
	client, exists := m.dirClients[key]
	cfg, registered := m.serverConfigs[serverName]
	m.mu.RUnlock()

	if exists {
		return client, true
	}
	if !registered || !isDirScopedServer(cfg) {
		return nil, false
	}

	// Bind the config to THIS project before spawning: Dir is what the process
	// starts in, and args carry the same path for servers that take it as a
	// flag. Both are resolved here, once, so the client itself stays generic.
	scoped := cfg
	scoped.Dir = resolveServerDir(cfg, projectPath)

	m.mu.Lock()
	if client, exists := m.dirClients[key]; exists {
		m.mu.Unlock()
		return client, true
	}
	// Constructed with the LOGICAL server name so logs, handshake and ServerInfo
	// read as the server, not as the composite key.
	lc := newLazyClientWithFactory(serverName, scoped, func() (Client, error) {
		return m.clientFactory(serverName, scoped)
	})
	lc.onReap = func() { m.evictDirClient(key, lc) }
	m.dirClients[key] = lc
	m.mu.Unlock()

	logging.Info("Created dir-scoped MCP client",
		"server", serverName,
		"projectPath", projectPath,
		"dir", scoped.Dir,
		"reason", "server answers are scoped to the tree it was started in; projects must not share one process")
	return lc, true
}

// evictDirClient drops a dir client once its subprocess has been reaped for
// inactivity. The pointer check keeps a late callback from a superseded client
// from removing its replacement.
func (m *Manager) evictDirClient(key string, client Client) {
	m.mu.Lock()
	if current, ok := m.dirClients[key]; ok && current == client {
		delete(m.dirClients, key)
	}
	m.mu.Unlock()
}

// evictSessionClient drops a session client once its subprocess has been reaped
// for inactivity. The pointer check keeps a late callback from a superseded
// client from removing its replacement.
func (m *Manager) evictSessionClient(key string, client Client) {
	m.mu.Lock()
	if current, ok := m.sessionClients[key]; ok && current == client {
		delete(m.sessionClients, key)
	}
	m.mu.Unlock()
}

// CallTool calls a tool on a specific MCP server.
//
// session isolates callers that must not share a server's process-global state
// — see session.go. Empty means the shared client, which is what every server
// except the session-scoped ones uses regardless of what is passed.
func (m *Manager) CallTool(session, serverName, toolName string, arguments map[string]interface{}) (*ToolResult, error) {
	// Ensure MCP servers are initialized on first call
	if err := m.ensureInitialized(); err != nil {
		return nil, fmt.Errorf("failed to initialize MCP servers: %w", err)
	}

	if sessionClient, ok := m.sessionClientFor(serverName, session); ok {
		// Session clients are private and lazily (re)spawned, so the shared
		// server's health gate does not describe them; a failure surfaces as the
		// call's own error.
		return sessionClient.CallTool(toolName, arguments)
	}

	m.mu.RLock()
	client, exists := m.clients[serverName]
	m.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("MCP server %s not found", serverName)
	}

	// Check health status
	if !m.isHealthy(serverName) {
		return nil, fmt.Errorf("MCP server %s is unhealthy", serverName)
	}

	result, err := client.CallTool(toolName, arguments)
	if err != nil {
		m.recordError(serverName, err)
		return nil, err
	}

	// Record successful call
	m.recordSuccess(serverName)

	return result, nil
}

// GetClient returns a specific MCP client
func (m *Manager) GetClient(name string) (Client, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	client, exists := m.clients[name]
	return client, exists
}

// GetAllClients returns all MCP clients
func (m *Manager) GetAllClients() map[string]Client {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]Client, len(m.clients))
	for k, v := range m.clients {
		result[k] = v
	}
	return result
}

// Close shuts down all MCP servers
func (m *Manager) Close() error {
	logging.Info("Shutting down MCP manager")

	// Cancel context to stop health monitoring first
	m.cancel()

	// Get all clients, including every session's private client.
	m.mu.Lock()
	clients := m.clients
	for key, client := range m.sessionClients {
		clients[key] = client
	}
	m.clients = make(map[string]Client)
	m.sessionClients = make(map[string]Client)
	m.projectServers = make(map[string]map[string]bool)
	m.mu.Unlock()

	// Close all clients in parallel for faster shutdown
	var wg sync.WaitGroup
	var errorsMu sync.Mutex
	var errors []error

	for name, client := range clients {
		wg.Add(1)
		go func(n string, c Client) {
			defer wg.Done()
			logging.Info("Closing MCP client", "name", n)
			if err := c.Close(); err != nil {
				logging.Error("Failed to close MCP client", "name", n, "error", err)
				errorsMu.Lock()
				errors = append(errors, fmt.Errorf("%s: %w", n, err))
				errorsMu.Unlock()
			} else {
				logging.Info("MCP client closed successfully", "name", n)
			}
		}(name, client)
	}

	// Wait for all clients to close with a timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logging.Info("All MCP clients closed")
	case <-time.After(10 * time.Second):
		logging.Warn("Timeout waiting for MCP clients to close")
	}

	if len(errors) > 0 {
		return fmt.Errorf("errors closing MCP clients: %v", errors)
	}

	return nil
}

// Health monitoring

func (m *Manager) monitorHealth() {
	ticker := time.NewTicker(m.healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.checkAllHealth()
		}
	}
}

func (m *Manager) checkAllHealth() {
	m.mu.RLock()
	clients := make(map[string]Client, len(m.clients))
	for k, v := range m.clients {
		clients[k] = v
	}
	m.mu.RUnlock()

	for name, client := range clients {
		m.checkHealth(name, client)
	}
}

func (m *Manager) checkHealth(name string, client Client) {
	// Simple health check: verify connection is alive
	healthy := client.IsConnected()

	m.healthMu.Lock()
	defer m.healthMu.Unlock()

	status, exists := m.healthChecks[name]
	if !exists {
		status = &healthStatus{}
		m.healthChecks[name] = status
	}

	status.lastCheck = time.Now()
	status.healthy = healthy

	if !healthy {
		status.errorCount++
		status.lastError = fmt.Errorf("connection lost")

		// Attempt to reconnect if unhealthy
		go m.attemptReconnect(name, client)
	} else {
		status.errorCount = 0
		status.lastError = nil
	}
}

func (m *Manager) attemptReconnect(name string, client Client) {
	logging.Info("Attempting to reconnect MCP server", "name", name)

	// Try to reinitialize with a 30-second timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := client.Initialize(ctx)
	if err != nil {
		classifiedErr := classifyMCPRuntimeError(name, err)
		logging.Error("Failed to reconnect MCP server", "name", name, "error", err, "classified_error", classifiedErr)
		m.healthMu.Lock()
		if status, exists := m.healthChecks[name]; exists {
			status.healthy = false
			status.errorCount++
			status.lastError = classifiedErr
		}
		m.healthMu.Unlock()
		return
	}

	// Update health status
	m.healthMu.Lock()
	if status, exists := m.healthChecks[name]; exists {
		status.healthy = true
		status.errorCount = 0
		status.lastError = nil
	}
	m.healthMu.Unlock()

	logging.Info("Successfully reconnected MCP server", "name", name)
}

func (m *Manager) isHealthy(name string) bool {
	m.healthMu.RLock()
	defer m.healthMu.RUnlock()

	status, exists := m.healthChecks[name]
	if !exists {
		return false
	}

	return status.healthy
}

func (m *Manager) recordError(name string, err error) {
	m.healthMu.Lock()
	defer m.healthMu.Unlock()

	classifiedErr := classifyMCPRuntimeError(name, err)

	if status, exists := m.healthChecks[name]; exists {
		status.errorCount++
		status.lastError = classifiedErr

		// Mark as unhealthy after too many errors
		if status.errorCount >= 3 {
			status.healthy = false
		}
	}
}

func classifyMCPStartupError(name string, cfg config.MCPServer, err error) error {
	if err == nil {
		return nil
	}

	lower := strings.ToLower(err.Error())
	base := classifyMCPErrorMessage(name, lower)

	if strings.Contains(lower, "calling \"initialize\": eof") || strings.HasSuffix(lower, ": eof") {
		base = fmt.Sprintf("MCP server %q exited before completing the handshake. %s", name, startupHintForServer(name, cfg))
	}

	return fmt.Errorf("%s", base)
}

func startupHintForServer(name string, cfg config.MCPServer) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "docker":
		return "Ensure Docker Desktop/daemon is running and DOCKER_HOST points to a valid unix socket."
	case "sqlite":
		return "Ensure SQLITE_DB_PATH is an absolute path to an existing .db file and that Reliant can read it."
	case "serena", "fetch":
		return "This is commonly caused by runtime/package startup failure (uvx install/cache/certificate issues). Check backend logs for concrete stderr output."
	default:
		if cfg.Command == "uvx" {
			return "This is commonly caused by uvx runtime/package startup failures. Check backend logs for concrete stderr output."
		}
		if cfg.Command == "npx" {
			return "This is commonly caused by node package startup failures. Check backend logs for concrete stderr output."
		}
		return "Check backend logs for concrete startup stderr output and verify required configuration values."
	}
}

func classifyMCPRuntimeError(name string, err error) error {
	if err == nil {
		return nil
	}

	lower := strings.ToLower(err.Error())
	base := classifyMCPErrorMessage(name, lower)
	return fmt.Errorf("%s", base)
}

func classifyMCPErrorMessage(name, lowerError string) string {
	switch {
	case strings.Contains(lowerError, "calling \"initialize\": eof") || strings.HasSuffix(lowerError, ": eof"):
		return fmt.Sprintf("MCP server %q exited before completing the handshake. Check required environment variables and server package/runtime health.", name)
	case strings.Contains(lowerError, "context deadline exceeded"):
		return fmt.Sprintf("MCP server %q startup timed out before handshake. First-run package downloads or blocked network access may be the cause.", name)
	case strings.Contains(lowerError, "unknownissuer") || strings.Contains(lowerError, "certificate"):
		return fmt.Sprintf("MCP server %q failed TLS certificate validation. Configure system CA trust or proxy certificates for this runtime.", name)
	case strings.Contains(lowerError, "executable file not found") || strings.Contains(lowerError, "not found on path"):
		return fmt.Sprintf("MCP server %q command is not available on PATH. Install the runtime/tool and retry.", name)
	case strings.Contains(lowerError, "docker socket"):
		return fmt.Sprintf("MCP server %q cannot reach Docker socket. Set DOCKER_HOST to a valid unix socket and retry.", name)
	case strings.Contains(lowerError, "err_module_not_found") || strings.Contains(lowerError, "cannot find module") || strings.Contains(lowerError, "module not found"):
		return fmt.Sprintf("MCP server %q failed due to missing Node module dependency in the upstream package. Reinstall/update the package and retry.", name)
	default:
		return fmt.Sprintf("MCP server %q failed to start. Check server logs and configuration, then retry.", name)
	}
}

func (m *Manager) recordSuccess(name string) {
	m.healthMu.Lock()
	defer m.healthMu.Unlock()

	if status, exists := m.healthChecks[name]; exists {
		status.errorCount = 0
		status.lastError = nil
		status.healthy = true
	}
}

// GetHealthStatus returns the health status of all MCP servers
func (m *Manager) GetHealthStatus() map[string]bool {
	m.healthMu.RLock()
	defer m.healthMu.RUnlock()

	result := make(map[string]bool)
	for name, status := range m.healthChecks {
		result[name] = status.healthy
	}
	return result
}

// GetLastError returns the last error for a specific server
func (m *Manager) GetLastError(name string) error {
	m.healthMu.RLock()
	defer m.healthMu.RUnlock()

	if status, exists := m.healthChecks[name]; exists {
		return status.lastError
	}
	return nil
}

// ProjectServerLoadResult contains the result of loading project MCP servers.
type ProjectServerLoadResult struct {
	// LoadedServers contains the names of servers that were successfully loaded.
	LoadedServers []string
	// FailedServers contains the names of servers that failed to load.
	FailedServers []string
	// Errors contains the errors for failed servers, keyed by server name.
	Errors map[string]error
}

// HasFailures returns true if any servers failed to load.
func (r *ProjectServerLoadResult) HasFailures() bool {
	return len(r.FailedServers) > 0
}

// EnsureProjectServersLoaded loads MCP servers from project config if they aren't already running.
// This is safe to call multiple times.
//
// The context is used for timeout/cancellation. Each server gets up to defaultProjectServerLoadTimeout
// to initialize. If context is cancelled or times out, remaining servers are skipped.
//
// Returns a result struct with information about loaded and failed servers for graceful degradation.
func (m *Manager) EnsureProjectServersLoaded(ctx context.Context, projectPath string) *ProjectServerLoadResult {
	result := &ProjectServerLoadResult{
		LoadedServers: []string{},
		FailedServers: []string{},
		Errors:        make(map[string]error),
	}

	normalizedProjectPath := normalizeProjectPath(projectPath)
	if normalizedProjectPath == "" {
		return result
	}

	// Prefer provider-backed config resolution when available.
	servers := m.loadProjectServersFromConfig(ctx, normalizedProjectPath)
	if len(servers) == 0 {
		return result
	}

	logging.Info("Loading MCP servers from project", "path", normalizedProjectPath, "count", len(servers))

	// Get currently running servers
	m.mu.RLock()
	runningServers := make(map[string]bool, len(m.clients))
	for name := range m.clients {
		runningServers[name] = true
	}
	m.mu.RUnlock()

	// Load any servers from config that aren't already running
	for name, serverCfg := range servers {
		if !serverCfg.Enabled {
			logging.Debug("Skipping disabled MCP server from project config", "name", name)
			if runningServers[name] {
				if err := m.RemoveServer(name); err != nil && !strings.Contains(err.Error(), "not found") {
					logging.Warn("Failed to stop disabled MCP server", "name", name, "error", err)
				}
			}
			continue
		}

		// Check if parent context is cancelled
		select {
		case <-ctx.Done():
			logging.Warn("Context cancelled, skipping remaining MCP servers", "remaining", name)
			result.FailedServers = append(result.FailedServers, name)
			result.Errors[name] = ctx.Err()
			continue
		default:
		}

		if runningServers[name] {
			m.trackProjectServer(normalizedProjectPath, name)
			logging.Debug("MCP server already running; associated with requested project scope",
				"name", name,
				"projectPath", normalizedProjectPath)
			result.LoadedServers = append(result.LoadedServers, name)
			continue
		}

		if !m.shouldRetryServerLoad(name) {
			logging.Debug("Skipping MCP server autoload due to retry cooldown", "name", name)
			continue
		}

		// Give first-time package downloads enough time (npx/uvx can be slow)
		serverCtx, cancel := context.WithTimeout(ctx, defaultProjectServerLoadTimeout)

		logging.Info("Loading MCP server from project config", "name", name, "command", serverCfg.Command)
		if err := m.AddServer(serverCtx, name, serverCfg); err != nil {
			logging.Warn("Failed to load MCP server (will continue without it)", "name", name, "error", err)
			result.FailedServers = append(result.FailedServers, name)
			result.Errors[name] = err
		} else {
			m.trackProjectServer(normalizedProjectPath, name)
			result.LoadedServers = append(result.LoadedServers, name)
		}
		cancel()
	}

	return result
}

func (m *Manager) shouldRetryServerLoad(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	next, exists := m.nextRetryAt[name]
	if !exists {
		return true
	}
	return time.Now().After(next)
}

// GetProjectClients returns running clients for a specific project keyed by logical server name.
func (m *Manager) GetProjectClients(projectPath string) map[string]Client {
	return m.snapshotProjectClients(projectPath)
}

// GetProjectHealthStatus returns health status for running project-scoped servers.
func (m *Manager) GetProjectHealthStatus(projectPath string) map[string]bool {
	clients := m.snapshotProjectClients(projectPath)
	status := make(map[string]bool, len(clients))
	for name := range clients {
		status[name] = m.isHealthy(name)
	}
	return status
}

// GetProjectLastError returns last startup/runtime error for a project-scoped server.
func (m *Manager) GetProjectLastError(projectPath, serverName string) error {
	return m.GetLastError(serverName)
}

// GetProjectClient returns a project-scoped client by logical server name.
func (m *Manager) GetProjectClient(projectPath, serverName string) (Client, bool) {
	if !m.isProjectServer(projectPath, serverName) {
		return nil, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	client, exists := m.clients[serverName]
	return client, exists
}

// AddProjectServer adds and initializes a server for a specific project scope.
func (m *Manager) AddProjectServer(ctx context.Context, projectPath, serverName string, cfg config.MCPServer) error {
	normalizedProjectPath := normalizeProjectPath(projectPath)
	if normalizedProjectPath == "" {
		return fmt.Errorf("project path is required")
	}

	if err := m.RemoveProjectServer(normalizedProjectPath, serverName); err != nil && !strings.Contains(err.Error(), "not found") {
		return err
	}

	if err := m.AddServer(ctx, serverName, cfg); err != nil {
		return err
	}

	m.trackProjectServer(normalizedProjectPath, serverName)
	return nil
}

// RemoveProjectServer removes and closes a project-scoped server connection.
func (m *Manager) RemoveProjectServer(projectPath, serverName string) error {
	if !m.isProjectServer(projectPath, serverName) {
		return fmt.Errorf("server %s not found", serverName)
	}
	return m.RemoveServer(serverName)
}

// RestartProjectServer restarts a project-scoped server with updated config.
func (m *Manager) RestartProjectServer(ctx context.Context, projectPath, serverName string, cfg config.MCPServer) error {
	if err := m.RemoveProjectServer(projectPath, serverName); err != nil && !strings.Contains(err.Error(), "not found") {
		return err
	}
	if !cfg.Enabled {
		return nil
	}
	return m.AddProjectServer(ctx, projectPath, serverName, cfg)
}

// ProjectCallTool calls a tool on a project-scoped server.
//
// For a DIR-SCOPED server this routes to that project's own client, because the
// server's answers are scoped to the tree it was started in and the shared
// client belongs to whichever project loaded it first. Every other server
// resolves exactly as CallTool does — projectPath is then a no-op, which is what
// it has always been.
func (m *Manager) ProjectCallTool(session, projectPath, serverName, toolName string, arguments map[string]interface{}) (*ToolResult, error) {
	if err := m.ensureInitialized(); err != nil {
		return nil, fmt.Errorf("failed to initialize MCP servers: %w", err)
	}
	if dirClient, ok := m.dirClientFor(serverName, projectPath); ok {
		// Private and lazily (re)spawned, so the shared server's health gate does
		// not describe it; a failure surfaces as the call's own error.
		return dirClient.CallTool(toolName, arguments)
	}
	return m.CallTool(session, serverName, toolName, arguments)
}

// ListProjectTools returns tools from all connected project-scoped MCP servers.
func (m *Manager) ListProjectTools(projectPath string) (map[string][]Tool, error) {
	if err := m.ensureInitialized(); err != nil {
		return nil, fmt.Errorf("failed to initialize MCP servers: %w", err)
	}

	clients := m.snapshotProjectClients(projectPath)
	result := make(map[string][]Tool)
	var errors []error

	for name, client := range clients {
		if !m.isHealthy(name) {
			logging.Warn("Skipping unhealthy MCP server", "name", name)
			continue
		}

		tools, err := client.ListTools()
		if err != nil {
			logging.Error("Failed to list tools from MCP server", "name", name, "error", err)
			errors = append(errors, fmt.Errorf("%s: %w", name, err))
			m.recordError(name, err)
			continue
		}

		if len(tools) > 0 {
			result[name] = tools
			logging.Debug("Listed tools from MCP server", "name", name, "count", len(tools))
		}
	}

	if len(errors) > 0 && len(result) == 0 {
		return nil, fmt.Errorf("failed to list tools from any server: %v", errors)
	}

	return result, nil
}

// builtinMCPServers returns MCP servers that ship with Reliant and are always available.
// These have the lowest priority and can be overridden by user/project config.
func builtinMCPServers() map[string]config.MCPServer {
	servers := map[string]config.MCPServer{
		"reliant-docs": {
			Type:    config.MCPHTTP,
			URL:     "https://reliantlabs.mintlify.app/mcp",
			Enabled: true,
		},
	}

	// chrome-devtools browser MCP.
	//
	// Two ways it gets a browser, in priority order:
	//
	//  1. Remote CDP (RELIANT_CHROME_BROWSER_URL set) — connect to an
	//     already-running browser over the DevTools Protocol instead of
	//     launching one locally. This is the "drive the laptop's Chrome" escape
	//     hatch for the arm64 local-dev daemon: set it to (e.g.)
	//     http://host.k3d.internal:9222 and run Chrome on the host with
	//     --remote-debugging-port=9222 --remote-debugging-address=0.0.0.0
	//     --remote-allow-origins=*. chrome-devtools-mcp needs NO local Chrome
	//     binary in this mode, so it deliberately BYPASSES the detectSystemChrome
	//     gate. Unset in cloud/prod → identical behavior to before.
	//
	//  2. Local browser on disk (detectSystemChrome) — cloud/amd64 daemons ship
	//     google-chrome-stable; the arm64 local-dev image ships chromium (xtradeb,
	//     control-plane docker/Dockerfile.workspace-base). Self-gating keeps
	//     browser-less images from spawning an MCP that would immediately fail.
	//     We point at the system binary via --executablePath so it never
	//     downloads its own Chrome, and run --headless (no display in the pod)
	//     --isolated (throwaway profile/session).
	if browserURL := strings.TrimSpace(os.Getenv("RELIANT_CHROME_BROWSER_URL")); browserURL != "" {
		servers[chromeDevtoolsServerName] = config.MCPServer{
			Type:    config.MCPStdio,
			Command: "npx",
			Args: []string{
				"-y",
				"chrome-devtools-mcp@" + chromeDevtoolsMCPVersion,
				"--isolated",
				"--browserUrl=" + browserURL,
			},
			Enabled: true,
		}
	} else if chromePath := detectSystemChrome(); chromePath != "" {
		args := []string{
			"-y",
			"chrome-devtools-mcp@" + chromeDevtoolsMCPVersion,
			"--headless",
			"--isolated",
			"--executablePath=" + chromePath,
		}
		// Sandbox policy differs by pod shape. The amd64 cloud daemon runs on the
		// privileged docker-tier (Kata) with a setuid chrome-sandbox, so Chrome
		// keeps its sandbox. The arm64 local-dev daemon pod drops ALL caps and
		// forbids privilege escalation (workspace_controller securityContext), so
		// neither the setuid nor the user-namespace sandbox can initialize and
		// chromium fails to launch without --no-sandbox. Gate on arch: non-amd64
		// == the dev pod shape. --disable-dev-shm-usage avoids crashes on the
		// pod's small /dev/shm.
		if runtime.GOARCH != "amd64" {
			args = append(args,
				"--chromeArg=--no-sandbox",
				"--chromeArg=--disable-setuid-sandbox",
				"--chromeArg=--disable-dev-shm-usage",
			)
		}
		servers[chromeDevtoolsServerName] = config.MCPServer{
			Type:    config.MCPStdio,
			Command: "npx",
			Args:    args,
			Enabled: true,
		}
	}

	return servers
}

func (m *Manager) loadProjectServersFromConfig(ctx context.Context, projectPath string) map[string]config.MCPServer {
	m.mu.RLock()
	resolver := m.projectConfigResolver
	m.mu.RUnlock()

	var userServers map[string]config.MCPServer

	if resolver == nil {
		// No resolver configured — use filesystem directly.
		// This path is used by the daemon runtime where filesystem access is expected.
		userServers = configloader.LoadMCPServersFromProjectScopes(projectPath)
		if len(userServers) == 0 {
			logging.Warn("Project config resolver is not configured; skipping MCP project autoload", "projectPath", projectPath)
		}
	} else {
		cfg, err := resolver(ctx, projectPath)
		if err != nil {
			logging.Warn("Failed provider-backed project config resolution; skipping MCP project autoload",
				"projectPath", projectPath,
				"error", err)
			// Fall back to filesystem-based config when the resolver fails.
			userServers = configloader.LoadMCPServersFromProjectScopes(projectPath)
		} else if cfg != nil && len(cfg.MCPServers) > 0 {
			userServers = cfg.MCPServers
			// Overlay filesystem local-scope (worktree) configs on top of resolved config.
			// This allows per-worktree overrides even when using a provider-backed resolver.
			localServers := configloader.LoadMCPServersFromProjectScopes(projectPath)
			for name, server := range localServers {
				userServers[name] = server
			}
		}
	}

	// Start with built-in servers (lowest priority), then layer user config on top.
	builtin := builtinMCPServers()
	if len(userServers) == 0 {
		return builtin
	}

	merged := make(map[string]config.MCPServer, len(builtin)+len(userServers))
	for name, server := range builtin {
		merged[name] = server
	}
	for name, server := range userServers {
		merged[name] = server
	}
	return merged
}
