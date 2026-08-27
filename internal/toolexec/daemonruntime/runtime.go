// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/cgroupmem"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/instanceid"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/netports"
	"github.com/reliant-labs/reliant/internal/skills/catalog"
	"github.com/reliant-labs/reliant/internal/terminal"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
	"github.com/reliant-labs/reliant/internal/toolexec/transport"
)

const (
	logPrefix              = "[🔧 ToolsDaemonClient]"
	defaultProjectScanPath = "."
)

type daemonClient struct {
	daemonID   string
	daemonName string
	userID     string
	hostname   string
	platform   string
	cwd        string
	// serverURL is the API server origin these credentials belong to. Used
	// as the per-origin key into ~/.reliant/daemon.json when persisting the
	// server-assigned daemonID after registration. Empty in server mode.
	serverURL string
	bootCfg   bootstrap.DaemonBootstrapConfig

	// gatewayClient and gatewayURL are built ONCE and reused by every session.
	// The transport underneath owns an HTTP/2 connection pool, so building one
	// per session leaks a live TCP connection (and its reader/writer
	// goroutines) on every reconnect: a daemon that flapped 60 times held 60
	// ESTABLISHED sockets to the gateway, none of them carrying a stream.
	// Reusing it also means a reconnect resumes on the pooled connection
	// instead of paying a fresh handshake.
	gatewayClient reliantv1connect.ToolsDaemonServiceClient
	gatewayURL    string

	mcpManager    *mcp.Manager
	localExecutor *toolexec.LocalToolExecutor
	capabilities  []string
	// runtimeType is the sandbox/runtime this daemon executes under ("kata",
	// "gvisor"), sourced from the DAEMON_RUNTIME_TYPE env stamped on cloud
	// daemon pods. Empty for local/unknown daemons. Advertised to the server
	// via a registration label so the model can be told about runtime limits.
	runtimeType string

	// sendCh decouples message producers from the stream I/O.
	// All goroutines push messages here; a single runSender goroutine
	// drains the channel and calls stream.Send(). This prevents work
	// goroutines from blocking on the stream write mutex/I/O.
	sendCh      chan *reliantv1.DaemonMessage
	sendDone    chan struct{} // closed when runSender exits
	sessionDone chan struct{} // closed when session is ending, before sendCh is closed

	cancelMu    sync.Mutex
	cancelByReq map[string]context.CancelFunc
	// backgroundByReq marks in-flight executions the user asked to detach into
	// a background process. The executing command polls this by request id (see
	// exec.Run) — the same correlation cancel uses, and the only handle the
	// daemon keeps for a running execution.
	backgroundByReq map[string]string // requestID -> toolCallID
	watchersMu      sync.Mutex
	watchersByPr    map[string]context.CancelFunc
	fsWatchersMu    sync.Mutex
	fsWatchersByPr  map[string]context.CancelFunc

	terminalPumps     *terminalPumpTracker
	processOutputSubs *processOutputSubTracker

	// memWatcher samples the workspace cgroup's memory usage (cloud daemons)
	// so heartbeats can carry used/limit/pressure telemetry to the gateway.
	// Inert on hosts without cgroup v2 accounting (macOS, local daemons).
	memWatcher *cgroupmem.Watcher

	// portWatcher scans /proc/net/tcp{,6} for loopback/wildcard LISTEN
	// sockets so heartbeats can carry the detected ports (the preview
	// affordance in the UI). Only set for pod-hosted daemons — nil for
	// local daemons, and all accessors are nil-safe.
	portWatcher *netports.Watcher
}

type StartOptions struct {
	BootstrapConfig bootstrap.DaemonBootstrapConfig
}

func Start(ctx context.Context, opts StartOptions) error {
	if err := opts.BootstrapConfig.Validate(); err != nil {
		return fmt.Errorf("daemon runtime bootstrap config invalid: %w", err)
	}

	// Claim the data directory before anything else, so a second daemon fails
	// here rather than at the gateway. Two daemons on one host resolve to one
	// daemonID; the gateway keys connections by daemonID and evicts the
	// incumbent on every registration, so the pair evict each other until one
	// is killed. Refusing to be the second one is the only place that costs
	// nothing.
	lock, err := daemonstate.Acquire(opts.BootstrapConfig.DataDir)
	if err != nil {
		if errors.Is(err, daemonstate.ErrLocked) {
			return fmt.Errorf("%s: %w", describeIncumbentDaemon(opts.BootstrapConfig.DataDir), err)
		}
		return err
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			logging.Warn(logPrefix+" Failed to release daemon data dir claim", "error", releaseErr)
		}
	}()

	client, err := newDaemonClient(opts.BootstrapConfig)
	if err != nil {
		return err
	}

	// Publish the runtime record before the first dial. Until the gateway acks
	// registration the record says so, which is what makes `reliant daemon
	// status` able to distinguish "the process exists" from "the daemon is
	// serving tool calls".
	gatewayURL := opts.BootstrapConfig.GRPCURL
	if !opts.BootstrapConfig.ServerMode {
		// Record the URL actually dialed, not the one typed, so an operator who
		// passed grpc:// sees where the daemon really went.
		if normalized, nErr := opts.BootstrapConfig.GatewayURL(); nErr == nil {
			gatewayURL = normalized
		}
	}
	if stateErr := daemonstate.Init(
		opts.BootstrapConfig.DataDir,
		gatewayURL,
		string(opts.BootstrapConfig.TLSMode),
		opts.BootstrapConfig.ServerMode,
	); stateErr != nil {
		logging.Warn(logPrefix+" Failed to write daemon runtime state", "error", stateErr)
	}
	defer func() {
		if clearErr := daemonstate.Clear(opts.BootstrapConfig.DataDir); clearErr != nil {
			logging.Warn(logPrefix+" Failed to clear daemon runtime state", "error", clearErr)
		}
	}()

	// Watch workspace memory (cgroup v2) for pressure telemetry. Returns
	// immediately on hosts without cgroup accounting, so this is free for
	// local/mac daemons. Runs for the daemon's whole lifetime, across
	// reconnects, in both server and client modes.
	go client.memWatcher.Run(ctx)

	// If GIT_TOKEN is set (injected by workspace reconciler in cloud mode),
	// configure git credential-store so all git operations use the token.
	setupGitCredentials()

	// Preview forwarder: in-pod reverse proxy that lets the workspace-proxy
	// reach loopback-bound user dev servers (see preview_forwarder.go). Runs
	// only where a pod-facing surface exists: cloud pods set
	// DAEMON_FRONTEND_PORT (workspace-controller pod spec), and server-mode
	// daemons are always pod-hosted. Local/mac daemons skip it — nothing
	// routes to their loopback, and binding :9191 could collide with the
	// docker-compose daemon-gateway on dev machines. Startup failure is
	// logged, never fatal: a broken preview surface must not take down tool
	// execution.
	if os.Getenv("DAEMON_FRONTEND_PORT") != "" || opts.BootstrapConfig.ServerMode {
		// Reserve the daemon's own RPC listen port so a preview URL can never
		// route into the Connect/gRPC surface (the forwarder adds its own
		// bound port automatically).
		rpcPort := opts.BootstrapConfig.ListenPort
		if rpcPort == 0 {
			rpcPort = 9190
		}
		forwarder := NewPreviewForwarder(rpcPort)
		if err := forwarder.Start(previewListenAddr(os.Getenv)); err != nil {
			logging.Warn(logPrefix+" preview forwarder failed to start (previews of loopback-bound servers will 502)", "error", err)
		} else {
			logging.Info(logPrefix+" preview forwarder started", "addr", forwarder.Addr().String())
			go func() {
				<-ctx.Done()
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = forwarder.Shutdown(shutdownCtx)
			}()
		}

		// Detect listening user ports (loopback/wildcard LISTEN sockets) and
		// piggyback them on heartbeats — same lifecycle as the forwarder they
		// feed, and same pod-only gate: on a local dev machine the listener
		// table is the user's whole machine, which is noise (and none of it
		// is proxy-reachable anyway). Inert without /proc/net/tcp.
		previewPort := DefaultPreviewPort
		if addr := previewListenAddr(os.Getenv); strings.HasPrefix(addr, ":") {
			if p, err := strconv.Atoi(addr[1:]); err == nil {
				previewPort = p
			}
		}
		client.portWatcher = netports.NewWatcher(netports.DefaultPollInterval, rpcPort, previewPort)
		go client.portWatcher.Run(ctx)
	}

	if opts.BootstrapConfig.ServerMode {
		logging.Info(logPrefix+" Starting in server mode (listening for gateway)",
			"daemonID", client.daemonID,
			"userID", client.userID,
			"cwd", client.cwd,
			"listenPort", opts.BootstrapConfig.ListenPort,
		)
		if err := client.runServerMode(ctx); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("tools daemon server stopped: %w", err)
		}
		return nil
	}

	logging.Info(logPrefix+" Starting in-process tools daemon runtime",
		"daemonID", client.daemonID,
		"userID", client.userID,
		"cwd", client.cwd,
	)

	if err := client.run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("tools daemon runtime stopped: %w", err)
	}
	return nil
}

// describeIncumbentDaemon names the daemon already holding dataDir, for the
// refusal message. The runtime record is the only thing that can say WHICH
// process it is; the claim itself only says that one exists. The record may be
// missing or stale — that is precisely why it is not what the refusal is based
// on — so this degrades to a generic sentence rather than lying.
func describeIncumbentDaemon(dataDir string) string {
	state, err := daemonstate.Read(dataDir)
	if err != nil || state.PID == 0 {
		return "a daemon is already running against this data directory — stop it with `reliant daemon stop` before starting another"
	}
	return fmt.Sprintf(
		"daemon PID %d is already running against this data directory (started %s) — stop it with `reliant daemon stop` before starting another",
		state.PID, state.StartedAt.Local().Format(time.RFC3339))
}

