// Copyright (c) 2025 Reliant Labs
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/daemon/configloader"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/logging"
	"golang.org/x/sync/singleflight"
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

	// maxDirScopedClients is the ceiling on how many per-project processes all
	// dir-scoped servers may hold AT ONCE, across every server and project.
	//
	// Dir scoping deliberately spawns one process per project, and until this
	// constant existed nothing bounded that count — the idle reaper
	// (lazyIdleTimeout) reclaims a process after five minutes of quiet, but a
	// session that keeps touching projects never gets there. Measured on the
	// incident that motivated it: ONE Go language server held 28,467 open file
	// descriptors and 1.9 GB resident, and macOS's system-wide file table is
	// 491,520 entries — so ~17 concurrent instances exhaust it for every
	// process on the machine, which is exactly what happened.
	//
	// Four caps that worst case near 114k descriptors (~23% of the table) with
	// room left for Docker, the desktop app and Spotlight. It is a bound on
	// SIMULTANEOUSLY ACTIVE projects, not on how many a session may visit: the
	// fifth project evicts the least recently used one, whose next tool call
	// transparently respawns it. A human driving more than four projects inside
	// one five-minute window is rare; a runaway indexer taking down the machine
	// is not recoverable.
	//
	// The cap bounds the MULTIPLIER only. A single process being 1.9 GB is the
	// other half of the same incident, and that is what RequiresFiles (a server
	// never starts where it has nothing to index) addresses.
	maxDirScopedClients = 4

	// maxSessionRestartNotes bounds Manager.sessionRestarts: a note waits for a
	// session's next call, and a session that never calls again must not hold
	// one for the daemon's lifetime.
	maxSessionRestartNotes = 1024
)

// isLazyStartServer reports whether a server should be started on first tool
// use rather than eagerly at session/daemon startup.
//
// Two kinds of server qualify, both for the same reason — a heavyweight
// subprocess that most sessions never exercise:
//
//   - the built-in chrome-devtools MCP, whose node + headless Chrome holds a
//     large resident set;
//   - any server that declares dirScoped, which is by definition a tree
//     indexer. Its per-project clients (dirClientFor) were always lazy; this
//     covers the SHARED client that AddServer registers, which for a dir-scoped
//     server is the one entry no tool call ever routes to — every call resolves
//     through the project's own client. All the shared entry is needed for is
//     tool discovery, and lazyClient serves that from a throwaway
//     spawn→tools/list→exit probe with no resident process. Before this, opening
//     a project eagerly started an indexer in whatever directory the daemon
//     happened to be in, and left it resident for the daemon's lifetime.
//
// The dir-scoped arm is driven by CONFIG rather than a server name, matching
// where dirScoped itself is declared; chrome-devtools stays name-scoped so
// user-defined servers keep their existing eager-start behavior.
func isLazyStartServer(name string, cfg config.MCPServer) bool {
	if cfg.Type != config.MCPStdio {
		return false
	}
	return name == chromeDevtoolsServerName || isDirScopedServer(cfg)
}

// systemChromePaths are on-disk locations a system Chrome/Chromium may live.
// The cloud workspace image installs google-chrome-stable and symlinks
// /opt/reliant/bin/reliant-chrome (control-plane
// docker/Dockerfile.workspace-runtime). /usr/local belongs to the user on a
// managed daemon, so image-owned tools live under /opt/reliant. Images built
// before that move are still covered by the distro paths below.
// Checked in order; first existing binary wins.
var systemChromePaths = []string{
	"/opt/reliant/bin/reliant-chrome",
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
	// chromeBrowserURL, when set, is the DevTools endpoint of an
	// already-running browser the built-in chrome-devtools MCP drives
	// (WithChromeBrowserURL).
	chromeBrowserURL string

	// lookupEnv resolves the ${VAR} references in a server config when its
	// client launches (WithEnvLookup, expand.go). nil resolves nothing, which
	// is the default for every role: the process environment is read only when
	// the daemon passes it explicitly.
	lookupEnv func(string) (string, bool)

	// role is fixed at construction. A Server-role manager can never hold a
	// stdio server and never reads the local filesystem for config.
	role Role

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

	// sessionRestarts remembers, per session key, why a session's private
	// client was dropped after it had run (the server was removed, or the
	// client was reaped idle), until that session's next client takes it. The
	// replacement is a new process with none of the old one's pages or
	// storage, and its first tool result says so (lazyClient.restartReason).
	// Bounded by maxSessionRestartNotes.
	sessionRestarts map[string]string

	// dirClients holds per-project private clients for dir-scoped servers (see
	// dirscope.go), keyed by dirClientKey. Separate from clients for the same
	// reason sessionClients is: these are the same logical server spawned again
	// against a different tree, and the model must still see ONE server.
	//
	// The shared `clients` entry for such a server remains the one bound to
	// whichever project loaded it first; every project resolves through here
	// instead, so no caller silently reads another project's index.
	//
	// Bounded by maxDirScopedClients: one process per project is correct, but
	// unbounded processes per daemon is how a session exhausts the machine's
	// file table. Entries leave by two doors that look the same from outside —
	// the idle reaper, and LRU eviction at the ceiling.
	dirClients map[string]Client

	// dirClientLastUse records when each dirClients entry was last resolved for
	// a tool call, which is what LRU eviction orders by.
	dirClientLastUse map[string]time.Time

	// preconditions caches, per (server, project), whether the project contains
	// the files the server declared it needs (config.MCPServer.RequiresFiles).
	// Verdicts expire after preconditionTTL and are dropped outright when the
	// project is reloaded — see precondition.go for why neither alone is enough.
	preconditions map[string]preconditionVerdict

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

	// failedConfigs is the config each server in nextRetryAt failed to start
	// with: a different config is a fix, retried without the cooldown
	// (shouldRetryServerLoad).
	failedConfigs map[string]config.MCPServer

	// toolListFlights dedupes concurrent first listings of one dir client
	// (listDirClientTools), so a slow start is probed once.
	toolListFlights singleflight.Group

	// reloadLocks serializes, per logical server, deciding whether a project
	// load must start or replace it and doing so (loadProjectServer).
	reloadLocks map[string]*reloadMutex

	// projectConfigs is the config each project last loaded each server
	// under, keyed by dirClientKey(server, project): how a project's edit to
	// a running server's config is told apart from another project declaring
	// the same name differently (projectConfigEdited).
	projectConfigs map[string]config.MCPServer
}