func newDaemonClient(bootCfg bootstrap.DaemonBootstrapConfig) (*daemonClient, error) {
	// Resolve CWD: prefer DAEMON_WORKING_DIR env var (used in Docker), else os.Getwd().
	cwd := strings.TrimSpace(os.Getenv("DAEMON_WORKING_DIR"))
	if cwd == "" {
		resolvedCwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to get working dir: %w", err)
		}
		cwd = resolvedCwd
	}

	// The real hostname, deliberately. It is sent as DaemonRegister.Hostname and
	// stored in daemons.hostname, where the gateway's resolveUnboundDaemonID
	// matches it to reuse an existing daemon row. Substituting a stable id here
	// would orphan that row. Stability comes from DaemonRegister.DaemonId (the
	// server-assigned id we re-assert), which is what makes identity survive a
	// hostname flip; this field stays the machine's current name.
	hostname := instanceid.Hostname()

	// DAEMON_RUNTIME_TYPE is stamped by the control-plane on cloud daemon pods
	// (e.g. "kata", "gvisor"); absent for local/self-hosted daemons.
	runtimeType := strings.TrimSpace(os.Getenv("DAEMON_RUNTIME_TYPE"))

	mcpManager := mcp.NewManager()
	storedConfigProvider := config.NewStoredConfigProvider(&filesystemConfigStore{})
	mcpManager.SetProjectConfigResolver(func(ctx context.Context, projectPath string) (*config.Config, error) {
		// Pass the filesystem path directly as the "projectID" — the
		// filesystemConfigStore treats it as a path, not a DB identifier.
		return storedConfigProvider.GetProjectConfig(ctx, config.ProjectRef{ProjectID: projectPath})
	})

	toolsFactory := tools.NewToolsFactory(&tools.ToolsOptions{})

	caps := toolsFactory.ListAvailableToolsForLocation(tools.ToolRunsOnDaemon)
	sort.Strings(caps)

	localExec := toolexec.NewLocalToolExecutor(toolsFactory)
	localExec.SetMCPContextBinder(toolexec.NewLocalMCPContextBinder(mcpManager))
	// The daemon runtime IS the local machine, so give the executor a
	// LocalClient so filesystem tools (glob, view, etc.) can operate.
	localExec.SetDaemonClient(daemon.NewLocalClient())

	// Initialize the terminal manager so terminal.* daemon commands can
	// create and manage PTY sessions on the user's machine.
	SetTerminalManager(terminal.NewManager())
	SetMCPManager(mcpManager)

	// daemonID is seeded from persisted per-origin credentials (bootCfg.DaemonID)
	// so a returning daemon re-asserts its stable identity in the registration
	// message. On first-ever registration it's empty; the gateway assigns one
	// via RegistrationAck and we persist it back to daemon.json.
	// The default display name carries the stable instance id alongside the
	// hostname, so two daemons whose hostname flipped mid-session are still
	// recognizable as the same machine in the daemon list. This is a label
	// only — nothing keys off it.
	daemonName := bootCfg.Name
	if daemonName == "" {
		daemonName = instanceid.Label()
	}

	// Seed the process-wide identity from persisted creds so exec.bg_list can
	// stamp ProcessInfo.DaemonID even before the RegistrationAck re-asserts it.
	SetDaemonIdentity(bootCfg.DaemonID)

	// One transport for the process. A bad URL or TLS mode is named here, at
	// startup, rather than becoming a session error that reconnect retries
	// forever against a config that can never come good.
	httpClient, gatewayURL, err := transport.NewDaemonHTTPClient(bootCfg)
	if err != nil {
		return nil, fmt.Errorf("creating daemon HTTP client: %w", err)
	}

	return &daemonClient{
		daemonID:   bootCfg.DaemonID,
		daemonName: daemonName,
		// userID is set later from RegistrationAck (gateway-derived from PAT).
		hostname:          hostname,
		platform:          runtime.GOOS,
		cwd:               cwd,
		serverURL:         bootCfg.ServerURL,
		bootCfg:           bootCfg,
		gatewayClient:     reliantv1connect.NewToolsDaemonServiceClient(httpClient, gatewayURL, connect.WithGRPC()),
		gatewayURL:        gatewayURL,
		mcpManager:        mcpManager,
		localExecutor:     localExec,
		capabilities:      caps,
		runtimeType:       runtimeType,
		cancelByReq:       make(map[string]context.CancelFunc),
		backgroundByReq:   make(map[string]string),
		watchersByPr:      make(map[string]context.CancelFunc),
		fsWatchersByPr:    make(map[string]context.CancelFunc),
		terminalPumps:     newTerminalPumpTracker(),
		processOutputSubs: newProcessOutputSubTracker(),
		memWatcher:        cgroupmem.NewWatcher(memReader, cgroupmem.DefaultPollInterval),
	}, nil
}

// persistDaemonID writes the server-assigned daemon id into the per-origin
// entry of ~/.reliant/daemon.json so it survives daemon restarts and machine
// hostname changes. It's a no-op when there's no server origin to key by
// (server mode / in-process runtime), when the id is empty, or when the
// persisted value already matches — avoiding needless disk writes on every
// reconnect. Best-effort: a write failure is logged but never fatal, since
// the daemon is already connected and functional.
func (d *daemonClient) persistDaemonID(daemonID string) {
	if daemonID == "" || strings.TrimSpace(d.serverURL) == "" {
		return
	}
	creds, err := auth.ReadDaemonCredentials(d.serverURL)
	if err != nil {
		logging.Warn(logPrefix+" Failed to read daemon credentials while persisting daemon id",
			"error", err, "serverURL", d.serverURL)
		return
	}
	if creds == nil {
		// No persisted entry for this origin (e.g. --token flow that never
		// wrote creds). Nothing to update — the id lives only in memory.
		return
	}
	if creds.DaemonID == daemonID {
		return
	}
	creds.DaemonID = daemonID
	if err := auth.WriteDaemonCredentials(creds); err != nil {
		logging.Warn(logPrefix+" Failed to persist assigned daemon id",
			"error", err, "serverURL", d.serverURL, "daemonID", daemonID)
		return
	}
	logging.Info(logPrefix+" Persisted stable daemon id for origin",
		"daemonID", daemonID, "serverURL", d.serverURL)
}

// registerLabels builds the daemon-registration label map. It advertises the
// daemon's runtime/sandbox type when known so the server can surface runtime
// capability limits to the model. Returns nil when there is nothing to report,
// keeping the registration message unchanged for local daemons.
func (d *daemonClient) registerLabels() map[string]string {
	if strings.TrimSpace(d.runtimeType) == "" {
		return nil
	}
	return map[string]string{
		config.DaemonRuntimeTypeLabelKey: d.runtimeType,
	}
}

// setupGitCredentials configures git credential-store globally when GIT_TOKEN
// is set (injected by the workspace reconciler in cloud mode). This runs once
// at daemon startup so all subsequent git operations use the token.
func setupGitCredentials() {
	token := os.Getenv("GIT_TOKEN")
	if token == "" {
		return
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		logging.Warn(logPrefix+" Failed to resolve home dir for git credentials", "error", err)
		return
	}

	// Configure git to use the credential-store helper globally.
	if err := exec.Command("git", "config", "--global", "credential.helper", "store").Run(); err != nil {
		logging.Warn(logPrefix+" Failed to configure git credential.helper", "error", err)
		return
	}

	// Append the token to ~/.git-credentials (don't overwrite existing entries).
	credFile := filepath.Join(homeDir, ".git-credentials")
	credLine := fmt.Sprintf("https://x-access-token:%s@github.com\n", token)

	f, err := os.OpenFile(credFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		logging.Warn(logPrefix+" Failed to open .git-credentials", "error", err)
		return
	}
	defer f.Close()

	if _, err := f.WriteString(credLine); err != nil {
		logging.Warn(logPrefix+" Failed to write .git-credentials", "error", err)
		return
	}

	logging.Info(logPrefix + " Configured git credential-store with GIT_TOKEN")
}

// recordStream publishes a gateway-stream transition to the daemon's on-disk
// runtime record so `reliant daemon status` reports the stream, not the PID.
// Best-effort: a daemon that is connected and serving must not die because a
// status file could not be written.
func (d *daemonClient) recordStream(s daemonstate.Stream, detail string) {
	if err := daemonstate.SetStream(d.bootCfg.DataDir, s, detail); err != nil {
		logging.Warn(logPrefix+" Failed to record daemon stream state", "error", err, "stream", string(s))
	}

	// Announce the transition on stdout as well as recording it.
	//
	// A supervising Electron watches the runtime record by stat-polling it
	// every 250ms, so it learns of a connect up to a full interval late — 146ms
	// after the fact in a measured dev sign-in — for news the daemon already
	// had. When the daemon was spawned as a child there is an ordered pipe
	// straight to the supervisor, so telling it costs one line and removes the
	// wait entirely.
	//
	// The file stays authoritative and the poll stays as the fallback: a daemon
	// running on another machine has no stdout its supervisor can read, and
	// anything not spawned by Electron has no reader at all. This is a fast
	// path, never the only path — which is also why a failed write above does
	// not suppress it.
	//
	// Written directly to stdout rather than through logging so it cannot be
	// reformatted, level-filtered, or routed to a file by a logger change.
	//
	// Only in verbose mode. The notice is machine output for a supervising
	// parent — Electron passes --verbose on every spawn precisely so this path
	// stays on — and printing `@@RELIANT_STREAM connecting` at a human running
	// the daemon in a terminal is noise they cannot act on. Suppressing it
	// costs a supervisor that somehow ran without the flag nothing but latency:
	// the stat-poll fallback still sees the transition in the on-disk record,
	// which stays authoritative in both modes.
	if d.bootCfg.Verbose {
		fmt.Printf("%s %s\n", daemonstate.StreamNoticePrefix, string(s))
	}

	d.announceStatus(s, detail)
}

// announceStatus prints the short, human-readable half of a stream transition:
// one line a person running `reliant daemon start` in a terminal can act on.
//
// Deliberately NOT the structured log. In non-verbose mode the slog stream goes
// to the rotating file only (see commands.setupToolsDaemonLogging), leaving
// stdout for these. In verbose mode the structured lines are already on stdout
// and say the same thing with more detail, so these would be duplication —
// hence the early return.
func (d *daemonClient) announceStatus(s daemonstate.Stream, detail string) {
	if d.bootCfg.Verbose {
		return
	}

	switch s {
	case daemonstate.StreamConnected:
		fmt.Printf("\n  ✓ Daemon connected — you can head back to Reliant and start working.\n\n")
		fmt.Printf("    Press Ctrl+C to disconnect.\n\n")
	case daemonstate.StreamAwaitingCredentials:
		fmt.Printf("  … Waiting for sign-in to finish.\n")
	case daemonstate.StreamDisconnected:
		// Only worth a line when it carries a reason; a bare disconnect is
		// immediately followed by a reconnect attempt, and narrating both
		// halves of a retry loop is the noise this whole change removes.
		if detail != "" {
			fmt.Printf("  ! Disconnected: %s\n    Reconnecting…\n", detail)
		}
	}
}

// isFatalError returns true for errors that should not be retried (e.g. auth failures).
func isFatalError(err error) bool {
	if err == nil {
		return false
	}
	code := connect.CodeOf(err)
	switch code {
	case connect.CodePermissionDenied:
		// Authenticated and refused: the gateway knows who this is and says no.
		// Redialing cannot change that answer, so stop.
		//
		// Unauthenticated is deliberately NOT here — see the comment on the
		// retry path in run(). The two look alike and recover differently.
		return true
	case connect.CodeUnimplemented:
		// Server doesn't have the ConnectDaemon endpoint — wrong URL.
		return true
	case connect.CodeAborted:
		// Another daemon process claimed this daemon id while we were being
		// served. Redialing would evict it and be evicted right back, forever:
		// two local daemons sharing one id flapped every 15-30s for 37 minutes
		// on 2026-07-27, losing every in-flight tool request. Exactly one
		// process may hold a daemon id, so the superseded one stops.
		return true
	}
	return false
}

func (d *daemonClient) run(ctx context.Context) error {
	delay := transport.ReconnectMinDelay
	for {
		if ctx.Err() != nil {
			d.stopAllStreams()
			return ctx.Err()
		}

		sessionStart := time.Now()
		d.recordStream(daemonstate.StreamConnecting, "")
		err := d.runSession(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			d.recordStream(daemonstate.StreamDisconnected, err.Error())
			if isFatalError(err) {
				logging.Error(logPrefix+" Fatal error — not reconnecting",
					"error", err,
					"code", connect.CodeOf(err).String(),
					"grpc_url", d.bootCfg.GRPCURL,
				)
				d.stopAllStreams()
				if connect.CodeOf(err) == connect.CodeAborted {
					// Name the condition rather than calling it a connection
					// failure: the connection worked fine, another daemon
					// process took the identity. The remedy is to stop one of
					// them, and the operator cannot infer that from "connection
					// failed".
					return fmt.Errorf(
						"another daemon process claimed this daemon's identity on %s — "+
							"exactly one daemon may run per identity, so this one is stopping: %w",
						d.bootCfg.GRPCURL, err)
				}
				return fmt.Errorf("daemon connection failed (not retrying): %w", err)
			}
			if connect.CodeOf(err) == connect.CodeUnauthenticated {
				// Credentials the gateway would not accept. This USED to be
				// fatal, and that is what cost a full working day.
				//
				// On 2026-08-24 a dev-k8s deploy repointed the gateway at a
				// different database, so the daemon's PAT was looked up where
				// it had never been written and came back "invalid token". The
				// daemon declared the error fatal, stopped, and logged nothing
				// for the next 10 hours while the app showed "no machine is
				// connected". Restoring the gateway did not help: nothing was
				// left running to notice. Only killing the process — so its
				// supervisor respawned it — brought the daemon back.
				//
				// Unauthenticated is RECOVERABLE and this process cannot
				// recover it alone: the PAT is minted by the supervisor
				// (Electron's ensureDaemonCreds), and the gateway may simply be
				// misconfigured or mid-rollout. All of those get fixed AROUND a
				// running daemon, so the daemon's job is to still be trying
				// when they are. Retrying costs one connect per backoff
				// interval, capped at ReconnectMaxDelay (15s).
				//
				// PermissionDenied stays fatal: there the identity was accepted
				// and the answer was still no, which redialing cannot change.
				logging.Error(logPrefix+" Rejected: credentials not accepted — retrying. "+
					"Causes, in order of likelihood: the daemon's PAT is stale and the "+
					"supervisor has not re-minted it yet; the gateway is validating against a "+
					"DIFFERENT database than the one the PAT was minted into (compare the "+
					"gateway's DATABASE_URL with the minting service's); or this identity's "+
					"token was revoked.",
					"error", err,
					"code", connect.CodeOf(err).String(),
					"delay", delay,
					"grpc_url", d.bootCfg.GRPCURL,
				)
			} else {
				logging.Error(logPrefix+" Session ended; reconnecting",
					"error", err,
					"code", connect.CodeOf(err).String(),
					"delay", delay,
					"grpc_url", d.bootCfg.GRPCURL,
				)
			}
		}

		// Reset backoff after a session that lasted long enough to be considered
		// successful (e.g. it stayed connected for at least 30 seconds).
		if time.Since(sessionStart) >= 30*time.Second {
			delay = transport.ReconnectMinDelay
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			d.stopAllStreams()
			return ctx.Err()
		case <-timer.C:
		}

		delay *= 2
		if delay > transport.ReconnectMaxDelay {
			delay = transport.ReconnectMaxDelay
		}
	}
}

func (d *daemonClient) runSession(ctx context.Context) error {
	baseURL := d.gatewayURL

	logging.Info(logPrefix+" Connecting to gateway", "url", baseURL)

	stream := d.gatewayClient.ConnectDaemon(ctx)

	// Cancelling ctx does NOT unblock the stream.Receive() below, so shutdown
	// has to close the read side by hand.
	//
	// Receive bottoms out in http2's transportResponseBody.Read, which is a
	// sync.Cond wait on the stream's pipe. That cond is woken by exactly two
	// things: bytes arriving from the peer, or the body being closed. A
	// cancelled context is neither. connect checks ctx.Err() once, *before*
	// entering the read (duplex_http_call.go:236), which is no help to a read
	// that is already parked — and a daemon parked at awaiting_credentials is
	// always already parked, because an idle gateway has nothing to say.
	//
	// Measured 2026-08-26 with the real binary against a silent gateway:
	// SIGTERM to exit was 10.004s WITHOUT this close and 6.8ms with it. The
	// 10s is not the teardown finishing — it is daemonShutdownGrace giving up
	// and force-exiting (exit code 1), because against a peer that never
	// speaks the receive has no upper bound at all. The dev-stack log shows
	// both shapes: a 4.54s exit-0 when the transport eventually noticed, and
	// a 10.005s exit-1 when it did not.
	//
	// Scoped to this session: the goroutine exits with runSession, so a
	// reconnecting daemon does not accumulate one per session.
	sessionOver := make(chan struct{})
	defer close(sessionOver)
	go func() {
		select {
		case <-ctx.Done():
			// Non-blocking, and safe against the send half: CloseResponse
			// touches only the response body, never the marshaler or request
			// pipe that runSender is writing into.
			_ = stream.CloseResponse()
		case <-sessionOver:
		}
	}()

	// --- Registration: send directly before starting the sender goroutine ---
	// daemon_id and user_id are no longer self-asserted; the gateway derives
	// both from the PAT used to authenticate the stream and returns them in
	// the RegistrationAck.
	register := &reliantv1.DaemonMessage{
		Message: &reliantv1.DaemonMessage_Register{Register: &reliantv1.DaemonRegister{
			Hostname:     d.hostname,
			Platform:     d.platform,
			WorkingDir:   d.cwd,
			Capabilities: d.capabilities,
			Name:         d.daemonName,
			// Dialing out no longer implies a personal machine: managed
			// workspaces dial out too under DAEMON_DIAL_OUT. See
			// resolveDaemonType.
			DaemonType: resolveDaemonType("local"),
			Labels:     d.registerLabels(),
			// Re-assert our persisted stable identity (empty on first-ever
			// registration). The gateway trusts it for unbound PATs so identity
			// survives restarts and hostname changes instead of being re-derived
			// from the (volatile) hostname.
			DaemonId: d.daemonID,
		}},
	}
	if err := stream.Send(register); err != nil {
		return fmt.Errorf("sending daemon registration to %s: %w", baseURL, err)
	}

	// --- Start the send channel + single writer goroutine ---
	d.sendCh = make(chan *reliantv1.DaemonMessage, 256)
	d.sendDone = make(chan struct{})
	d.sessionDone = make(chan struct{})
	go d.runSender(stream)
	defer d.drainSender(senderDrainTimeout)

	if err := d.sendProjectDiscovery(); err != nil {
		logging.Warn(logPrefix+" Failed to send project discovery", "error", err)
	}

	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat()
	go d.runHeartbeats(heartbeatCtx)

	for {
		msg, err := stream.Receive()
		if err != nil {
			// A cancelled daemon closed its own read side to get here (see the
			// CloseResponse goroutine above), so Receive reports a closed body
			// rather than a cancellation. Report what actually happened:
			// otherwise every ordinary shutdown logs "Session ended;
			// reconnecting" with a torn-stream error and records the stream
			// DISCONNECTED on its way out, which reads as a fault in the logs
			// and in `reliant daemon status`. run() and Start() both key off
			// context.Canceled to stay quiet.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("daemon stream receive: %w", err)
		}
		if msg == nil {
			continue
		}
		if err := d.handleServerMessage(ctx, msg); err != nil {
			logging.Warn(logPrefix+" Failed handling server message", "error", err)
		}
	}
}