type healthStatus struct {
	lastCheck  time.Time
	healthy    bool
	errorCount int
	lastError  error
}

// Role is the kind of process hosting a Manager.
type Role string

const (
	// RoleDaemon runs on the user's machine: every transport, filesystem
	// config, built-in stdio servers.
	RoleDaemon Role = "daemon"
	// RoleServer runs in a server process (api-server / worker). It must never
	// spawn a subprocess, so stdio servers are refused.
	RoleServer Role = "server"
)

// ErrStdioOnServer is returned when a server-role manager is asked to run a
// stdio MCP server, which would spawn a process on the server instead of the
// user's daemon.
var ErrStdioOnServer = errors.New("stdio MCP servers can only run on a daemon, not on the server")

func isStdioConfig(cfg config.MCPServer) bool {
	return cfg.Type == config.MCPStdio || cfg.Command != ""
}

// NewManager creates a new MCP manager for the given host role.
// ManagerOption configures a Manager at construction.
type ManagerOption func(*Manager)

// WithChromeBrowserURL points the built-in chrome-devtools MCP at an
// already-running browser over the DevTools Protocol instead of a local
// Chrome: the daemon's RELIANT_CHROME_BROWSER_URL (see builtinMCPServers).
func WithChromeBrowserURL(url string) ManagerOption {
	return func(m *Manager) { m.chromeBrowserURL = url }
}

// WithEnvLookup sets where the ${VAR} references in a server config
// (`Authorization: Bearer ${GITHUB_TOKEN}`, url, args, env) are resolved when
// its client launches. The daemon passes os.LookupEnv: it runs in the user's
// environment. A server process must never pass its own environment — that
// would substitute Reliant's credentials into a tenant's request. Without this
// option nothing is resolved: a reference takes its ${VAR:-default}, or the
// empty string.
func WithEnvLookup(lookup func(name string) (string, bool)) ManagerOption {
	return func(m *Manager) { m.lookupEnv = lookup }
}

func NewManager(role Role, opts ...ManagerOption) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		role:                role,
		clients:             make(map[string]Client),
		projectServers:      make(map[string]map[string]bool),
		serverConfigs:       make(map[string]config.MCPServer),
		sessionClients:      make(map[string]Client),
		sessionRestarts:     make(map[string]string),
		dirClients:          make(map[string]Client),
		dirClientLastUse:    make(map[string]time.Time),
		preconditions:       make(map[string]preconditionVerdict),
		healthChecks:        make(map[string]*healthStatus),
		healthCheckInterval: 30 * time.Second,
		ctx:                 ctx,
		cancel:              cancel,
		pendingServers:      make(map[string]config.MCPServer),
		nextRetryAt:         make(map[string]time.Time),
		failedConfigs:       make(map[string]config.MCPServer),
		projectConfigs:      make(map[string]config.MCPServer),
		reloadLocks:         make(map[string]*reloadMutex),
	}
	for _, opt := range opts {
		opt(m)
	}
	m.clientFactory = m.newClient
	return m
}