func (d *daemonClient) handleServerMessage(ctx context.Context, msg *reliantv1.ServerMessage) error {
	switch m := msg.Message.(type) {
	case *reliantv1.ServerMessage_ToolRequest:
		if m.ToolRequest == nil {
			return nil
		}
		go d.executeTool(m.ToolRequest)
		return nil

	case *reliantv1.ServerMessage_ToolCancel:
		if m.ToolCancel == nil {
			return nil
		}
		d.cancelToolExecution(m.ToolCancel.RequestId)
		return nil

	case *reliantv1.ServerMessage_ToolBackground:
		if m.ToolBackground == nil {
			return nil
		}
		d.markToolExecutionBackgrounded(m.ToolBackground.RequestId, m.ToolBackground.ToolCallId)
		return nil

	case *reliantv1.ServerMessage_Heartbeat:
		return nil

	case *reliantv1.ServerMessage_RegistrationAck:
		if m.RegistrationAck != nil {
			// The ack is the only unambiguous "the gateway stream is up" signal
			// the daemon gets: the gateway sends it after it has accepted the
			// registration and bound the connection. Record it — a successful
			// local Send() proves nothing about the far end.
			d.recordStream(daemonstate.StreamConnected, "")

			// Gateway derives both identities from the PAT used to authenticate
			// the stream and tells us. Daemon stores them locally for downstream
			// use (tool execution context, logging).
			if m.RegistrationAck.DaemonId != "" {
				d.daemonID = m.RegistrationAck.DaemonId
				// Publish to the process-wide identity so static command handlers
				// (e.g. exec.bg_list) can stamp ProcessInfo.DaemonID for preview URLs.
				SetDaemonIdentity(d.daemonID)
				logging.Info(logPrefix+" Daemon identity assigned by gateway", "daemonID", d.daemonID)
				// Persist the assigned id per-origin so it survives restarts and
				// hostname changes; next reconnect re-asserts it in Register.
				d.persistDaemonID(m.RegistrationAck.DaemonId)
			}
			if m.RegistrationAck.UserId != "" {
				d.userID = m.RegistrationAck.UserId
			}
			for _, projectPath := range m.RegistrationAck.RequestedProjectPaths {
				if err := d.sendLoadProjectConfigResponse(projectPath, uuid.New().String()); err != nil {
					logging.Warn(logPrefix+" Failed responding to registration requested project load", "projectPath", projectPath, "error", err)
				}
				d.startProjectWatcher(ctx, projectPath, true)
			}
		}
		return nil

	case *reliantv1.ServerMessage_LoadProjectConfigs:
		if m.LoadProjectConfigs == nil {
			return nil
		}
		// Dispatch async like DaemonCommand/KillProcess above. Building the
		// response runs full skill/workflow/preset discovery (including forge
		// skill enumeration via forgecli) and can take tens of seconds on a
		// large project. Handling it inline wedged the stream RECEIVE loop:
		// no DaemonCommand could even be dispatched until discovery finished,
		// so every daemon-routed RPC timed out ("nats: timeout") and the
		// stalled stream eventually died with EOF, retriggering the same
		// LoadProjectConfigs on reconnect — a self-sustaining outage loop
		// (2026-07-09 incident).
		go func(req *reliantv1.LoadProjectConfigsRequest) {
			if err := d.sendLoadProjectConfigResponse(req.ProjectPath, req.RequestId); err != nil {
				logging.Warn(logPrefix+" Failed to send project config response",
					"projectPath", req.ProjectPath, "requestID", req.RequestId, "error", err)
			}
		}(m.LoadProjectConfigs)
		return nil

	case *reliantv1.ServerMessage_WatchProjectConfigs:
		if m.WatchProjectConfigs == nil {
			return nil
		}
		d.startProjectWatcher(ctx, m.WatchProjectConfigs.ProjectPath, m.WatchProjectConfigs.IncludeInitial)
		return nil

	case *reliantv1.ServerMessage_UnwatchProjectConfigs:
		if m.UnwatchProjectConfigs == nil {
			return nil
		}
		d.stopProjectWatcher(m.UnwatchProjectConfigs.ProjectPath)
		return nil

	case *reliantv1.ServerMessage_KillProcess:
		if m.KillProcess == nil {
			return nil
		}
		go d.handleKillProcess(m.KillProcess)
		return nil

	case *reliantv1.ServerMessage_DaemonCommand:
		if m.DaemonCommand == nil {
			return nil
		}
		go d.handleDaemonCommand(m.DaemonCommand)
		return nil

	case *reliantv1.ServerMessage_TerminalInput:
		if m.TerminalInput == nil {
			return nil
		}
		d.handleTerminalInput(m.TerminalInput)
		return nil

	case *reliantv1.ServerMessage_TerminalResize:
		if m.TerminalResize == nil {
			return nil
		}
		d.handleTerminalResize(m.TerminalResize)
		return nil

	case *reliantv1.ServerMessage_TerminalOutputSubscribe:
		if m.TerminalOutputSubscribe == nil {
			return nil
		}
		// Subscribe-driven: this is what starts the PTY output pump (mirrors the
		// process-output subscribe case below). The PTY buffered its initial
		// shell prompt until now, so the prompt is delivered once the pump reads.
		if sid := m.TerminalOutputSubscribe.GetSessionId(); sid != "" {
			d.startTerminalOutputPump(sid)
		}
		return nil

	case *reliantv1.ServerMessage_ProcessOutputSubscribe:
		if m.ProcessOutputSubscribe == nil {
			return nil
		}
		d.handleProcessOutputSubscribe(m.ProcessOutputSubscribe)
		return nil

	case *reliantv1.ServerMessage_ProcessOutputUnsubscribe:
		if m.ProcessOutputUnsubscribe == nil {
			return nil
		}
		d.handleProcessOutputUnsubscribe(m.ProcessOutputUnsubscribe)
		return nil

	default:
		return nil
	}
}

func (d *daemonClient) handleKillProcess(req *reliantv1.DaemonKillProcessRequest) {
	bgManager := shell.GetBackgroundManager()
	err := bgManager.KillProcess(req.ProcessId)

	resp := &reliantv1.DaemonMessage{
		Message: &reliantv1.DaemonMessage_KillProcessResponse{
			KillProcessResponse: &reliantv1.DaemonKillProcessResponse{
				ProcessId: req.ProcessId,
				Success:   err == nil,
			},
		},
	}
	if err != nil {
		resp.GetKillProcessResponse().ErrorMessage = err.Error()
	}
	if sendErr := d.send(resp); sendErr != nil {
		logging.Warn(logPrefix+" Failed to send kill process response", "processID", req.ProcessId, "error", sendErr)
	}
}

func (d *daemonClient) handleDaemonCommand(req *reliantv1.DaemonCommandRequest) {
	ctx, cancel := context.WithCancel(context.Background())
	if req.TimeoutMs > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutMs)*time.Millisecond)
	}
	defer cancel()
	if strings.TrimSpace(req.RequestId) != "" {
		d.registerCancel(req.RequestId, cancel)
		defer d.unregisterCancel(req.RequestId)
	}

	// Connector policy gate. Requests from third-party MCP clients carry a
	// policy confining which commands they may run and which paths they may
	// touch; first-party requests carry none and are unaffected.
	//
	// This runs here, at the single point every command passes through, rather
	// than in the caller that built the request — so a future caller that
	// speaks this protocol cannot skip it by construction.
	policy := daemonpolicy.FromProto(req.GetPolicy())
	if err := policy.Check(req.CommandType, req.Payload); err != nil {
		logging.Warn(logPrefix+" daemon command denied by connector policy",
			"commandType", req.CommandType, "requestID", req.RequestId,
			"grantID", policy.GrantIDForLog(), "error", err)
		d.sendCommandFailure(req, err)
		return
	}
	ctx = daemonpolicy.NewContext(ctx, policy)

	// Let a long-running exec detach itself if the user asks. Keyed by request
	// id — the same correlation cancellation uses, and the only handle the
	// daemon has on a running execution.
	requestID := req.RequestId
	ctx = WithBackgroundProbe(ctx, func() (string, bool) {
		return d.takeBackgroundRequest(requestID)
	})

	// Watchdog: daemon commands can wedge on network calls (the 2026-07-09
	// incident was worktree.git_changes hanging on a git remote), and the
	// stream response is only sent on completion — without this nothing is
	// logged anywhere while a command hangs.
	const slowCommandThreshold = 10 * time.Second
	start := time.Now()
	watchdog := time.AfterFunc(slowCommandThreshold, func() {
		logging.Warn(logPrefix+" daemon command still running",
			"commandType", req.CommandType, "requestID", req.RequestId)
	})

	resultPayload, err := defaultRegistry.Handle(ctx, req.CommandType, req.Payload)

	watchdog.Stop()
	if elapsed := time.Since(start); elapsed >= slowCommandThreshold {
		logging.Warn(logPrefix+" daemon command completed slowly",
			"commandType", req.CommandType, "requestID", req.RequestId,
			"elapsed", elapsed.Round(time.Millisecond).String(),
			"failed", err != nil)
	}

	resp := &reliantv1.DaemonMessage{
		Message: &reliantv1.DaemonMessage_DaemonCommandResponse{
			DaemonCommandResponse: &reliantv1.DaemonCommandResponse{
				RequestId:   req.RequestId,
				CommandType: req.CommandType,
				Success:     err == nil,
				Payload:     resultPayload,
			},
		},
	}
	if err != nil {
		resp.GetDaemonCommandResponse().ErrorMessage = err.Error()
	}
	if sendErr := d.send(resp); sendErr != nil {
		logging.Warn(logPrefix+" Failed to send daemon command response",
			"requestID", req.RequestId, "commandType", req.CommandType, "error", sendErr)
	}

	d.announceFilesystemMutation(req.CommandType, resultPayload, err)

	// NOTE: the terminal output pump is NOT started here on terminal.create.
	// It is subscribe-driven — started only when a TerminalOutputSubscribeMessage
	// arrives (see handleServerMessage), mirroring the process-output subscribe
	// flow. Until then the PTY buffers its initial shell prompt, so the prompt
	// cannot be drained before a subscriber's interest chain is established.
}

// filesystemMutatingCommands are daemon commands that create or remove a
// project directory outright.
//
// The file-tree watcher (fs_watcher.go) only polls paths it already knows
// about, so a clone into a brand-new directory produces no change signal at
// all. That matters most for `git.clone`, which the control-plane issues on
// this same subject and whose caller may well be gone by the time it lands:
// the clone is a 10-60s NATS request/reply, and a client that backgrounds
// mid-flight (a phone) never learns it succeeded even though the repo is on
// disk. Announcing here turns the outcome into a `user_updates` row, which
// replays on reconnect.
var filesystemMutatingCommands = map[string]bool{
	"git.clone":   true,
	"git.reclone": true,
	"git.remove":  true,
}

// announceFilesystemMutation tells the server a command changed the shape of
// the filesystem, so it can emit a refetch for clients that were not listening.
//
// Best-effort and non-blocking by design: the command response has already
// been sent, and failing to announce must never turn a successful clone into
// a reported failure.
func (d *daemonClient) announceFilesystemMutation(commandType string, resultPayload []byte, cmdErr error) {
	if cmdErr != nil || !filesystemMutatingCommands[commandType] {
		return
	}

	// The handlers report where they landed, which is the path the server
	// needs to resolve a project. Without it there is nothing to scope the
	// refetch to.
	var result struct {
		Path string `json:"path"`
	}
	if len(resultPayload) > 0 {
		_ = json.Unmarshal(resultPayload, &result)
	}
	if result.Path == "" {
		return
	}

	if err := d.send(&reliantv1.DaemonMessage{
		Message: &reliantv1.DaemonMessage_FileSystemChanged{
			FileSystemChanged: &reliantv1.FileSystemChanged{
				ProjectPath:     result.Path,
				TimestampUnixMs: time.Now().UTC().UnixMilli(),
			},
		},
	}); err != nil {
		logging.Warn(logPrefix+" Failed to announce filesystem mutation",
			"commandType", commandType, "path", result.Path, "error", err)
	}
}

// sendCommandFailure reports a command that never ran. Refusals must still
// answer on the stream: the caller is blocked on a reply, and dropping it
// would surface as a timeout rather than as the denial it is.
func (d *daemonClient) sendCommandFailure(req *reliantv1.DaemonCommandRequest, cause error) {
	resp := &reliantv1.DaemonMessage{
		Message: &reliantv1.DaemonMessage_DaemonCommandResponse{
			DaemonCommandResponse: &reliantv1.DaemonCommandResponse{
				RequestId:    req.RequestId,
				CommandType:  req.CommandType,
				Success:      false,
				ErrorMessage: cause.Error(),
			},
		},
	}
	if sendErr := d.send(resp); sendErr != nil {
		logging.Warn(logPrefix+" Failed to send daemon command failure",
			"requestID", req.RequestId, "commandType", req.CommandType, "error", sendErr)
	}
}

func (d *daemonClient) runHeartbeats(ctx context.Context) {
	ticker := time.NewTicker(transport.DaemonHeartbeatInterval)
	defer ticker.Stop()

	sendHeartbeat := func(now time.Time) error {
		hb := &reliantv1.DaemonHeartbeat{Timestamp: now.UnixMilli()}
		// Piggyback workspace memory telemetry when available (cloud daemons
		// in a cgroup-limited pod). Fields stay zero on hosts without cgroup
		// accounting — the gateway treats limit==0 as "not reported".
		if sample, ok := d.memWatcher.Latest(); ok {
			hb.MemoryUsedBytes = sample.UsedBytes
			hb.MemoryLimitBytes = sample.LimitBytes
			hb.MemoryPressure = sample.Pressure
		}
		// Piggyback detected listener ports (pod-hosted daemons only; the
		// watcher is nil-safe and ok stays false until the first scan).
		if ports, ok := d.portWatcher.Latest(); ok {
			hb.DetectedPorts = ports
		}
		return d.send(&reliantv1.DaemonMessage{
			Message: &reliantv1.DaemonMessage_Heartbeat{Heartbeat: hb},
		})
	}

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := sendHeartbeat(now); err != nil {
				logging.Warn(logPrefix+" Failed to send heartbeat", "error", err)
				return
			}
		case <-d.memWatcher.Changed():
			// Pressure flipped — tell the gateway now instead of waiting up
			// to a full heartbeat interval. Nil-safe: a nil channel (no
			// watcher) never fires.
			if err := sendHeartbeat(time.Now()); err != nil {
				logging.Warn(logPrefix+" Failed to send pressure heartbeat", "error", err)
				return
			}
		case <-d.portWatcher.Changed():
			// Listener set changed — surface the new port (or its
			// disappearance) immediately so the UI preview affordance tracks
			// dev-server starts without a heartbeat-interval lag.
			if err := sendHeartbeat(time.Now()); err != nil {
				logging.Warn(logPrefix+" Failed to send port-change heartbeat", "error", err)
				return
			}
		}
	}
}

func (d *daemonClient) executeTool(req *reliantv1.ToolRequest) {
	if req == nil {
		return
	}

	execCtx, cancel := context.WithCancel(context.Background())
	// Registered under both ids. request_id is the transport's correlation key
	// (a fresh timestamp per dispatch, known only to the server that sent it);
	// tool_call_id is the LLM's identifier, and the only one a user cancelling
	// a specific tool from the UI can name. Cancelling one tool must reach
	// exactly that execution -- the alternative was cancelling the whole chat
	// workflow, which took every sibling tool down with it.
	d.registerCancel(req.RequestId, cancel)
	defer d.unregisterCancel(req.RequestId)
	if req.ToolCallId != req.RequestId {
		d.registerCancel(req.ToolCallId, cancel)
		defer d.unregisterCancel(req.ToolCallId)
	}
	defer cancel()

	// Backgrounding is addressed the same way, and for the same reason: the
	// only id a user can name from the UI is the tool call's. Probe both so a
	// request that arrives under either id reaches this execution.
	execCtx = WithBackgroundProbe(execCtx, func() (string, bool) {
		if toolCallID, ok := d.takeBackgroundRequest(req.RequestId); ok {
			return toolCallID, true
		}
		return d.takeBackgroundRequest(req.ToolCallId)
	})

	contextMap := map[string]interface{}{}
	if strings.TrimSpace(req.ContextJson) != "" {
		if err := json.Unmarshal([]byte(req.ContextJson), &contextMap); err != nil {
			contextMap = map[string]interface{}{}
		}
	}
	if contextMap["user_id"] == nil {
		contextMap["user_id"] = d.userID
	}

	projectPath := ""
	if isMCPToolName(req.ToolName) {
		projectPath = d.ensureMCPServersLoadedForRequest(execCtx, req, contextMap)
	}

	result := d.localExecutor.ExecuteTool(
		execCtx,
		req.ToolName,
		req.ToolInput,
		req.ToolCallId,
		int(req.TimeoutMs),
		contextMap,
	)

	if isMCPToolName(req.ToolName) && isMCPAuthError(result) {
		serverName := parseMCPServerName(req.ToolName)
		if serverName != "" {
			if projectPath == "" {
				projectPath = resolveProjectPathFromContext(contextMap, "", d.cwd)
			}
			if projectPath != "" {
				if err := d.mcpManager.RemoveProjectServer(projectPath, serverName); err != nil {
					logging.Warn(logPrefix+" Failed removing MCP server before auth retry", "projectPath", projectPath, "server", serverName, "error", err)
				}
				d.mcpManager.EnsureProjectServersLoaded(execCtx, projectPath)
			}
			result = d.localExecutor.ExecuteTool(
				execCtx,
				req.ToolName,
				req.ToolInput,
				req.ToolCallId,
				int(req.TimeoutMs),
				contextMap,
			)
		}
	}

	if execCtx.Err() != nil {
		result = &toolexec.ExecutionResult{
			Success:      false,
			IsError:      true,
			Content:      "Tool execution cancelled",
			ErrorMessage: execCtx.Err().Error(),
			ErrorCode:    "CANCELLED",
		}
	}

	if result == nil {
		result = &toolexec.ExecutionResult{
			Success:      false,
			IsError:      true,
			Content:      "Tool execution failed: nil result",
			ErrorMessage: "nil result",
			ErrorCode:    "EXECUTION_ERROR",
		}
	}

	// Sanitize all string fields to valid UTF-8 before protobuf serialization.
	// Protobuf3 string fields reject invalid UTF-8, which would silently drop
	// the response and leave the server-side poller waiting until timeout.
	result.Content = strings.ToValidUTF8(result.Content, "\uFFFD")
	result.Metadata = strings.ToValidUTF8(result.Metadata, "\uFFFD")
	result.ErrorMessage = strings.ToValidUTF8(result.ErrorMessage, "\uFFFD")

	resp := &reliantv1.DaemonMessage{
		Message: &reliantv1.DaemonMessage_ToolResponse{ToolResponse: &reliantv1.ToolResponse{
			RequestId:    req.RequestId,
			Success:      result.Success,
			IsError:      result.IsError,
			Content:      result.Content,
			Metadata:     result.Metadata,
			ErrorMessage: result.ErrorMessage,
			ErrorCode:    result.ErrorCode,
			Backgrounded: result.Backgrounded,
		}},
	}
	if err := d.send(resp); err != nil {
		logging.Warn(logPrefix+" Failed to send tool response via stream", "requestID", req.RequestId, "error", err)
	}
}