// newClient is the default clientFactory. Every path that constructs a client
// (AddServer, session- and dir-scoped clients) reaches it, so it re-checks the
// role: AddServer already refuses a stdio server off the daemon, and a stdio
// client inherits this process's environment (client.go), which only the
// daemon's may be.
func (m *Manager) newClient(name string, cfg config.MCPServer) (Client, error) {
	if m.role != RoleDaemon {
		if isStdioConfig(cfg) {
			return nil, fmt.Errorf("MCP server %q: %w", name, ErrStdioOnServer)
		}
		return newClientWithEnvLookup(name, cfg, nil)
	}
	return newClientWithEnvLookup(name, cfg, m.lookupEnv)
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
	if m.role != RoleDaemon && isStdioConfig(cfg) {
		return fmt.Errorf("MCP server %q: %w", name, ErrStdioOnServer)
	}
	logging.InfoContext(ctx, "Adding MCP server", "name", name, "type", cfg.Type)

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
		logging.ErrorContext(ctx, "MCP server initialization failed", "name", name, "raw_error", err.Error())
		classifiedErr := classifyMCPStartupError(name, cfg, err)

		if closeErr := client.Close(); closeErr != nil {
			logging.WarnContext(ctx, "Failed to close client after initialization failure", "error", closeErr)
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
		m.failedConfigs[name] = cfg
		m.mu.Unlock()

		return fmt.Errorf("failed to initialize MCP client: %w", classifiedErr)
	}

	m.mu.Lock()
	replaced := m.clients[name]
	m.clients[name] = client
	m.serverConfigs[name] = cfg
	delete(m.nextRetryAt, name)
	delete(m.failedConfigs, name)
	m.mu.Unlock()

	// A second registration under a live name used to overwrite the map entry
	// and leak the first client's process, silently. Close it, and say so.
	if replaced != nil && replaced != client {
		logging.WarnContext(ctx, "Replacing a running MCP server that was registered again without being removed",
			"name", name, "type", cfg.Type)
		if err := replaced.Close(); err != nil {
			logging.WarnContext(ctx, "Failed to close the replaced MCP server", "name", name, "error", err)
		}
	}

	m.healthMu.Lock()
	m.healthChecks[name] = &healthStatus{
		lastCheck: time.Now(),
		healthy:   true,
	}
	m.healthMu.Unlock()

	if info := client.ServerInfo(); info != nil {
		logging.InfoContext(ctx, "MCP server connected",
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
// per-session and per-project clients spawned from it.
//
// reason says who asked and why ("disabled in project X's MCP config",
// "manage_server remove", …). It is logged with every removal — a removal used
// to leave no trace, so a browser that vanished mid-session could not be told
// apart from one whose process died (#2818) — and each session that loses a
// running client is told, on its next call, that its server restarted.
func (m *Manager) RemoveServer(name, reason string) error {
	m.mu.Lock()
	client, exists := m.clients[name]
	if exists {
		delete(m.clients, name)
	}
	delete(m.serverConfigs, name)
	var projects []string
	for projectPath, servers := range m.projectServers {
		if servers[name] {
			projects = append(projects, projectPath)
		}
	}
	sessionPrefix := name + sessionKeySeparator
	var sessionClients []Client
	for key, sc := range m.sessionClients {
		if strings.HasPrefix(key, sessionPrefix) {
			sessionClients = append(sessionClients, sc)
			delete(m.sessionClients, key)
			if lc, ok := sc.(*lazyClient); ok && lc.hasStarted() {
				m.rememberSessionRestartLocked(key, "the MCP server was removed: "+reason)
			}
		}
	}
	// A dir-scoped server's real processes live here, one per project, and they
	// outlived the server that owned them until this loop existed: removing or
	// disabling the server tore down the shared client and left every project's
	// indexer running with nothing left to route to it.
	dirPrefix := name + dirKeySeparator
	var dirClients []Client
	for key, dc := range m.dirClients {
		if strings.HasPrefix(key, dirPrefix) {
			dirClients = append(dirClients, dc)
			delete(m.dirClients, key)
			delete(m.dirClientLastUse, key)
		}
	}
	for key := range m.preconditions {
		if strings.HasPrefix(key, dirPrefix) {
			delete(m.preconditions, key)
		}
	}
	delete(m.nextRetryAt, name)
	delete(m.failedConfigs, name)
	m.untrackProjectServer(name)
	m.mu.Unlock()

	if exists || len(sessionClients) > 0 || len(dirClients) > 0 {
		logging.Info("Removing MCP server",
			"name", name,
			"reason", reason,
			"projects", projects,
			"session_clients", len(sessionClients),
			"dir_clients", len(dirClients))
	}

	for _, sc := range sessionClients {
		if err := sc.Close(); err != nil {
			logging.Warn("Failed to close session-scoped MCP client", "server", name, "error", err)
		}
	}

	for _, dc := range dirClients {
		if err := dc.Close(); err != nil {
			logging.Warn("Failed to close dir-scoped MCP client", "server", name, "error", err)
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
	if reason, ok := m.sessionRestarts[key]; ok {
		delete(m.sessionRestarts, key)
		lc.setRestartReason(reason)
	}
	m.sessionClients[key] = lc
	m.mu.Unlock()

	logging.Debug("Created session-scoped MCP client",
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
// A non-nil error is the third outcome: the server IS dir-scoped but must not
// serve this project, because the project does not contain the files the server
// declared it needs (config.MCPServer.RequiresFiles). That cannot be reported as
// ok=false — falling through to the shared client would hand this project
// answers indexed from whichever project loaded the server first, which is the
// exact wrong-tree defect dir scoping exists to prevent. Refusing loudly is the
// only correct answer.
//
// Dir clients are always lazy, for the same reason session clients are: a
// language server holds a real index, and a long-lived daemon must not carry one
// per project it has ever served. The reap callback evicts the map entry.
func (m *Manager) dirClientFor(serverName, projectPath string) (Client, bool, error) {
	projectPath = normalizeProjectPath(projectPath)
	if projectPath == "" {
		return nil, false, nil
	}
	key := dirClientKey(serverName, projectPath)

	m.mu.RLock()
	client, exists := m.dirClients[key]
	cfg, registered := m.serverConfigs[serverName]
	m.mu.RUnlock()

	if exists {
		m.touchDirClient(key)
		return client, true, nil
	}
	if !registered || !isDirScopedServer(cfg) {
		return nil, false, nil
	}
	if !m.projectMeetsPrecondition(serverName, cfg, projectPath) {
		return nil, false, fmt.Errorf(
			"MCP server %q is not available in this project: it requires %v, and none was found",
			serverName, requiredFilePatterns(cfg))
	}

	// Bind the config to THIS project before spawning: Dir is what the process
	// starts in, and args carry the same path for servers that take it as a
	// flag. Both are resolved here, once, so the client itself stays generic.
	scoped := cfg
	scoped.Dir = resolveServerDir(cfg, projectPath)

	m.mu.Lock()
	if client, exists := m.dirClients[key]; exists {
		m.dirClientLastUse[key] = time.Now()
		m.mu.Unlock()
		return client, true, nil
	}
	// Constructed with the LOGICAL server name so logs, handshake and ServerInfo
	// read as the server, not as the composite key.
	lc := newLazyClientWithFactory(serverName, scoped, func() (Client, error) {
		return m.clientFactory(serverName, scoped)
	})
	lc.onReap = func() { m.evictDirClient(key, lc) }
	m.dirClients[key] = lc
	m.dirClientLastUse[key] = time.Now()
	evictedKey, evicted := m.evictLRUDirClientLocked()
	m.mu.Unlock()

	// Reaped with m.mu released: reapNow reaches the manager back through onReap.
	if evicted != nil {
		logging.Info("Reaping least-recently-used dir-scoped MCP client at the ceiling",
			"evicted", evictedKey,
			"limit", maxDirScopedClients,
			"reason", "one tree-indexing process per project is correct; unbounded processes per daemon exhausts the machine's file table")
		evicted.reapNow()
	}

	logging.Debug("Created dir-scoped MCP client",
		"server", serverName,
		"projectPath", projectPath,
		"dir", scoped.Dir,
		"reason", "server answers are scoped to the tree it was started in; projects must not share one process")
	return lc, true, nil
}

// touchDirClient records that a dir client was just resolved for a tool call,
// which is what keeps it out of the LRU victim seat.
func (m *Manager) touchDirClient(key string) {
	m.mu.Lock()
	if _, ok := m.dirClients[key]; ok {
		m.dirClientLastUse[key] = time.Now()
	}
	m.mu.Unlock()
}

// evictLRUDirClientLocked drops the least recently used dir client when the map
// is over maxDirScopedClients, and returns it for the caller to reap once m.mu
// is released. It returns nil when nothing needs evicting.
//
// The returned client is reaped rather than closed, so eviction is
// indistinguishable from an idle reap to everything else: the map entry is gone,
// the subprocess is gone, and the wrapper a caller may still be holding stays
// usable and respawns on its next call rather than failing with "closed".
//
// A client with a tool call in flight is never chosen. Two independent guards
// enforce it — the busy() filter here, and lazyClient.idleShutdown's own refusal
// to tear down while inFlight > 0 — because tearing a live call's subprocess out
// from under it is a visible failure, and the ceiling is worth breaching for the
// seconds it takes a call to finish. If every client is busy, nothing is evicted
// and the next resolve tries again.
//
// m.mu must be held.
func (m *Manager) evictLRUDirClientLocked() (string, *lazyClient) {
	if len(m.dirClients) <= maxDirScopedClients {
		return "", nil
	}

	var (
		victimKey string
		victim    *lazyClient
		victimAt  time.Time
	)
	for key, client := range m.dirClients {
		lc, ok := client.(*lazyClient)
		if !ok || lc.busy() {
			continue
		}
		at := m.dirClientLastUse[key]
		if victim == nil || at.Before(victimAt) {
			victimKey, victim, victimAt = key, lc, at
		}
	}
	if victim == nil {
		logging.Warn("Dir-scoped MCP clients over the ceiling but all are mid-call; deferring eviction",
			"count", len(m.dirClients),
			"limit", maxDirScopedClients)
		return "", nil
	}

	delete(m.dirClients, victimKey)
	delete(m.dirClientLastUse, victimKey)
	return victimKey, victim
}

// evictDirClient drops a dir client once its subprocess has been reaped for
// inactivity. The pointer check keeps a late callback from a superseded client
// from removing its replacement.
func (m *Manager) evictDirClient(key string, client Client) {
	m.mu.Lock()
	if current, ok := m.dirClients[key]; ok && current == client {
		delete(m.dirClients, key)
		delete(m.dirClientLastUse, key)
	}
	m.mu.Unlock()
}

// evictSessionClient drops a session client once its subprocess has been reaped
// for inactivity. The pointer check keeps a late callback from a superseded
// client from removing its replacement.
//
// The reaped client's restart reason moves to sessionRestarts, so the
// session's next client — a new process — reports it.
func (m *Manager) evictSessionClient(key string, client Client) {
	m.mu.Lock()
	if current, ok := m.sessionClients[key]; ok && current == client {
		delete(m.sessionClients, key)
		if lc, ok := client.(*lazyClient); ok {
			if reason := lc.takeRestartReason(); reason != "" {
				m.rememberSessionRestartLocked(key, reason)
			}
		}
	}
	m.mu.Unlock()
}

// rememberSessionRestartLocked records why a session's client was dropped, for
// its next client to report. m.mu must be held.
func (m *Manager) rememberSessionRestartLocked(key, reason string) {
	if _, ok := m.sessionRestarts[key]; !ok && len(m.sessionRestarts) >= maxSessionRestartNotes {
		for stale := range m.sessionRestarts {
			delete(m.sessionRestarts, stale)
			break
		}
	}
	m.sessionRestarts[key] = reason
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

	// Get all clients, including every session's and every project's private
	// client. Dir clients are the heaviest thing the manager owns — one indexer
	// process per project — and were missing from this sweep, so shutting the
	// manager down left them running.
	m.mu.Lock()
	clients := m.clients
	for key, client := range m.sessionClients {
		clients[key] = client
	}
	for key, client := range m.dirClients {
		clients[key] = client
	}
	m.clients = make(map[string]Client)
	m.sessionClients = make(map[string]Client)
	m.sessionRestarts = make(map[string]string)
	m.dirClients = make(map[string]Client)
	m.dirClientLastUse = make(map[string]time.Time)
	m.preconditions = make(map[string]preconditionVerdict)
	m.projectServers = make(map[string]map[string]bool)
	m.projectConfigs = make(map[string]config.MCPServer)
	m.mu.Unlock()

	// Close all clients in parallel for faster shutdown
	var wg sync.WaitGroup
	var errorsMu sync.Mutex
	var errors []error

	for name, client := range clients {
		wg.Add(1)
		go func(n string, c Client) {
			defer wg.Done()
			logging.Debug("Closing MCP client", "name", n)
			if err := c.Close(); err != nil {
				logging.Error("Failed to close MCP client", "name", n, "error", err)
				errorsMu.Lock()
				errors = append(errors, fmt.Errorf("%s: %w", n, err))
				errorsMu.Unlock()
			} else {
				logging.Debug("MCP client closed successfully", "name", n)
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

	// Project load is the moment the tree most plausibly changed under us, and
	// the moment a user who just ran `go mod init` expects the tool to appear.
	// Re-decide from scratch rather than serving a verdict from before.
	m.forgetPreconditions(normalizedProjectPath)

	// Prefer provider-backed config resolution when available.
	servers := m.loadProjectServersFromConfig(ctx, normalizedProjectPath)
	if len(servers) == 0 {
		return result
	}

	logging.DebugContext(ctx, "Loading MCP servers from project", "path", normalizedProjectPath, "count", len(servers))

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
			logging.DebugContext(ctx, "Skipping disabled MCP server from project config", "name", name)
			if runningServers[name] {
				reason := fmt.Sprintf("disabled in the MCP config of project %s", normalizedProjectPath)
				if err := m.RemoveServer(name, reason); err != nil && !strings.Contains(err.Error(), "not found") {
					logging.WarnContext(ctx, "Failed to stop disabled MCP server", "name", name, "error", err)
				}
			}
			continue
		}

		// A server that declared what a project must contain before it is worth
		// starting gets asked here, BEFORE the spawn and before the project is
		// associated with an already-running shared client. Skipped exactly like
		// a disabled server — neither loaded nor failed — because refusing to
		// start something that had nothing to do is not a failure. A server that
		// declared nothing always passes, which is every server today.
		if !m.projectMeetsPrecondition(name, serverCfg, normalizedProjectPath) {
			continue
		}

		// The run's org does not allow this server (its policy stamp,
		// daemonpolicy.OrgStamp, carried on ctx): it is never started for
		// it. Skipped like a disabled server, neither loaded nor failed. One
		// already running for another caller stays running; this run is
		// only kept from it (the tool call itself is refused upstream).
		if stamp := daemonpolicy.OrgStampFromContext(ctx); !stamp.MCPServerAllowed(name) {
			logging.InfoContext(ctx, "Not starting an MCP server the run's organization does not allow",
				"name", name, "org_id", stamp.OrgID, "policy_version", stamp.Version)
			continue
		}

		// Check if parent context is cancelled
		select {
		case <-ctx.Done():
			logging.WarnContext(ctx, "Context cancelled, skipping remaining MCP servers", "remaining", name)
			result.FailedServers = append(result.FailedServers, name)
			result.Errors[name] = ctx.Err()
			continue
		default:
		}

		m.loadProjectServer(ctx, normalizedProjectPath, name, serverCfg, result)
	}

	return result
}

// loadProjectServer starts, replaces or associates one server for a project
// load, recording the outcome in result.
//
// The decision and the action are one step per logical server (reloadLock):
// two loads that both read the state before either acted would otherwise both
// replace an edited server, the second closing the process the first had just
// started and dropping its project associations. The decision is re-read from
// the live state after the lock is taken, so a load that waited sees what the
// one before it did. Starting a server holds only its own lock, never m.mu.
func (m *Manager) loadProjectServer(ctx context.Context, projectPath, name string, serverCfg config.MCPServer, result *ProjectServerLoadResult) {
	unlock := m.reloadLock(name)
	defer unlock()

	m.mu.RLock()
	_, running := m.clients[name]
	m.mu.RUnlock()

	if running {
		if !m.projectConfigEdited(projectPath, name, serverCfg) {
			m.trackProjectServer(projectPath, name)
			m.rememberProjectConfig(projectPath, name, serverCfg)
			logging.DebugContext(ctx, "MCP server already running; associated with requested project scope",
				"name", name,
				"projectPath", projectPath)
			result.LoadedServers = append(result.LoadedServers, name)
			return
		}
		// The project's own config for this server changed since it last
		// loaded it, and what is running is not the new config: an edit to
		// mcp.json. Start the new one now. It used to stay on the old command
		// until something restarted it, and a restart issued before the
		// server-side copy of the config caught up restarted the old command
		// again.
		reason := fmt.Sprintf("its config changed in project %s", projectPath)
		if err := m.RemoveServer(name, reason); err != nil && !strings.Contains(err.Error(), "not found") {
			logging.WarnContext(ctx, "Failed to stop an MCP server whose config changed", "name", name, "error", err)
		}
	} else if !m.shouldRetryServerLoad(name, serverCfg) {
		logging.DebugContext(ctx, "Skipping MCP server autoload due to retry cooldown", "name", name)
		return
	}

	// Give first-time package downloads enough time (npx/uvx can be slow)
	serverCtx, cancel := context.WithTimeout(ctx, defaultProjectServerLoadTimeout)
	defer cancel()

	logging.DebugContext(ctx, "Loading MCP server from project config", "name", name)
	if err := m.AddServer(serverCtx, name, serverCfg); err != nil {
		logging.WarnContext(ctx, "Failed to load MCP server (will continue without it)", "name", name, "error", err)
		result.FailedServers = append(result.FailedServers, name)
		result.Errors[name] = err
	} else {
		m.trackProjectServer(projectPath, name)
		result.LoadedServers = append(result.LoadedServers, name)
	}
	m.rememberProjectConfig(projectPath, name, serverCfg)
}

// reloadMutex is one server's reload lock. waiting counts the loads blocked
// on it, which is what lets a test wait for the second of two loads to reach
// the lock instead of sleeping.
type reloadMutex struct {
	sync.Mutex
	waiting atomic.Int32
}

// reloadLock takes the per-server lock loadProjectServer holds, and returns
// its release.
func (m *Manager) reloadLock(name string) func() {
	m.mu.Lock()
	l, ok := m.reloadLocks[name]
	if !ok {
		l = &reloadMutex{}
		m.reloadLocks[name] = l
	}
	m.mu.Unlock()
	l.waiting.Add(1)
	l.Lock()
	l.waiting.Add(-1)
	return l.Unlock
}

// reloadWaiters is how many loads are blocked on name's reload lock.
func (m *Manager) reloadWaiters(name string) int32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if l, ok := m.reloadLocks[name]; ok {
		return l.waiting.Load()
	}
	return 0
}

// shouldRetryServerLoad reports whether a server that failed to start may be
// tried again: once its cooldown has passed, or at once when its config is no
// longer the one that failed. A fix to a broken command used to wait out the
// cooldown like a retry of the same broken command.
func (m *Manager) shouldRetryServerLoad(name string, cfg config.MCPServer) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	next, exists := m.nextRetryAt[name]
	if !exists {
		return true
	}
	if failed, ok := m.failedConfigs[name]; ok && !mcpConfigEqual(failed, cfg) {
		return true
	}
	return time.Now().After(next)
}

// projectConfigEdited reports whether a running server must be restarted for
// a project because the project's config for it was edited: the config this
// project last loaded it under differs from the one it reads now, and the
// running server is not already the new one.
//
// The first comparison is what keeps this to edits. Two projects that declare
// one server name differently share its single client (it is name-scoped);
// comparing only against what is running would restart it on every load as
// the two projects took turns, where neither had changed anything.
func (m *Manager) projectConfigEdited(projectPath, name string, cfg config.MCPServer) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	previous, seen := m.projectConfigs[dirClientKey(name, projectPath)]
	if !seen || mcpConfigEqual(previous, cfg) {
		return false
	}
	running, ok := m.serverConfigs[name]
	return !ok || !mcpConfigEqual(running, cfg)
}

// rememberProjectConfig records the config a project loaded a server under,
// for projectConfigEdited.
func (m *Manager) rememberProjectConfig(projectPath, name string, cfg config.MCPServer) {
	m.mu.Lock()
	m.projectConfigs[dirClientKey(name, projectPath)] = cfg
	m.mu.Unlock()
}

// mcpConfigEqual compares two server configs, an empty list or map being the
// same as an absent one (config read from JSON and config built in code spell
// "none" differently).
func mcpConfigEqual(a, b config.MCPServer) bool {
	return reflect.DeepEqual(normalizeMCPConfig(a), normalizeMCPConfig(b))
}

func normalizeMCPConfig(c config.MCPServer) config.MCPServer {
	if len(c.Env) == 0 {
		c.Env = nil
	}
	if len(c.Args) == 0 {
		c.Args = nil
	}
	if len(c.Headers) == 0 {
		c.Headers = nil
	}
	if len(c.RequiresFiles) == 0 {
		c.RequiresFiles = nil
	}
	return c
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

	reason := "replaced by a new registration"
	if err := m.RemoveProjectServer(normalizedProjectPath, serverName, reason); err != nil && !strings.Contains(err.Error(), "not found") {
		return err
	}

	if err := m.AddServer(ctx, serverName, cfg); err != nil {
		return err
	}

	m.trackProjectServer(normalizedProjectPath, serverName)
	return nil
}

// ProjectServerConfig is the config a project's MCP config files declare for
// one server on this machine now, read from disk, with the built-in servers
// beneath them. ok is false when the project declares no such server.
//
// A server process works from a copy of these files that the machine syncs
// to it every few seconds. A restart has to start what the files say now, or a
// restart issued right after an edit starts the old command again.
func (m *Manager) ProjectServerConfig(ctx context.Context, projectPath, serverName string) (config.MCPServer, bool) {
	normalized := normalizeProjectPath(projectPath)
	if normalized == "" {
		return config.MCPServer{}, false
	}
	cfg, ok := m.loadProjectServersFromConfig(ctx, normalized)[serverName]
	return cfg, ok
}

// RemoveProjectServer removes and closes a project-scoped server connection.
// reason is logged as RemoveServer's is.
//
// The server is name-scoped, so removing it for one project removes it for
// every project and session sharing it; the log names them all.
func (m *Manager) RemoveProjectServer(projectPath, serverName, reason string) error {
	if !m.isProjectServer(projectPath, serverName) {
		return fmt.Errorf("server %s not found", serverName)
	}
	return m.RemoveServer(serverName, fmt.Sprintf("%s (requested for project %s)", reason, normalizeProjectPath(projectPath)))
}

// RestartProjectServer restarts a project-scoped server with updated config.
func (m *Manager) RestartProjectServer(ctx context.Context, projectPath, serverName string, cfg config.MCPServer) error {
	if err := m.RemoveProjectServer(projectPath, serverName, "restart"); err != nil && !strings.Contains(err.Error(), "not found") {
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
	dirClient, ok, err := m.dirClientFor(serverName, projectPath)
	if err != nil {
		// The server is dir-scoped but declined this project. Surfacing the
		// refusal is the point: the shared client belongs to another tree.
		return nil, err
	}
	if ok {
		// Private and lazily (re)spawned, so the shared server's health gate does
		// not describe it; a failure surfaces as the call's own error.
		return dirClient.CallTool(toolName, arguments)
	}
	return m.CallTool(session, serverName, toolName, arguments)
}

// ProjectListTools lists one project-scoped server's tools, resolved the way
// ProjectCallTool resolves a call. For a DIR-SCOPED server that is the
// project's own client: the shared client was registered with no tree, so a
// server that takes the tree as `${projectPath}` (or reads its working
// directory) cannot even start from it. Listing it there failed or answered
// for the wrong tree, which is how a dir-scoped server connected and yet its
// tools were never offered to an agent. Every other server lists from the
// shared client, as before.
//
// A dir client's first listing starts its server, which a tree indexer may
// take a minute to do. The caller is answered within projectToolListBudget
// either way (ErrToolsStillLoading when the start runs longer); the start goes
// on, and the listing after it is served from its manifest.
func (m *Manager) ProjectListTools(projectPath, serverName string) ([]Tool, error) {
	if err := m.ensureInitialized(); err != nil {
		return nil, fmt.Errorf("failed to initialize MCP servers: %w", err)
	}
	if !m.isProjectServer(projectPath, serverName) {
		return nil, fmt.Errorf("server %q not found", serverName)
	}
	dirClient, ok, err := m.dirClientFor(serverName, projectPath)
	if err != nil {
		return nil, err
	}
	if ok {
		return m.listDirClientTools(serverName, projectPath, dirClient)
	}
	m.mu.RLock()
	client, exists := m.clients[serverName]
	m.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("server %q not found", serverName)
	}
	return client.ListTools()
}

// projectToolListBudget bounds how long a project's listing waits on a dir
// client's tools. The worker gives each mcp.list_tools 10s
// (toolexec daemonMCPRuntime.listTools); answering inside that keeps one slow
// server from failing the listing of every other.
var projectToolListBudget = 8 * time.Second

// ErrToolsStillLoading is a dir-scoped server whose tools are not known yet
// because its server is still starting for the project.
var ErrToolsStillLoading = errors.New("still starting in this project; its tools are offered once it answers")

// listDirClientTools lists a dir client's tools within projectToolListBudget.
// Concurrent listings of one (server, project) share one probe.
func (m *Manager) listDirClientTools(serverName, projectPath string, client Client) ([]Tool, error) {
	if lc, ok := client.(*lazyClient); ok {
		if tools, cached := lc.cachedManifest(); cached {
			return tools, nil
		}
	}
	// Keyed by the client instance as well: a replacement (an edit to
	// mcp.json) must never join its predecessor's probe, which outlives a
	// removal and would answer with the old server's tools.
	flight := fmt.Sprintf("%s|%p", dirClientKey(serverName, normalizeProjectPath(projectPath)), client)
	ch := m.toolListFlights.DoChan(flight, func() (interface{}, error) {
		return client.ListTools()
	})
	timer := time.NewTimer(projectToolListBudget)
	defer timer.Stop()
	select {
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		tools, _ := res.Val.([]Tool)
		return tools, nil
	case <-timer.C:
		return nil, fmt.Errorf("MCP server %q is %w", serverName, ErrToolsStillLoading)
	}
}

// ProjectToolCount is how many tools a project-scoped server offers the
// project, as far as is known WITHOUT starting anything: a status report must
// answer at once, and a dir-scoped server's first listing may start a tree
// indexer. ok is false when it is not known yet (a dir-scoped server not yet
// listed for this project).
func (m *Manager) ProjectToolCount(projectPath, serverName string) (int, bool) {
	normalized := normalizeProjectPath(projectPath)
	m.mu.RLock()
	cfg, registered := m.serverConfigs[serverName]
	dirClient, hasDir := m.dirClients[dirClientKey(serverName, normalized)]
	shared, hasShared := m.clients[serverName]
	m.mu.RUnlock()
	if registered && isDirScopedServer(cfg) {
		if !hasDir {
			return 0, false
		}
		if lc, ok := dirClient.(*lazyClient); ok {
			tools, cached := lc.cachedManifest()
			return len(tools), cached
		}
		return 0, false
	}
	if !hasShared {
		return 0, false
	}
	tools, err := shared.ListTools()
	if err != nil {
		return 0, false
	}
	return len(tools), true
}

// ListProjectTools returns tools from all connected project-scoped MCP servers.
//
// A dir-scoped server lists from the project's own client (ProjectListTools);
// a project its declared files rule out offers none of its tools, and that is
// not a failure.
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

		dirClient, isDir, dirErr := m.dirClientFor(name, projectPath)
		if dirErr != nil {
			logging.Debug("Not offering a dir-scoped MCP server's tools in this project", "name", name, "reason", dirErr)
			continue
		}
		if isDir {
			// Private and lazily (re)spawned, like a dir-scoped call: its
			// failure is not the shared server's health.
			tools, err := m.listDirClientTools(name, projectPath, dirClient)
			if err != nil {
				logging.Error("Failed to list tools from dir-scoped MCP server", "name", name, "projectPath", projectPath, "error", err)
				errors = append(errors, fmt.Errorf("%s: %w", name, err))
				continue
			}
			if len(tools) > 0 {
				result[name] = tools
			}
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
func builtinMCPServers(chromeBrowserURL string) map[string]config.MCPServer {
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
	if browserURL := strings.TrimSpace(chromeBrowserURL); browserURL != "" {
		command, args := chromeDevtoolsLaunch()
		servers[chromeDevtoolsServerName] = config.MCPServer{
			Type:    config.MCPStdio,
			Command: command,
			Args: append(args,
				"--isolated",
				"--browserUrl="+browserURL,
			),
			Enabled: true,
		}
	} else if chromePath := detectSystemChrome(); chromePath != "" {
		command, args := chromeDevtoolsLaunch()
		args = append(args,
			"--headless",
			"--isolated",
			"--executablePath="+chromePath,
		)
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
			Command: command,
			Args:    args,
			Enabled: true,
		}
	}

	return servers
}

// lookPath is exec.LookPath, a seam for tests.
var lookPath = exec.LookPath

// chromeDevtoolsLaunch is the command and leading arguments that start the
// pinned chrome-devtools-mcp: its installed binary when the machine already
// has exactly that version on PATH (the cloud workspace image preinstalls it),
// `npx -y chrome-devtools-mcp@<pin>` otherwise.
//
// Why not always npx: npx resolves the package through npm on every spawn
// before node even starts the server. Measured on a cloud machine at load
// average ~90 (31 CPUs): the initialize handshake answered after 30-50s
// through npx and after 7.5s from the installed binary, against a 45s start
// budget — so under load the browser MCP failed with "failed to connect to MCP
// server: context deadline exceeded" while the server itself was healthy.
//
// A binary of any other version is not used: the flags below are written for
// the pinned release, and an image that drifted from the pin falls back to the
// pinned npx package rather than to an untested server.
func chromeDevtoolsLaunch() (string, []string) {
	if bin := installedChromeDevtoolsMCP(); bin != "" {
		return bin, nil
	}
	return "npx", []string{"-y", "chrome-devtools-mcp@" + chromeDevtoolsMCPVersion}
}

// installedChromeDevtoolsMCP returns the chrome-devtools-mcp on PATH when its
// package is the pinned version, or "". npm installs the bin as a symlink into
// the package (…/node_modules/chrome-devtools-mcp/build/src/…), so the version
// is the package.json of the first ancestor that names the package.
func installedChromeDevtoolsMCP() string {
	bin, err := lookPath("chrome-devtools-mcp")
	if err != nil {
		return ""
	}
	target, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return ""
	}
	const maxDepth = 8
	dir := filepath.Dir(target)
	for i := 0; i < maxDepth && dir != filepath.Dir(dir); i, dir = i+1, filepath.Dir(dir) {
		data, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err != nil {
			continue
		}
		var pkg struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if json.Unmarshal(data, &pkg) != nil || pkg.Name != "chrome-devtools-mcp" {
			continue
		}
		if pkg.Version != chromeDevtoolsMCPVersion {
			logging.Info("Installed chrome-devtools-mcp is not the pinned version; launching the pinned one through npx",
				"path", bin, "installed", pkg.Version, "pinned", chromeDevtoolsMCPVersion)
			return ""
		}
		return bin
	}
	return ""
}

func (m *Manager) loadProjectServersFromConfig(ctx context.Context, projectPath string) map[string]config.MCPServer {
	// The server role has no filesystem to read and no built-in stdio servers;
	// project config reaches it, if ever, only through a curated catalog.
	if m.role != RoleDaemon {
		return nil
	}
	m.mu.RLock()
	resolver := m.projectConfigResolver
	m.mu.RUnlock()

	var userServers map[string]config.MCPServer

	if resolver == nil {
		// No resolver configured — use filesystem directly.
		// This path is used by the daemon runtime where filesystem access is expected.
		userServers = configloader.LoadMCPServersFromProjectScopes(projectPath)
		if len(userServers) == 0 {
			logging.WarnContext(ctx, "Project config resolver is not configured; skipping MCP project autoload", "projectPath", projectPath)
		}
	} else {
		cfg, err := resolver(ctx, projectPath)
		if err != nil {
			logging.WarnContext(ctx, "Failed provider-backed project config resolution; skipping MCP project autoload",
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
	builtin := builtinMCPServers(m.chromeBrowserURL)
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