// send enqueues a message for the single runSender goroutine to write.
// It returns an error only if the session is shutting down.
func (d *daemonClient) send(msg *reliantv1.DaemonMessage) error {
	select {
	case d.sendCh <- msg:
		return nil
	case <-d.sessionDone:
		return fmt.Errorf("daemon session ended")
	}
}

// messageSender is the write half of the gateway stream. It is an interface so
// session teardown can be tested against a sender whose Send never returns —
// the condition that makes an unbounded drain wedge the whole process.
type messageSender interface {
	Send(*reliantv1.DaemonMessage) error
}

// runSender is the single goroutine that drains sendCh and writes to the
// bidi stream. This serialises writes (required — stream.Send is not
// thread-safe) without blocking producers on I/O.
func (d *daemonClient) runSender(stream messageSender) {
	defer close(d.sendDone)
	for {
		select {
		case msg := <-d.sendCh:
			if msg == nil {
				return
			}
			if err := stream.Send(msg); err != nil {
				logging.Warn(logPrefix+" runSender: stream.Send failed", "error", err)
				return
			}
		case <-d.sessionDone:
			return
		}
	}
}

// senderDrainTimeout bounds how long session teardown waits for runSender.
const senderDrainTimeout = 3 * time.Second

// drainSender ends the session and waits — with a deadline — for runSender to
// stop. It reports whether the sender actually stopped.
//
// The deadline is the point. stream.Send writes into an io.Pipe that feeds the
// HTTP/2 request body, so it blocks whenever the peer stops reading: a gateway
// that has evicted this connection, or one whose flow-control window is full.
// runSender is then inside Send and cannot reach its `case <-sessionDone`, so
// an unbounded wait here never returns — and it sits between SIGTERM and
// process exit, which is how a daemon takes SIGTERM, prints "Shutting down",
// and then lives forever.
//
// Abandoning the goroutine is safe: the session is over, its stream is being
// discarded, and the process is on its way out.
func (d *daemonClient) drainSender(timeout time.Duration) bool {
	close(d.sessionDone) // signal send() and runSender to stop
	select {
	case <-d.sendDone:
		return true
	case <-time.After(timeout):
		logging.Warn(logPrefix+" sender did not stop within the drain deadline; abandoning it",
			"timeout", timeout.String())
		return false
	}
}

func (d *daemonClient) registerCancel(requestID string, cancel context.CancelFunc) {
	if strings.TrimSpace(requestID) == "" || cancel == nil {
		return
	}
	d.cancelMu.Lock()
	defer d.cancelMu.Unlock()
	d.cancelByReq[requestID] = cancel
}

func (d *daemonClient) unregisterCancel(requestID string) {
	if strings.TrimSpace(requestID) == "" {
		return
	}
	d.cancelMu.Lock()
	defer d.cancelMu.Unlock()
	delete(d.cancelByReq, requestID)
	delete(d.backgroundByReq, requestID)
}

// markToolExecutionBackgrounded records that the user asked to detach this
// in-flight execution. The running command polls for it and adopts itself into
// the background manager; a request for an execution that has already finished
// is harmless — nothing reads the entry and unregisterCancel clears it.
func (d *daemonClient) markToolExecutionBackgrounded(requestID, toolCallID string) {
	if strings.TrimSpace(requestID) == "" {
		return
	}
	d.cancelMu.Lock()
	defer d.cancelMu.Unlock()
	d.backgroundByReq[requestID] = toolCallID
}

// takeBackgroundRequest reports whether this execution was asked to background,
// consuming the request so it fires exactly once.
func (d *daemonClient) takeBackgroundRequest(requestID string) (string, bool) {
	if strings.TrimSpace(requestID) == "" {
		return "", false
	}
	d.cancelMu.Lock()
	defer d.cancelMu.Unlock()
	toolCallID, ok := d.backgroundByReq[requestID]
	if ok {
		delete(d.backgroundByReq, requestID)
	}
	return toolCallID, ok
}

func (d *daemonClient) cancelToolExecution(requestID string) {
	if strings.TrimSpace(requestID) == "" {
		return
	}
	d.cancelMu.Lock()
	cancel := d.cancelByReq[requestID]
	d.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (d *daemonClient) sendProjectDiscovery() error {
	projectPath := strings.TrimSpace(os.Getenv("PROJECT_PATH"))
	if projectPath == "" {
		projectPath = d.cwd
	}
	projectPath = normalizePath(projectPath)
	if projectPath == "" {
		projectPath = normalizePath(defaultProjectScanPath)
	}

	// Only discover as a project if it contains a .git directory.
	// This prevents generic directories (like a user's home dir) from being
	// registered as projects when d.cwd is used as a fallback.
	if !pathLooksLikeGitRepo(projectPath) {
		slog.Debug("[ToolsDaemon] Skipping project discovery — no .git found", "path", projectPath)
		return nil
	}

	projectName := filepath.Base(projectPath)
	if projectName == "." || projectName == string(filepath.Separator) || projectName == "" {
		projectName = "project"
	}

	msg := &reliantv1.DaemonMessage{
		Message: &reliantv1.DaemonMessage_ProjectDiscovery{ProjectDiscovery: &reliantv1.ProjectDiscovery{
			Projects: []*reliantv1.DiscoveredProject{{
				Path:      projectPath,
				Name:      projectName,
				IsGitRepo: true,
			}},
		}},
	}
	return d.send(msg)
}

func pathLooksLikeGitRepo(projectPath string) bool {
	if projectPath == "" {
		return false
	}
	// Direct .git at root: single-repo checkout or worktree.
	if _, err := os.Stat(filepath.Join(projectPath, ".git")); err == nil {
		return true
	}
	// Multi-repo root: no .git here, but immediate children may have one.
	// A shallow scan (depth 1) avoids walking large trees.
	entries, err := os.ReadDir(projectPath)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(projectPath, e.Name(), ".git")); err == nil {
			return true
		}
	}
	return false
}

func (d *daemonClient) sendLoadProjectConfigResponse(projectPath, requestID string) error {
	snapshot, err := buildProjectSnapshot(projectPath)
	resp := &reliantv1.LoadProjectConfigsResponse{
		RequestId: requestID,
		Snapshot:  snapshot,
	}
	if err != nil {
		resp.Error = err.Error()
	}

	msg := &reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_LoadProjectConfigsResponse{LoadProjectConfigsResponse: resp}}
	return d.send(msg)
}

func (d *daemonClient) startProjectWatcher(ctx context.Context, projectPath string, includeInitial bool) {
	projectPath = normalizePath(projectPath)
	if projectPath == "" {
		return
	}

	d.watchersMu.Lock()
	if existing := d.watchersByPr[projectPath]; existing != nil {
		existing()
	}
	watchCtx, cancel := context.WithCancel(ctx)
	d.watchersByPr[projectPath] = cancel
	d.watchersMu.Unlock()

	go d.runProjectWatcher(watchCtx, projectPath, includeInitial)
	d.startFileTreeWatcher(ctx, projectPath)
}

func (d *daemonClient) stopProjectWatcher(projectPath string) {
	projectPath = normalizePath(projectPath)
	d.watchersMu.Lock()
	defer d.watchersMu.Unlock()
	if cancel := d.watchersByPr[projectPath]; cancel != nil {
		cancel()
	}
	delete(d.watchersByPr, projectPath)
	d.stopFileTreeWatcher(projectPath)
}

func (d *daemonClient) stopAllWatchers() {
	d.watchersMu.Lock()
	defer d.watchersMu.Unlock()
	for projectPath, cancel := range d.watchersByPr {
		if cancel != nil {
			cancel()
		}
		delete(d.watchersByPr, projectPath)
	}
}

// stopAllStreams stops all watchers, terminal output pumps, and process
// output subscriptions. Called on disconnect / context cancellation.
func (d *daemonClient) stopAllStreams() {
	d.stopAllWatchers()
	d.stopAllFileTreeWatchers()
	d.terminalPumps.stopAll()
	d.processOutputSubs.stopAll()
	if tm := terminalManager(); tm != nil {
		tm.Cleanup()
	}
}

func (d *daemonClient) startFileTreeWatcher(ctx context.Context, projectPath string) {
	projectPath = normalizePath(projectPath)
	if projectPath == "" {
		return
	}

	d.fsWatchersMu.Lock()
	if existing := d.fsWatchersByPr[projectPath]; existing != nil {
		existing()
	}
	watchCtx, cancel := context.WithCancel(ctx)
	d.fsWatchersByPr[projectPath] = cancel
	d.fsWatchersMu.Unlock()

	go d.runFileTreeWatcher(watchCtx, projectPath)
}

func (d *daemonClient) stopFileTreeWatcher(projectPath string) {
	projectPath = normalizePath(projectPath)
	d.fsWatchersMu.Lock()
	defer d.fsWatchersMu.Unlock()
	if cancel := d.fsWatchersByPr[projectPath]; cancel != nil {
		cancel()
	}
	delete(d.fsWatchersByPr, projectPath)
}

func (d *daemonClient) stopAllFileTreeWatchers() {
	d.fsWatchersMu.Lock()
	defer d.fsWatchersMu.Unlock()
	for projectPath, cancel := range d.fsWatchersByPr {
		if cancel != nil {
			cancel()
		}
		delete(d.fsWatchersByPr, projectPath)
	}
}

func (d *daemonClient) runProjectWatcher(ctx context.Context, projectPath string, includeInitial bool) {
	var lastVersion string

	sendSnapshotDelta := func() {
		snapshot, err := buildProjectSnapshot(projectPath)
		if err != nil || snapshot == nil {
			if err != nil {
				logging.Warn(logPrefix+" Failed to build project snapshot for watcher", "projectPath", projectPath, "error", err)
			}
			return
		}
		if snapshot.ConfigVersion == lastVersion {
			return
		}
		lastVersion = snapshot.ConfigVersion

		delta := &reliantv1.ProjectConfigDelta{
			ProjectPath:           projectPath,
			ConfigVersion:         snapshot.ConfigVersion,
			DaemonTimestampUnixMs: time.Now().UTC().UnixMilli(),
			ChangedFiles:          []*reliantv1.ChangedFile{},
			SnapshotIfCompacted:   snapshot,
		}
		msg := &reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_ProjectConfigDelta{ProjectConfigDelta: delta}}
		if err := d.send(msg); err != nil {
			logging.Warn(logPrefix+" Failed to send project config delta", "projectPath", projectPath, "error", err)
		}
	}

	if includeInitial {
		sendSnapshotDelta()
	}

	ticker := time.NewTicker(transport.WatchPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sendSnapshotDelta()
		}
	}
}

func buildProjectSnapshot(projectPath string) (*reliantv1.ProjectConfigSnapshot, error) {
	projectPath = normalizePath(projectPath)
	if projectPath == "" {
		return nil, fmt.Errorf("project path is empty")
	}

	userConfigDir := config.GetUserConfigDir()
	userConfigPath := filepath.Join(userConfigDir, "config.yaml")
	projectConfigPath := filepath.Join(projectPath, ".reliant", "config.yaml")
	localConfigPath := filepath.Join(projectPath, ".reliant.local", "config.yaml")
	userMCPPath := filepath.Join(userConfigDir, "mcp.json")
	projectMCPPath := filepath.Join(projectPath, ".reliant", "mcp.json")
	localMCPPath := filepath.Join(projectPath, ".reliant.local", "mcp.json")
	globalMemoryPath := filepath.Join(userConfigDir, "reliant.md")
	projectMemoryPath := filepath.Join(projectPath, "reliant.md")

	userConfigYAML, _ := readOptionalFile(userConfigPath)
	projectConfigYAML, _ := readOptionalFile(projectConfigPath)
	localConfigYAML, _ := readOptionalFile(localConfigPath)
	userMCP, _ := readOptionalFile(userMCPPath)
	projectMCP, _ := readOptionalFile(projectMCPPath)
	localMCP, _ := readOptionalFile(localMCPPath)
	globalMemory, _ := readOptionalFile(globalMemoryPath)
	projectMemory, _ := readOptionalFile(projectMemoryPath)
	// Inject the forge framework cheat-sheet (architecture, proto
	// rules, "use forge skills" callout) when the project is a forge
	// project — forge skips writing a top-level reliant.md so the
	// only authoritative source is the embedded template, rendered
	// here. For non-forge projects this is a no-op.
	projectMemory = projectMemoryWithForgeFramework(projectPath, projectMemory)

	workflows, workflowBytes := indexWorkflows(projectPath)
	presets, presetBytes := indexPresets(projectPath)
	scenarios, scenarioBytes := indexScenarios(projectPath)
	skills, skillBytes := indexSkills(projectPath)
	repoMemories, repoMemoriesBytes := collectRepoMemories(projectPath)

	version := hashBytes(
		userConfigYAML,
		projectConfigYAML,
		localConfigYAML,
		userMCP,
		projectMCP,
		localMCP,
		globalMemory,
		projectMemory,
		workflowBytes,
		presetBytes,
		scenarioBytes,
		skillBytes,
		repoMemoriesBytes,
	)

	return &reliantv1.ProjectConfigSnapshot{
		ProjectPath:           projectPath,
		ConfigVersion:         version,
		DaemonTimestampUnixMs: time.Now().UTC().UnixMilli(),
		UserConfigYaml:        userConfigYAML,
		ProjectConfigYaml:     projectConfigYAML,
		LocalConfigYaml:       localConfigYAML,
		McpConfigs: map[string][]byte{
			"user":    userMCP,
			"project": projectMCP,
			"local":   localMCP,
		},
		Workflows:       workflows,
		Presets:         presets,
		Scenarios:       scenarios,
		Skills:          skills,
		GlobalMemoryMd:  globalMemory,
		ProjectMemoryMd: projectMemory,
		RepoMemoriesMd:  repoMemories,
	}, nil
}

// collectRepoMemories reads reliant.md and reliant.local.md for each nested
// repo and returns a map[repoRelPath]->content suitable for the proto snapshot.
// The second return value is a deterministic byte summary for version hashing.
func collectRepoMemories(projectPath string) (map[string][]byte, []byte) {
	repoSources := discoverRepoSources(context.Background(), projectPath)
	if len(repoSources) == 0 {
		return nil, nil
	}

	result := make(map[string][]byte, len(repoSources))
	acc := strings.Builder{}

	for _, rel := range repoSources {
		if rel == "" {
			continue
		}
		repoDir := filepath.Join(projectPath, rel)
		var parts []string
		md, _ := readOptionalFile(filepath.Join(repoDir, "reliant.md"))
		// Inject forge framework memory for nested forge repos — same
		// rationale as the top-level case in buildProjectSnapshot. A
		// no-op when repoDir has no forge.yaml.
		md = projectMemoryWithForgeFramework(repoDir, md)
		if len(md) > 0 {
			parts = append(parts, string(md))
		}
		if local, _ := readOptionalFile(filepath.Join(repoDir, "reliant.local.md")); len(local) > 0 {
			parts = append(parts, string(local))
		}
		if len(parts) == 0 {
			continue
		}
		content := strings.Join(parts, "\n\n")
		result[rel] = []byte(content)
		acc.WriteString(rel)
		acc.WriteString(":")
		acc.WriteString(hashBytes([]byte(content)))
		acc.WriteString(";")
	}

	if len(result) == 0 {
		return nil, nil
	}
	return result, []byte(acc.String())
}

// indexSkills discovers all SKILL.md files across the standard roots
// (project-local, project, claude, codex, agents, global, claude/codex global, builtin)
// and returns them as IndexedSkill protos. The body is included so the server-side
// skill tool can render the full skill without additional round trips.
//
// Skill discovery is recursive across nested repos: each discovered repo's
// .reliant/skills, .claude/skills, etc. are scanned in addition to the project
// root's. Each skill carries a Source field identifying which repo it came from
// ("" for project root) so the LLM can disambiguate same-named skills.
// skillsIndexCache memoizes indexSkills per project path. Discovery is by far
// the most expensive part of snapshot building — forge skill enumeration via
// forgecli plus full-definition loads, across the project root AND every repo
// source — and during the 2026-07-09 incident repeated snapshot builds pinned
// the daemon above 150% CPU for minutes. Skills change rarely mid-session; a
// short TTL keeps edits visible without paying discovery on every rebuild.
// Cached slices are shared across callers — treat them as read-only.
var (
	skillsIndexMu    sync.Mutex
	skillsIndexCache = map[string]skillsIndexEntry{}
)

type skillsIndexEntry struct {
	skills  []*reliantv1.IndexedSkill
	blob    []byte
	expires time.Time

	// forgeProject records whether the project had a forge.yaml when this entry
	// was built. It invalidates the entry independently of the TTL — see
	// indexSkills.
	forgeProject bool
}

const skillsIndexTTL = 60 * time.Second

// projectHasForgeYAML reports whether the project root carries a forge.yaml,
// the file that makes forge's framework skills visible at all
// (internal/skills/catalog/forge.go drops every `emit: forge` skill without it).
func projectHasForgeYAML(projectPath string) bool {
	if projectPath == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(projectPath, "forge.yaml"))
	return err == nil
}

func indexSkills(projectPath string) ([]*reliantv1.IndexedSkill, []byte) {
	// A project BECOMING a forge project changes the catalog wholesale — every
	// `forge/*` skill appears (or disappears) at once — and it is a transition a
	// generation workflow makes on purpose, in its first phase, seconds before it
	// is told to work from those very skills. Serving a cached "no forge skills"
	// answer for up to a TTL across that moment means the agent's skill loads
	// 404 and it proceeds from memory instead. Cheap to check: one stat against
	// discovery that walks every skill root in the tree.
	forgeProject := projectHasForgeYAML(projectPath)

	skillsIndexMu.Lock()
	if e, ok := skillsIndexCache[projectPath]; ok && time.Now().Before(e.expires) && e.forgeProject == forgeProject {
		skillsIndexMu.Unlock()
		return e.skills, e.blob
	}
	skillsIndexMu.Unlock()

	skills, blob := buildSkillsIndex(projectPath)

	skillsIndexMu.Lock()
	skillsIndexCache[projectPath] = skillsIndexEntry{
		skills:       skills,
		blob:         blob,
		expires:      time.Now().Add(skillsIndexTTL),
		forgeProject: forgeProject,
	}
	skillsIndexMu.Unlock()
	return skills, blob
}

func buildSkillsIndex(projectPath string) ([]*reliantv1.IndexedSkill, []byte) {
	repoSources := discoverRepoSources(context.Background(), projectPath)
	snapshot := catalog.DiscoverAll(catalog.DiscoverInput{
		ProjectPath:         projectPath,
		LoadFullDefinitions: true,
		RepoSources:         repoSources,
	})

	results := make([]*reliantv1.IndexedSkill, 0, len(snapshot.Definitions))
	acc := strings.Builder{}

	for _, def := range snapshot.Definitions {
		// Compute relative path for project-scoped skills; global / builtin
		// skills stay absolute.
		var relPath string
		if projectPath != "" {
			if rel, err := filepath.Rel(projectPath, def.Path); err == nil && !strings.HasPrefix(rel, "..") {
				relPath = filepath.ToSlash(rel)
			}
		}

		var mtimeMs int64
		if st, err := os.Stat(def.Path); err == nil {
			mtimeMs = st.ModTime().UTC().UnixMilli()
		}

		h := hashBytes([]byte(def.Body), []byte(def.Description), []byte(def.SkillPath))

		userInvocable := ""
		if def.UserInvocable != nil {
			if *def.UserInvocable {
				userInvocable = "true"
			} else {
				userInvocable = "false"
			}
		}

		results = append(results, &reliantv1.IndexedSkill{
			SkillPath:              def.SkillPath,
			Name:                   def.Name,
			Description:            def.Description,
			RelativePath:           relPath,
			ContentHash:            h,
			MtimeUnixMs:            mtimeMs,
			Scope:                  string(def.Scope),
			Body:                   def.Body,
			AllowedTools:           def.AllowedTools,
			Metadata:               def.Metadata,
			HasChildren:            def.HasChildren,
			DisableModelInvocation: def.DisableModelInvocation,
			UserInvocable:          userInvocable,
			ArgumentHint:           def.ArgumentHint,
			Paths:                  def.Paths,
			Source:                 def.Source,
		})

		acc.WriteString(def.SkillPath)
		acc.WriteString(":")
		acc.WriteString(h)
		acc.WriteString(";")
	}

	return results, []byte(acc.String())
}

func indexWorkflows(projectPath string) ([]*reliantv1.IndexedWorkflow, []byte) {
	baseDir := filepath.Join(projectPath, ".reliant", "workflows")
	files := listYAMLFiles(baseDir)
	results := make([]*reliantv1.IndexedWorkflow, 0, len(files))
	acc := strings.Builder{}
	seenSlugs := make(map[string]string) // slug -> file path (for duplicate detection)

	for _, path := range files {
		// Only index top-level YAML files; subdirectory files (e.g. {slug}/scenarios/*.yaml)
		// are handled by indexScenarios.
		relToBase, err := filepath.Rel(baseDir, path)
		if err != nil {
			continue
		}
		if strings.Contains(filepath.ToSlash(relToBase), "/") {
			continue
		}
		rel, err := filepath.Rel(projectPath, path)
		if err != nil {
			continue
		}
		st, err := os.Stat(path)
		if err != nil {
			continue
		}
		blob, _ := os.ReadFile(path)
		filename := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		name := extractNameFromYAML(blob)
		if strings.TrimSpace(name) == "" {
			name = filename
		}
		slug := config.NormalizeSlug(name)

		if existingFile, ok := seenSlugs[slug]; ok {
			logging.Warn("Duplicate workflow name, skipping",
				"slug", slug, "file", filepath.Base(path), "conflicts_with", existingFile)
			continue
		}
		seenSlugs[slug] = filepath.Base(path)

		h := hashBytes(blob)
		results = append(results, &reliantv1.IndexedWorkflow{
			Slug:         slug,
			Name:         name,
			RelativePath: filepath.ToSlash(rel),
			ContentHash:  h,
			MtimeUnixMs:  st.ModTime().UTC().UnixMilli(),
			YamlContent:  blob,
		})
		acc.WriteString(filepath.ToSlash(rel))
		acc.WriteString(":")
		acc.WriteString(h)
		acc.WriteString(";")
	}
	return results, []byte(acc.String())
}

func indexPresets(projectPath string) ([]*reliantv1.IndexedPreset, []byte) {
	baseDir := filepath.Join(projectPath, ".reliant", "presets")
	files := listYAMLFiles(baseDir)
	results := make([]*reliantv1.IndexedPreset, 0, len(files))
	acc := strings.Builder{}

	for _, path := range files {
		rel, err := filepath.Rel(projectPath, path)
		if err != nil {
			continue
		}
		st, err := os.Stat(path)
		if err != nil {
			continue
		}
		blob, _ := os.ReadFile(path)
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		h := hashBytes(blob)
		results = append(results, &reliantv1.IndexedPreset{
			Name:         name,
			RelativePath: filepath.ToSlash(rel),
			ContentHash:  h,
			MtimeUnixMs:  st.ModTime().UTC().UnixMilli(),
			YamlContent:  blob,
		})
		acc.WriteString(filepath.ToSlash(rel))
		acc.WriteString(":")
		acc.WriteString(h)
		acc.WriteString(";")
	}

	return results, []byte(acc.String())
}

func indexScenarios(projectPath string) ([]*reliantv1.IndexedScenario, []byte) {
	root := filepath.Join(projectPath, ".reliant", "workflows")
	all := listYAMLFiles(root)
	results := make([]*reliantv1.IndexedScenario, 0)
	acc := strings.Builder{}

	for _, path := range all {
		relToWorkflows, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}
		parts := strings.Split(filepath.ToSlash(relToWorkflows), "/")
		if len(parts) < 3 {
			continue
		}
		if parts[1] != "scenarios" {
			continue
		}

		workflowSlug := parts[0]
		scenarioName := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		relProject, err := filepath.Rel(projectPath, path)
		if err != nil {
			continue
		}
		st, err := os.Stat(path)
		if err != nil {
			continue
		}
		blob, _ := os.ReadFile(path)
		h := hashBytes(blob)
		results = append(results, &reliantv1.IndexedScenario{
			WorkflowSlug: workflowSlug,
			Name:         scenarioName,
			RelativePath: filepath.ToSlash(relProject),
			ContentHash:  h,
			MtimeUnixMs:  st.ModTime().UTC().UnixMilli(),
			YamlContent:  blob,
		})
		acc.WriteString(filepath.ToSlash(relProject))
		acc.WriteString(":")
		acc.WriteString(h)
		acc.WriteString(";")
	}

	return results, []byte(acc.String())
}

func listYAMLFiles(root string) []string {
	var files []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)
	return files
}

func extractNameFromYAML(blob []byte) string {
	if len(blob) == 0 {
		return ""
	}
	var doc map[string]interface{}
	if err := yaml.Unmarshal(blob, &doc); err != nil {
		return ""
	}
	if v, ok := doc["name"].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func readOptionalFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

func hashBytes(chunks ...[]byte) string {
	h := sha256.New()
	for _, c := range chunks {
		if len(c) == 0 {
			continue
		}
		_, _ = h.Write(c)
		_, _ = h.Write([]byte("\n--\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func normalizePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(abs)
}

func isMCPToolName(toolName string) bool {
	return strings.HasPrefix(strings.TrimSpace(toolName), "mcp__")
}

func resolveProjectPathFromContext(contextMap map[string]interface{}, workingDir, fallback string) string {
	if worktreeMap, ok := contextMap["worktree"].(map[string]interface{}); ok {
		if worktreePath, _ := worktreeMap["path"].(string); strings.TrimSpace(worktreePath) != "" {
			return normalizePath(worktreePath)
		}
	}
	if projectMap, ok := contextMap["project"].(map[string]interface{}); ok {
		if projectPath, _ := projectMap["path"].(string); strings.TrimSpace(projectPath) != "" {
			return normalizePath(projectPath)
		}
	}
	if strings.TrimSpace(workingDir) != "" {
		return normalizePath(workingDir)
	}
	return normalizePath(fallback)
}

func parseMCPServerName(toolName string) string {
	parts := strings.Split(strings.TrimSpace(toolName), "__")
	if len(parts) < 3 || parts[0] != "mcp" {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func isMCPAuthError(result *toolexec.ExecutionResult) bool {
	if result == nil {
		return false
	}
	combined := strings.ToLower(strings.TrimSpace(result.Content + " " + result.ErrorMessage))
	if combined == "" {
		return false
	}
	if strings.Contains(combined, "authentication failed") || strings.Contains(combined, "bad credentials") {
		return true
	}
	if strings.Contains(combined, "invalid api key") {
		return true
	}
	if strings.Contains(combined, "api key") && strings.Contains(combined, "invalid") {
		return true
	}
	return false
}

func (d *daemonClient) ensureMCPServersLoadedForRequest(ctx context.Context, req *reliantv1.ToolRequest, contextMap map[string]interface{}) string {
	if d == nil || d.mcpManager == nil || req == nil {
		return ""
	}

	projectPath := resolveProjectPathFromContext(contextMap, "", d.cwd)
	if projectPath == "" {
		return ""
	}

	loadResult := d.mcpManager.EnsureProjectServersLoaded(ctx, projectPath)
	if loadResult == nil || !loadResult.HasFailures() {
		return projectPath
	}

	logging.Warn(logPrefix+" MCP server autoload completed with failures",
		"projectPath", projectPath,
		"failedServers", loadResult.FailedServers,
	)
	return projectPath
}
