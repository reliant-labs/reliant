// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/cgroupmem"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/diskguard"
	"github.com/reliant-labs/reliant/internal/diskwatch"
	"github.com/reliant-labs/reliant/internal/egresslimit"
	"github.com/reliant-labs/reliant/internal/heavyjob"
	"github.com/reliant-labs/reliant/internal/instanceid"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/netports"
	"github.com/reliant-labs/reliant/internal/shellaudit"
	"github.com/reliant-labs/reliant/internal/skills/catalog"
	"github.com/reliant-labs/reliant/internal/terminal"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
	"github.com/reliant-labs/reliant/internal/toolexec/transport"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
)

const (
	logPrefix              = "[🔧 ToolsDaemonClient]"
	defaultProjectScanPath = "."
)

type daemonClient struct {
	// identityMu guards daemonID and userID: a registration ack on a new
	// stream writes them while calls still running from a draining stream
	// read them (gateway_session.go).
	identityMu sync.RWMutex
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
	// "gvisor"), from the DAEMON_RUNTIME_TYPE the operator stamps on cloud
	// daemon pods. Empty for local/unknown daemons. Advertised to the server
	// via a registration label so the model can be told about runtime limits.
	runtimeType string
	// host is what the platform declared about this daemon (StartOptions.Host).
	host Host

	// session is the gateway stream new work goes on: the newest one the
	// gateway has registered. Nil until the first registers. While the daemon
	// moves off a draining replica the old stream stays open beside it
	// (drainingSessions); see gateway_session.go.
	session          atomic.Pointer[gatewaySession]
	drainingMu       sync.Mutex
	drainingSessions map[*gatewaySession]struct{}
	// results holds each tool result until a gateway acknowledges it, so a
	// result finished while the daemon was between streams still arrives.
	results resultOutbox

	cancelMu    sync.Mutex
	cancelByReq map[string]context.CancelFunc
	// backgroundByReq marks in-flight executions the user asked to detach into
	// a background process. The executing command polls this by request id (see
	// exec.Run) — the same correlation cancel uses, and the only handle the
	// daemon keeps for a running execution.
	backgroundByReq map[string]string // requestID -> toolCallID
	preemptedByReq  map[string]bool   // cancel was a preemption; see markToolExecutionPreempted
	watchersMu      sync.Mutex
	watchersByPr    map[string]context.CancelFunc
	fsWatchersMu    sync.Mutex
	fsWatchersByPr  map[string]context.CancelFunc

	localModelsOnce sync.Once
	localModels     *localModelManager

	terminalPumps     *terminalPumpTracker
	processOutputSubs *processOutputSubTracker

	// memWatcher samples the workspace cgroup's memory usage (cloud daemons)
	// so heartbeats can carry used/limit/pressure telemetry to the gateway.
	// Inert on hosts without cgroup v2 accounting (macOS, local daemons).
	memWatcher *cgroupmem.Watcher

	// diskWatcher samples the filesystems builds and tools write to (the
	// build-cache/docker volume, home, temp) so heartbeats can carry a
	// DiskReport, and explains ENOSPC failures in tool results. The
	// process-wide diskwatch.Default(), shared with the exec paths.
	diskWatcher *diskwatch.Watcher

	// diskGuard acts on the volumes diskWatcher reports: it reclaims past
	// one threshold and refuses new heavy commands past another
	// (internal/diskguard). Its state rides on the heartbeat's disk report.
	diskGuard *diskguard.Guard

	// portWatcher scans /proc/net/tcp{,6} for loopback/wildcard LISTEN
	// sockets so heartbeats can carry the detected ports (the preview
	// affordance in the UI). Only set for pod-hosted daemons — nil for
	// local daemons, and all accessors are nil-safe.
	portWatcher *netports.Watcher

	// guard refuses work under a directory that stopped being the one the
	// daemon was serving, and stops a managed daemon whose workspace volume
	// went away (workspace_guard.go). Nil when unguarded; every use is
	// nil-safe.
	guard *workspaceGuard

	// buildSnapshot overrides buildProjectSnapshot. Nil in production; tests
	// set it to stand in for discovery that takes seconds on a real project.
	buildSnapshot func(projectPath string) (*reliantv1.ProjectConfigSnapshot, error)

	// foreignIDSetAside records that this process already moved a saved
	// daemon id aside because the gateway said another user owns it. The
	// recovery runs at most once per process; see setAsideForeignDaemonID.
	foreignIDSetAside bool

	// Log policy state: see log_policy.go. Zero values are ready to use, so a
	// daemonClient built as a literal (as tests do) needs no setup.
	reconnects  reconnectTracker
	rejections  rejectionTracker
	transitions streamTransitions

	// timing is the session's heartbeat and liveness cadence
	// (session_liveness.go). Zero in production, meaning the defaults; tests
	// shorten it.
	timing sessionTiming
}

// currentDaemonID is the daemon's id as the gateway last assigned it.
func (d *daemonClient) currentDaemonID() string {
	d.identityMu.RLock()
	defer d.identityMu.RUnlock()
	return d.daemonID
}

// currentUserID is the account the gateway last said this daemon serves.
func (d *daemonClient) currentUserID() string {
	d.identityMu.RLock()
	defer d.identityMu.RUnlock()
	return d.userID
}

// projectSnapshot builds the config snapshot for projectPath.
func (d *daemonClient) projectSnapshot(ctx context.Context, projectPath string) (*reliantv1.ProjectConfigSnapshot, error) {
	if d.buildSnapshot != nil {
		return d.buildSnapshot(projectPath)
	}
	return buildProjectSnapshotContext(ctx, projectPath)
}

type StartOptions struct {
	BootstrapConfig bootstrap.DaemonBootstrapConfig
	// StorageReaper schedules forge's storage reaper on this machine; the zero
	// value leaves it off (diskwatch.ReaperConfig).
	StorageReaper diskwatch.ReaperConfig
	// Host is what the platform declares about where this daemon runs; zero
	// off a workspace pod.
	Host Host
	// ChromeBrowserURL, when set, points the built-in chrome-devtools MCP at
	// an already-running browser (RELIANT_CHROME_BROWSER_URL).
	ChromeBrowserURL string
	// Guards is the policy for this machine's heavy commands and a filling
	// disk; the zero value is the default policy.
	Guards Guards
	// OAuthHelperOrigins are the configured web origins the on-demand OAuth
	// helper accepts beyond its defaults (oauthhelper.WebOrigins).
	OAuthHelperOrigins []string
}

// Guards is the daemon's policy for heavy commands (internal/heavyjob) and a
// filling disk (internal/diskguard).
type Guards struct {
	// Disk is the disk guard's thresholds; zero fields take the defaults.
	Disk diskguard.Config
	// HeavyCommands are the heavy-command patterns. Nil is
	// heavyjob.DefaultCommands; empty declares nothing heavy by pattern.
	HeavyCommands []string
}

func Start(ctx context.Context, opts StartOptions) error {
	if err := opts.BootstrapConfig.Validate(); err != nil {
		return fmt.Errorf("daemon runtime bootstrap config invalid: %w", err)
	}
	setOAuthHelperOrigins(opts.OAuthHelperOrigins)
	// The data directory holds the daemon's identity and state; a command run
	// with `credentials: none` does not see it (see daemonpolicy.ProtectedCredentials).
	daemonpolicy.SetDaemonDataDir(opts.BootstrapConfig.DataDir)

	// Every shell command the tool is asked to run is recorded beside the
	// daemon's other logs, for investigations of shared-state damage. A log
	// that cannot be opened is reported and skipped: auditing never stops work.
	if auditLog, err := shellaudit.Open(filepath.Join(opts.BootstrapConfig.DataDir, "logs", shellaudit.FileName), shellaudit.Options{}); err != nil {
		logging.WarnContext(ctx, logPrefix+" Shell audit log unavailable; shell commands are not being recorded", "error", err)
	} else {
		defer auditLog.Close() //nolint:errcheck
		previous := shellaudit.SetDefault(auditLog)
		defer shellaudit.SetDefault(previous)
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
			logging.WarnContext(ctx, logPrefix+" Failed to release daemon data dir claim", "error", releaseErr)
		}
	}()

	client, err := newDaemonClient(opts.BootstrapConfig, opts.Host, opts.Guards, mcp.WithChromeBrowserURL(opts.ChromeBrowserURL))
	if err != nil {
		return err
	}

	// The runtime stops when the caller cancels — or when the workspace guard
	// finds a managed workspace's volume gone, because exiting is the only
	// thing that gets it re-attached. See workspace_guard.go.
	ctx, stopRuntime := context.WithCancel(ctx)
	defer stopRuntime()
	client.armWorkspaceGuard(ctx, stopRuntime)
	initializeWorktreeActivity(ctx, opts.BootstrapConfig.DataDir)

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
	if stateErr := daemonstate.Init(opts.BootstrapConfig.DataDir, daemonstate.Startup{
		GatewayURL: gatewayURL,
		TLSMode:    string(opts.BootstrapConfig.TLSMode),
		ServerMode: opts.BootstrapConfig.ServerMode,
		Managed:    opts.Host.Managed(),
	}); stateErr != nil {
		logging.WarnContext(ctx, logPrefix+" Failed to write daemon runtime state", "error", stateErr)
	}
	defer func() {
		if clearErr := daemonstate.Clear(opts.BootstrapConfig.DataDir); clearErr != nil {
			logging.WarnContext(ctx, logPrefix+" Failed to clear daemon runtime state", "error", clearErr)
		}
	}()

	// Watch workspace memory (cgroup v2) for pressure telemetry. Returns
	// immediately on hosts without cgroup accounting, so this is free for
	// local/mac daemons. Runs for the daemon's whole lifetime, across
	// reconnects, in both server and client modes.
	go client.memWatcher.Run(ctx)
	// Same lifetime for disk telemetry: statfs of a handful of paths every
	// 10s, plus a bounded "what is using it" scan only while a volume is
	// under pressure.
	go client.diskWatcher.Run(ctx)
	// And the reaper that keeps the build caches inside their budget: forge's
	// storage pass, on a schedule and under pressure, when opts enables it.
	client.diskWatcher.ConfigureReaper(opts.StorageReaper)
	go client.diskWatcher.RunReaper(ctx)
	// The disk guard reclaims with the same pass, at once, when a volume
	// crosses its reclaim threshold — where the routine runs at all: a
	// person's own machine is left alone unless they enabled it.
	if opts.StorageReaper.Enabled {
		client.diskGuard.Register(client.diskWatcher)
	}
	go client.diskGuard.Run(ctx)

	// Repair what a crash left in the shared clones (empty objects, torn
	// indexes, stale gc.log, dead registrations) before agents run git there.
	go repairSharedClones(ctx)

	// Managed daemons: route github.com auth through the credential helper and
	// scrub any token an older release left on disk. Local daemons are left
	// alone. Best-effort and off the startup path.
	setupManagedGitCredentials(opts.Host.Managed())

	// Preview forwarder: in-pod reverse proxy that lets the workspace-proxy
	// reach loopback-bound user dev servers (see preview_forwarder.go). Runs
	// only where a pod-facing surface exists: cloud pods set a frontend port
	// (DAEMON_FRONTEND_PORT, workspace-controller pod spec), and server-mode
	// daemons are always pod-hosted. Local/mac daemons skip it — nothing
	// routes to their loopback, and binding :9191 could collide with the
	// docker-compose daemon-gateway on dev machines. Startup failure is
	// logged, never fatal: a broken preview surface must not take down tool
	// execution.
	if opts.Host.FrontendPort != 0 || opts.BootstrapConfig.ServerMode {
		// Reserve the daemon's own RPC listen port so a preview URL can never
		// route into the Connect/gRPC surface (the forwarder adds its own
		// bound port automatically).
		rpcPort := opts.BootstrapConfig.ListenPort
		if rpcPort == 0 {
			rpcPort = 9190
		}
		forwarder := NewPreviewForwarder(rpcPort)
		previewPort := previewListenPort(opts.Host.FrontendPort)
		if err := forwarder.Start(fmt.Sprintf(":%d", previewPort)); err != nil {
			logging.WarnContext(ctx, logPrefix+" preview forwarder failed to start (previews of loopback-bound servers will 502)", "error", err)
		} else {
			logging.InfoContext(ctx, logPrefix+" preview forwarder started", "addr", forwarder.Addr().String())
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
		client.portWatcher = netports.NewWatcher(netports.DefaultPollInterval, rpcPort, previewPort)
		go client.portWatcher.Run(ctx)
	}

	if opts.BootstrapConfig.ServerMode {
		logging.InfoContext(ctx, logPrefix+" Starting in server mode (listening for gateway)",
			"daemonID", client.daemonID,
			"userID", client.userID,
			"cwd", client.cwd,
			"listenPort", opts.BootstrapConfig.ListenPort,
		)
		err := client.runServerMode(ctx)
		if lost := client.guard.lostVolume(); lost != nil {
			return lost
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("tools daemon server stopped: %w", err)
		}
		return nil
	}

	logging.InfoContext(ctx, logPrefix+" Starting in-process tools daemon runtime",
		"daemonID", client.daemonID,
		"userID", client.userID,
		"cwd", client.cwd,
	)

	err = client.run(ctx)
	if lost := client.guard.lostVolume(); lost != nil {
		return lost
	}
	if err != nil && !errors.Is(err, context.Canceled) {
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

func newDaemonClient(bootCfg bootstrap.DaemonBootstrapConfig, host Host, guards Guards, mcpOpts ...mcp.ManagerOption) (*daemonClient, error) {
	// Resolve CWD: the platform's working directory (DAEMON_WORKING_DIR, used
	// in Docker and on pods), else os.Getwd().
	cwd := strings.TrimSpace(host.WorkingDir)
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

	// The runtime type is stamped by the control-plane on cloud daemon pods
	// (e.g. "kata", "gvisor"); absent for local/self-hosted daemons.
	runtimeType := strings.TrimSpace(host.RuntimeType)

	mcpManager := mcp.NewManager(mcp.RoleDaemon, mcpOpts...)
	storedConfigProvider := config.NewStoredConfigProvider(&filesystemConfigStore{})
	mcpManager.SetProjectConfigResolver(func(ctx context.Context, projectPath string) (*config.Config, error) {
		// Pass the filesystem path directly as the "projectID" — the
		// filesystemConfigStore treats it as a path, not a DB identifier.
		return storedConfigProvider.GetProjectConfig(ctx, config.ProjectRef{ProjectID: projectPath})
	})

	// This process IS the machine that runs shell commands, so its own GOOS is
	// the right answer here — unlike on the server, where reading runtime.GOOS
	// would describe the wrong machine entirely.
	toolsFactory := newDaemonToolsFactory(host)

	caps := toolsFactory.ListAvailableToolsForPlacement(tools.PlacementDaemon)
	sort.Strings(caps)

	// The disk guard acts on a filling disk: past its reclaim threshold it
	// runs the reclaimers and the heartbeat asks the server to re-check this
	// machine's kept workspaces; past its refuse threshold it refuses new
	// heavy commands.
	diskWatcher := diskwatch.Default()
	// Its reclaimers — what frees space safely beside running builds — are
	// registered in Start: forge's storage reaper, where the reaper routine
	// is enabled.
	diskGuard := diskguard.New(guards.Disk, diskWatcher)

	// Every agent on this machine runs its commands through this daemon, so
	// this is where their heavy ones (builds, test suites, codegen) are held
	// to the machine's heavy slots — the pool forge's own lanes draw from —
	// and refused on a nearly full disk. A gate that cannot queue (slots
	// switched off, own binary not found) runs them unqueued: a broken
	// limiter must never stop work.
	heavyGate, err := heavyjob.New(heavyjob.Config{
		Commands: guards.HeavyCommands,
		// The slot pool is forge's, read from forge's own variables
		// (FORGE_SLOTS_DIR, FORGE_HEAVY_SLOTS) exactly as forge's lanes
		// read them, so both draw on one machine-wide budget.
		Env:       os.Environ(), //nolint:forbidigo // forge's FORGE_* slot settings, passed through to forge/pkg/hostslot; not this daemon's config
		Admission: diskGuard,
	})
	if err != nil {
		logging.Warn(logPrefix+" Heavy commands run unqueued and unguarded", "error", err)
	} else {
		logging.Info(logPrefix + " " + heavyGate.String())
	}
	SetHeavyGate(heavyGate)

	localExec := toolexec.NewLocalToolExecutor(toolsFactory)
	egressLimiter, err := egresslimit.NewInstrumented(egresslimit.Deps{Limits: daemonEgressLimits(host)})
	if err != nil {
		return nil, fmt.Errorf("creating egress limiter: %w", err)
	}
	localExec.SetEgressLimiter(egressLimiter)
	localExec.SetMCPContextBinder(toolexec.NewLocalMCPContextBinder(mcpManager))
	// The daemon runtime IS the local machine, so give the executor a
	// LocalClient so filesystem tools (glob, view, etc.) can operate.
	localExec.SetDaemonClient(daemon.NewLocalClient().WithHeavyGate(heavyGate))

	// Initialize the terminal manager so terminal.* daemon commands can
	// create and manage PTY sessions on the user's machine.
	SetTerminalManager(terminal.NewManager())
	SetMCPManager(mcpManager)

	// daemonID is seeded from this instance's own data directory so a returning
	// daemon re-asserts its stable identity in the registration message. The
	// CLI normally reads it before constructing bootCfg; re-reading here covers
	// every other entry point (Electron, tests, the in-process runtime) so no
	// caller has to know where the id is kept. On first-ever registration it's
	// empty; the gateway assigns one via RegistrationAck and persistDaemonID
	// writes it back.
	if bootCfg.DaemonID == "" {
		bootCfg.DaemonID = bootstrap.ReadDaemonID(bootCfg.DataDir)
	}
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
	logging.SetDaemonID(bootCfg.DaemonID)

	// One transport for the process. A bad URL or TLS mode is named here, at
	// startup, rather than becoming a session error that reconnect retries
	// forever against a config that can never come good.
	httpClient, gatewayURL, err := transport.NewDaemonHTTPClient(bootCfg)
	if err != nil {
		return nil, fmt.Errorf("creating daemon HTTP client: %w", err)
	}

	client := &daemonClient{
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
		host:              host,
		cancelByReq:       make(map[string]context.CancelFunc),
		backgroundByReq:   make(map[string]string),
		watchersByPr:      make(map[string]context.CancelFunc),
		fsWatchersByPr:    make(map[string]context.CancelFunc),
		terminalPumps:     newTerminalPumpTracker(),
		processOutputSubs: newProcessOutputSubTracker(),
		memWatcher:        cgroupmem.NewWatcher(memReader, cgroupmem.DefaultPollInterval),
		diskWatcher:       diskWatcher,
		diskGuard:         diskGuard,
	}
	client.restoreResultOutbox()
	return client, nil
}

// restoreResultOutbox gives the result outbox its home in the instance's data
// directory and reloads what the previous process left there, so a tool
// result survives a daemon restart. A daemon with no data directory (an
// embedded runtime), or one whose outbox directory cannot be used, runs with
// the in-memory outbox: it never fails to start over this.
func (d *daemonClient) restoreResultOutbox() {
	dataDir := strings.TrimSpace(d.bootCfg.DataDir)
	if dataDir == "" {
		return
	}
	store, err := openOutboxStore(filepath.Join(dataDir, outboxDirName))
	if err != nil {
		logging.Warn(logPrefix+" Tool results will not survive a daemon restart", "error", err)
		return
	}
	d.results.restore(store)
}

// persistDaemonID writes the server-assigned daemon id into THIS INSTANCE's
// data directory so it survives daemon restarts and machine hostname changes.
//
// Per instance, not per origin. The id used to live in the per-origin entry of
// ~/.reliant/daemon.json, which meant every worktree on one machine read back
// the same id and re-asserted it as DaemonRegister.DaemonId — a field the
// gateway trusts verbatim — so each registration evicted the other daemon and
// the two fought indefinitely. The data directory is already the thing that is
// one-per-instance, so the id belongs beside the runtime record.
//
// It's a no-op when there's no server origin to belong to (server mode /
// in-process runtime), when the id is empty, when there is no data directory,
// or when the persisted value already matches — avoiding needless disk writes
// on every reconnect. Best-effort: a write failure is logged but never fatal,
// since the daemon is already connected and functional.
func (d *daemonClient) persistDaemonID(daemonID string) {
	if daemonID == "" || strings.TrimSpace(d.serverURL) == "" {
		return
	}
	dataDir := strings.TrimSpace(d.bootCfg.DataDir)
	if dataDir == "" {
		// Nowhere instance-scoped to put it — the id lives only in memory.
		// This is the same class of case as the --token flow that never wrote
		// credentials: functional now, re-assigned on the next start.
		return
	}
	if bootstrap.ReadDaemonID(dataDir) == daemonID {
		return
	}
	if err := bootstrap.WriteDaemonID(dataDir, daemonID); err != nil {
		logging.Warn(logPrefix+" Failed to persist assigned daemon id",
			"error", err, "dataDir", dataDir, "daemonID", daemonID)
		return
	}
	logging.Info(logPrefix+" Persisted stable daemon id for instance",
		"daemonID", daemonID, "dataDir", dataDir, "serverURL", d.serverURL)
}

// setAsideForeignDaemonID recovers from the one refusal a daemon can fix by
// itself: the gateway saying the id it asserted belongs to another user. It
// reports whether it did, in which case the caller redials.
//
// The usual cause is one laptop serving two accounts. The instance directory
// still held the id the gateway minted for the FIRST account, and this
// daemon, running as the second, asserted it — so every start was refused,
// forever, until someone found the file by hand. The server mints ids and
// never gives one to two owners, so the fix is to stop asserting this one:
// move the file aside (renamed, never deleted — it is the other account's
// machine identity) and register with no id, which the gateway answers with
// this account's own. The other owner's record is untouched.
//
// Only an id read from this instance's daemon-id file is moved: one a launcher
// passed in explicitly is not this daemon's to rewrite. And only once per
// process: if registering with no id is refused too, the credential itself
// names a daemon another account owns, and redialing cannot change that.
func (d *daemonClient) setAsideForeignDaemonID(err error) bool {
	if d.foreignIDSetAside || !bootstrap.IsForeignDaemonIDError(err) {
		return false
	}
	asserted := strings.TrimSpace(d.currentDaemonID())
	dataDir := strings.TrimSpace(d.bootCfg.DataDir)
	if asserted == "" || dataDir == "" || bootstrap.ReadDaemonID(dataDir) != asserted {
		return false
	}

	setAsideTo, mvErr := bootstrap.SetAsideDaemonID(dataDir)
	if mvErr != nil {
		logging.Error(logPrefix+" Saved daemon id belongs to another account, and moving it aside failed",
			"error", mvErr, "daemonID", asserted, "dataDir", dataDir)
		return false
	}
	d.foreignIDSetAside = true
	d.identityMu.Lock()
	d.daemonID = ""
	d.identityMu.Unlock()
	d.bootCfg.DaemonID = ""
	SetDaemonIdentity("")
	logging.SetDaemonID("")

	logging.Warn(logPrefix+" Saved daemon id belongs to another account — set it aside; registering as a new machine",
		"daemonID", asserted, "setAsideTo", setAsideTo, "dataDir", dataDir)
	if !d.bootCfg.Verbose {
		fmt.Fprintf(logging.Notices(), "  ! The machine identity saved for this workspace belongs to a different Reliant account.\n"+
			"    Moved it aside (%s) and registering this machine with your account.\n", setAsideTo)
	}
	return true
}

// foreignDaemonIDFailure is the error a daemon stops with when the gateway
// refuses its id as another user's and setAsideForeignDaemonID could not help.
// It never calls this a credential problem: the credential was accepted.
func (d *daemonClient) foreignDaemonIDFailure(err error) error {
	if d.foreignIDSetAside {
		return fmt.Errorf("the gateway still refused this daemon after its saved id was set aside and it registered "+
			"with no id, so the credential itself is bound to a machine owned by another Reliant account — "+
			"use a daemon token minted by the account you want this machine to serve: %w", err)
	}
	return fmt.Errorf("the gateway refused this daemon's id because another Reliant account owns it, and the id "+
		"was not read from %s, so it was left in place — start the daemon without that id: %w",
		bootstrap.DaemonIDPath(d.bootCfg.DataDir), err)
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

// recordStream publishes a gateway-stream transition to the daemon's on-disk
// runtime record so `reliant daemon status` reports the stream, not the PID.
// Best-effort: a daemon that is connected and serving must not die because a
// status file could not be written.
func (d *daemonClient) recordStream(s daemonstate.Stream, detail string) {
	d.recordStreamTransition(s, detail, true)
}

// recordStreamStopped records a stream that ended for good. Same record and
// machine notice as recordStream; the human line just does not promise a
// reconnect that is never coming.
func (d *daemonClient) recordStreamStopped(detail string) {
	d.recordStreamTransition(daemonstate.StreamDisconnected, detail, false)
}

func (d *daemonClient) recordStreamTransition(s daemonstate.Stream, detail string, retrying bool) {
	if level, attrs, ok := d.transitions.transition(s, summarizeReason(detail)); ok {
		logging.Log(level, logPrefix+" daemon stream state", attrs...)
	}
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
		fmt.Fprintf(os.Stdout, "%s %s\n", daemonstate.StreamNoticePrefix, string(s))
	}

	d.announceStatus(s, detail, retrying)
}

// announceStatus prints the short, human-readable half of a stream transition:
// one line a person running `reliant daemon start` in a terminal can act on.
//
// Deliberately NOT the structured log. In non-verbose mode the slog stream goes
// to the rotating file only (see commands.setupToolsDaemonLogging), leaving
// stdout for these. In verbose mode the structured lines are already on stdout
// and say the same thing with more detail, so these would be duplication —
// hence the early return.
func (d *daemonClient) announceStatus(s daemonstate.Stream, detail string, retrying bool) {
	if d.bootCfg.Verbose {
		return
	}

	switch s {
	case daemonstate.StreamConnected:
		fmt.Fprintf(logging.Notices(), "\n  ✓ Daemon connected — you can head back to Reliant and start working.\n\n")
		fmt.Fprintf(logging.Notices(), "    Press Ctrl+C to disconnect.\n\n")
	case daemonstate.StreamAwaitingCredentials:
		fmt.Fprintf(logging.Notices(), "  … Waiting for sign-in to finish.\n")
	case daemonstate.StreamDisconnected:
		// Only worth a line when it carries a reason; a bare disconnect is
		// immediately followed by a reconnect attempt, and narrating both
		// halves of a retry loop is the noise this whole change removes.
		if detail == "" {
			break
		}
		if retrying {
			fmt.Fprintf(logging.Notices(), "  ! Disconnected: %s\n    Reconnecting…\n", detail)
		} else {
			// Stopping for good: the error that follows says why and what
			// to do, so this line must not promise a retry.
			fmt.Fprintf(logging.Notices(), "  ! Disconnected: %s\n", detail)
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
		if !d.holdsOpenSession() {
			d.recordStream(daemonstate.StreamConnecting, "")
		}
		err := d.runSession(ctx)
		if errors.Is(err, errGatewayMoving) {
			// Make before break: the draining replica's stream is still
			// open and serving, so dial its successor now, with no backoff.
			delay = transport.ReconnectMinDelay
			continue
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			if d.setAsideForeignDaemonID(err) {
				// Redial at once: nothing on the gateway has to change, this
				// daemon just stops asserting an id that is not its own.
				continue
			}
			if isFatalError(err) {
				d.recordStreamStopped(err.Error())
				logging.ErrorContext(ctx, logPrefix+" Fatal error — not reconnecting",
					"error", err,
					"code", connect.CodeOf(err).String(),
					"grpc_url", d.bootCfg.GRPCURL,
				)
				d.stopAllStreams()
				if bootstrap.IsForeignDaemonIDError(err) {
					return d.foreignDaemonIDFailure(err)
				}
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
			// A failed attempt to move off a draining replica leaves the
			// daemon connected through that replica's stream until its
			// deadline: it is not disconnected yet.
			if !d.holdsOpenSession() {
				d.recordStream(daemonstate.StreamDisconnected, err.Error())
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
				// ERROR once per streak, with the causes; then one line per
				// window carrying a count. The retry itself is DEBUG.
				rej := d.rejections.rejected()
				switch {
				case rej.first:
					logging.ErrorContext(ctx, logPrefix+" Rejected: credentials not accepted — retrying. "+
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
				case rej.emit:
					logging.ErrorContext(ctx, logPrefix+" Credentials still not accepted",
						"error", err,
						"code", connect.CodeOf(err).String(),
						"rejections", rej.total,
						"suppressed", rej.suppressed,
						"grpc_url", d.bootCfg.GRPCURL,
					)
				default:
					logging.DebugContext(ctx, logPrefix+" Credentials not accepted; retrying",
						"rejections", rej.total, "delay", delay)
				}
			} else {
				// A dropped stream is routine (gateway rollout, idle timeout,
				// node drain): INFO with an attempt count, WARN once the
				// streak is long, ERROR once the outage needs an operator.
				d.rejections.reset()
				dec := d.reconnects.failed(time.Since(sessionStart))
				attrs := append(dec.attrs,
					"error", err,
					"code", connect.CodeOf(err).String(),
					"delay", delay,
					"grpc_url", d.bootCfg.GRPCURL,
				)
				if dec.emit {
					logging.LogContext(ctx, dec.level, logPrefix+" Session ended; reconnecting", attrs...)
				} else {
					logging.DebugContext(ctx, logPrefix+" Session ended; reconnecting", attrs...)
				}
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

	logging.InfoContext(ctx, logPrefix+" Connecting to gateway", "url", baseURL)

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
	// Scoped to this session: the goroutine exits when the session ends, so
	// a reconnecting daemon does not accumulate one per session.
	sessionOver := make(chan struct{})
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
			DaemonType: resolveDaemonType(d.host.DaemonType, "local"),
			Labels:     d.registerLabels(),
			// Re-assert our persisted stable identity (empty on first-ever
			// registration). The gateway trusts it for unbound PATs so identity
			// survives restarts and hostname changes instead of being re-derived
			// from the (volatile) hostname.
			DaemonId: d.currentDaemonID(),
			// This daemon moves to another replica when its gateway drains,
			// and holds tool results until they are acknowledged
			// (gateway_session.go).
			MovesOnDrain: true,
		}},
	}
	stampBuildIdentity(register.GetRegister())
	if err := sendRegistration(stream, register); err != nil {
		close(sessionOver)
		return fmt.Errorf("sending daemon registration to %s: %w", baseURL, err)
	}

	// --- Start the send channel + single writer goroutine ---
	// The session becomes the daemon's current one only when the gateway
	// acknowledges the registration (activateSession): until then, a
	// draining predecessor keeps carrying the daemon's work.
	s := newGatewaySession()
	// Retiring a drained session is the same close the cancellation path
	// uses: it unparks Receive without touching the send half.
	s.retire = func() { _ = stream.CloseResponse() }
	go s.runSender(stream)

	if err := d.sendProjectDiscovery(s); err != nil {
		logSendFailure(logPrefix+" Failed to send project discovery", err)
	}

	// End-to-end liveness (session_liveness.go). A stream the gateway has
	// stopped reading, or stopped writing, is closed from this side so the
	// loop below returns and run() redials — the transport would otherwise
	// keep it open indefinitely.
	liveness := newSessionLiveness(d.timing, time.Now())
	var deadStream atomic.Pointer[error]
	go func() {
		ticker := time.NewTicker(liveness.timing.livenessCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-sessionOver:
				return
			case now := <-ticker.C:
				if err := liveness.check(now); err != nil {
					deadStream.Store(&err)
					logging.WarnContext(ctx, logPrefix+" Gateway stream is dead; ending the session to reconnect", "reason", err.Error())
					// Same close the cancellation path uses: it unparks the
					// Receive below without touching the send half.
					_ = stream.CloseResponse()
					return
				}
			}
		}
	}()

	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	go d.runHeartbeats(heartbeatCtx, s, liveness)
	localModelCtx, stopLocalModels := context.WithCancel(heartbeatCtx)
	go d.localModelMgr().run(localModelCtx, d.send)

	end := func() {
		stopLocalModels()
		cancelHeartbeat()
		close(sessionOver)
		s.drain(senderDrainTimeout)
		d.sessionEnded(s)
	}
	err := d.receiveSession(ctx, s, stream, liveness, &deadStream)
	if !errors.Is(err, errGatewayMoving) {
		end()
		return err
	}
	// Make before break (gateway_session.go): this stream stays open, read
	// in the background, until it is retired or the replica ends it, while
	// run() dials its successor now. The successor publishes the local-model
	// inventory once it registers.
	stopLocalModels()
	go func() {
		defer end()
		if err := d.receiveSession(ctx, s, stream, liveness, &deadStream); err != nil && ctx.Err() == nil {
			logging.DebugContext(ctx, logPrefix+" Drained gateway stream ended", "error", err)
		}
	}()
	return err
}

// receiveSession reads one session's stream and dispatches what arrives, until
// the stream ends — or, the first time only, until the gateway says the
// replica serving it is draining (errGatewayMoving).
func (d *daemonClient) receiveSession(
	ctx context.Context,
	s *gatewaySession,
	stream interface {
		Receive() (*reliantv1.ServerMessage, error)
	},
	liveness *sessionLiveness,
	deadStream *atomic.Pointer[error],
) error {
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
			// Likewise when the liveness check closed it: the reason is the
			// news, the closed body is only how it was delivered.
			if dead := deadStream.Load(); dead != nil {
				return fmt.Errorf("daemon stream: %w", *dead)
			}
			return fmt.Errorf("daemon stream receive: %w", err)
		}
		if msg == nil {
			continue
		}
		liveness.received(msg, time.Now())
		if notice := msg.GetDrain(); notice != nil {
			if d.beginSessionDrain(s, notice) {
				return errGatewayMoving
			}
			continue
		}
		if err := d.handleServerMessage(ctx, s, msg); err != nil {
			logging.WarnContext(ctx, logPrefix+" Failed handling server message", "error", err)
		}
	}
}

// handleServerMessage dispatches one message that arrived on session s. A
// request's reply goes back on s while s is open (see reply).
func (d *daemonClient) handleServerMessage(ctx context.Context, s *gatewaySession, msg *reliantv1.ServerMessage) error {
	switch m := msg.Message.(type) {
	case *reliantv1.ServerMessage_ToolRequest:
		if m.ToolRequest == nil {
			return nil
		}
		// Recorded before the goroutine starts, so a status question that
		// arrives right behind the request already finds it running.
		d.results.start(m.ToolRequest.RequestId)
		if s != nil && m.ToolRequest.RequestId != "" {
			s.inFlight.Add(1) // until a gateway acknowledges the result
		}
		go d.executeTool(s, m.ToolRequest)
		return nil

	case *reliantv1.ServerMessage_ToolResultAck:
		if m.ToolResultAck != nil {
			d.handleToolResultAck(m.ToolResultAck.RequestId)
		}
		return nil

	case *reliantv1.ServerMessage_ToolCancel:
		if m.ToolCancel == nil {
			return nil
		}
		if m.ToolCancel.Reason == toolexec.PreemptedCancelReason {
			d.markToolExecutionPreempted(m.ToolCancel.RequestId)
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
			//
			// It is also when this stream takes over the daemon's work from
			// any predecessor, and receives every result still waiting for
			// a gateway.
			if s != nil {
				d.activateSession(s)
			}
			d.recordStream(daemonstate.StreamConnected, "")
			d.rejections.reset()
			if failures, outage := d.reconnects.recovered(); failures > 0 {
				logging.InfoContext(ctx, logPrefix+" Gateway stream recovered",
					"failed_attempts", failures, "disconnected_for_ms", outage.Milliseconds())
			}
			d.localModelMgr().signalRegistered()

			// Gateway derives both identities from the PAT used to authenticate
			// the stream and tells us. Daemon stores them locally for downstream
			// use (tool execution context, logging).
			if id := m.RegistrationAck.DaemonId; id != "" {
				d.identityMu.Lock()
				d.daemonID = id
				d.identityMu.Unlock()
				// Publish to the process-wide identity so static command handlers
				// (e.g. exec.bg_list) can stamp ProcessInfo.DaemonID for preview URLs.
				SetDaemonIdentity(id)
				logging.SetDaemonID(id)
				logging.InfoContext(ctx, logPrefix+" Daemon identity assigned by gateway", "daemonID", id)
				// Persist the assigned id per-origin so it survives restarts and
				// hostname changes; next reconnect re-asserts it in Register.
				d.persistDaemonID(m.RegistrationAck.DaemonId)
			}
			if m.RegistrationAck.UserId != "" {
				d.identityMu.Lock()
				d.userID = m.RegistrationAck.UserId
				d.identityMu.Unlock()
			}
			// Watchers start here, in stream order, so a later
			// Watch/UnwatchProjectConfigs for the same path cannot be
			// overtaken. Starting one is cheap: its first snapshot is built
			// on its own goroutine.
			requested := m.RegistrationAck.RequestedProjectPaths
			for _, projectPath := range requested {
				d.startProjectWatcher(ctx, projectPath, true)
			}
			// The load responses are NOT built here. Each is a full config
			// snapshot — skill discovery included, 4.9s cold for one
			// multi-repo project — and the ack names every project the user
			// has. Built inline, they held this receive loop for the whole
			// run, so no command behind the ack was dispatched until all of
			// them finished: everything sent just after a reconnect waited
			// on discovery it had nothing to do with. Same fix, same reason
			// as LoadProjectConfigs below. One goroutine, sequential, so a
			// reconnect does not start a discovery per project at once.
			if len(requested) > 0 {
				go func(paths []string) {
					for _, projectPath := range paths {
						if ctx.Err() != nil {
							return // session over; the next ack asks again
						}
						if err := d.sendLoadProjectConfigResponse(ctx, projectPath, uuid.New().String()); err != nil {
							logging.WarnContext(ctx, logPrefix+" Failed responding to registration requested project load", "projectPath", projectPath, "error", err)
						}
					}
				}(requested)
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
			if err := d.sendLoadProjectConfigResponse(ctx, req.ProjectPath, req.RequestId); err != nil {
				logSendFailure(logPrefix+" Failed to send project config response", err, "project_path", req.ProjectPath, "request_id", req.RequestId)
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
		if s != nil {
			s.inFlight.Add(1) // until its response is queued
		}
		go d.handleDaemonCommand(s, m.DaemonCommand)
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

	case *reliantv1.ServerMessage_LocalModelHttpRequest:
		if m.LocalModelHttpRequest != nil {
			d.handleLocalModelHTTPRequest(ctx, s, m.LocalModelHttpRequest)
		}
		return nil

	case *reliantv1.ServerMessage_LocalModelHttpCancel:
		if m.LocalModelHttpCancel != nil {
			d.handleLocalModelHTTPCancel(m.LocalModelHttpCancel)
		}
		return nil

	case *reliantv1.ServerMessage_LocalModelRefresh:
		d.handleLocalModelRefresh()
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
		logSendFailure(logPrefix+" Failed to send kill process response", sendErr, "process_id", req.ProcessId)
	}
}

// handleDaemonCommand runs one command that arrived on session s and answers
// it there (see reply).
func (d *daemonClient) handleDaemonCommand(s *gatewaySession, req *reliantv1.DaemonCommandRequest) {
	// After the response is queued (the deferred sends below run first):
	// a draining stream is held open until then.
	defer d.requestDone(s)
	// Continue the gateway span that forwarded the command, so every log line
	// written for it carries the dispatching request's trace_id.
	root := commandDispatchContext(req)
	ctx, cancel := context.WithCancel(root)
	if req.TimeoutMs > 0 {
		cancel()
		ctx, cancel = context.WithTimeout(root, time.Duration(req.TimeoutMs)*time.Millisecond)
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
		logging.Warn(logPrefix+" daemon command handled",
			"command_type", req.CommandType, "request_id", req.RequestId,
			"elapsed_ms", 0, "outcome", "denied",
			"grantID", policy.GrantIDForLog(), "error", err)
		d.sendCommandFailure(s, req, err)
		return
	}
	ctx = daemonpolicy.NewContext(ctx, policy)

	releaseRecovery, admissionErr := beginRecoveryCommand(req.CommandType, req.Payload)
	if admissionErr != nil {
		d.sendCommandFailure(s, req, admissionErr)
		return
	}
	defer releaseRecovery()

	// What became of a tool call is the daemon's own bookkeeping, not the
	// disk's, so it is answered even while the guard below refuses work.
	if req.CommandType == toolexec.ToolStatusCommand {
		payload, err := d.handleToolStatus(req.Payload)
		if err != nil {
			d.sendCommandFailure(s, req, err)
			return
		}
		if sendErr := d.reply(s, &reliantv1.DaemonMessage{
			Message: &reliantv1.DaemonMessage_DaemonCommandResponse{DaemonCommandResponse: &reliantv1.DaemonCommandResponse{
				RequestId:   req.RequestId,
				CommandType: req.CommandType,
				Success:     true,
				Payload:     payload,
			}},
		}); sendErr != nil {
			logSendFailure(logPrefix+" Failed to send tool status", sendErr, "request_id", req.RequestId)
		}
		return
	}

	// Refuse everything while $HOME is not the directory the daemon started
	// with: whatever the command touches would be the wrong disk.
	if refusal := d.guard.commandPreflight(); refusal != nil {
		logging.Warn(logPrefix+" daemon command handled",
			"command_type", req.CommandType, "request_id", req.RequestId,
			"elapsed_ms", 0, "outcome", "refused: workspace directory changed", "error", refusal)
		d.sendCommandFailure(s, req, refusal)
		return
	}

	// Let a long-running exec detach itself if the user asks. Keyed by request
	// id — the same correlation cancellation uses, and the only handle the
	// daemon has on a running execution.
	requestID := req.RequestId
	ctx = daemon.WithBackgroundProbe(ctx, func() (string, bool) {
		return d.takeBackgroundRequest(requestID)
	})

	// Watchdog: daemon commands can wedge on network calls (the 2026-07-09
	// incident was worktree.git_changes hanging on a git remote), and the
	// stream response is only sent on completion — without this nothing is
	// logged anywhere while a command hangs.
	start := time.Now()
	watchdog := time.AfterFunc(slowDaemonCommandThreshold, func() {
		logging.Warn(logPrefix+" daemon command still running",
			"commandType", req.CommandType, "requestID", req.RequestId)
	})

	resultPayload, err := defaultRegistry.Handle(ctx, req.CommandType, req.Payload)
	if refusal := d.guard.commandVerdict(); refusal != nil {
		// $HOME went away while this ran; its result describes the wrong disk.
		resultPayload, err = nil, refusal
	}

	watchdog.Stop()
	logDaemonCommandHandled(ctx, req, time.Since(start), err, d.sendBacklog())

	resp := &reliantv1.DaemonMessage{
		Message: &reliantv1.DaemonMessage_DaemonCommandResponse{
			DaemonCommandResponse: &reliantv1.DaemonCommandResponse{
				RequestId:   req.RequestId,
				CommandType: req.CommandType,
				Success:     err == nil,
				Payload:     resultPayload,
				// The gateway's span splits the round trip with it: the
				// daemon exports no spans of its own.
				HandlerElapsedMs: time.Since(start).Milliseconds(),
			},
		},
	}
	if err != nil {
		resp.GetDaemonCommandResponse().ErrorMessage = withNoSpaceExplanation(d.diskWatcher, err.Error())
	}
	if sendErr := d.reply(s, resp); sendErr != nil {
		logSendFailure(logPrefix+" Failed to send daemon command response", sendErr, "request_id", req.RequestId, "command_type", req.CommandType)
	}

	d.announceFilesystemMutation(req.RequestId, req.CommandType, resultPayload, err)

	// NOTE: the terminal output pump is NOT started here on terminal.create.
	// It is subscribe-driven — started only when a TerminalOutputSubscribeMessage
	// arrives (see handleServerMessage), mirroring the process-output subscribe
	// flow. Until then the PTY buffers its initial shell prompt, so the prompt
	// cannot be drained before a subscriber's interest chain is established.
}

// slowDaemonCommandThreshold is when a command's watchdog fires, and when its
// completion line is raised from Info to Warn.
const slowDaemonCommandThreshold = 10 * time.Second

// logDaemonCommandHandled writes the one line every handled daemon command
// gets: which command, how long its handler ran, and how it ended.
//
// Without it a slow round trip could not be split between the daemon and
// everything in front of it. A 780ms greenfield probe on 2026-10-05 left no
// trace on the daemon at all — only commands slower than 10s were logged — so
// "the daemon queued it", "the scan was slow" and "the gateway sat on the
// reply" were indistinguishable. sendBacklog is the number of messages already
// queued for the gateway stream when the response joined it: a fast handler
// with a large backlog means the reply waited on the wire, not on the work.
//
// Payloads are never logged — they carry file contents and credentials.
func logDaemonCommandHandled(ctx context.Context, req *reliantv1.DaemonCommandRequest, elapsed time.Duration, err error, sendBacklog int) {
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	args := []any{
		"command_type", req.GetCommandType(),
		"request_id", req.GetRequestId(),
		"elapsed_ms", elapsed.Milliseconds(),
		"outcome", outcome,
		"send_backlog", sendBacklog,
	}
	if err != nil {
		args = append(args, "error", err)
	}
	logging.LogContext(ctx, daemonCommandLogLevel(elapsed, err != nil), logPrefix+" daemon command handled", args...)
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

// announceFilesystemMutation tells the server a command changed (or tried
// to change) the shape of the filesystem, so it can emit a refetch — or, on
// failure, a durable failure notification — for clients that were not
// listening for the DaemonCommandResponse.
//
// This is the ONLY way a command dispatched from DAEMON_PENDING_COMMANDS
// (no live RPC waiter — see control-plane's fire-and-forget CloneRepo) gets
// its outcome to a client at all: the response above went nowhere, because
// nothing registered a pendingCommands channel for a request id the API
// server never issued synchronously. Without this, a failed pending clone
// would vanish into the daemon-gateway log with no user-visible trace.
//
// Best-effort and non-blocking by design: the command response has already
// been sent (or attempted), and a failure to announce must never be
// reported as if the underlying command itself had failed.
func (d *daemonClient) announceFilesystemMutation(requestID, commandType string, resultPayload []byte, cmdErr error) {
	if !filesystemMutatingCommands[commandType] {
		return
	}

	if cmdErr != nil {
		d.announceCommandFailure(requestID, commandType, cmdErr)
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

// announceCommandFailure is the failure counterpart of announceFilesystemMutation.
// It does NOT carry a request id keyed to a live waiter — a normal (live)
// caller already got its failure via the DaemonCommandResponse sent just
// before this runs, so re-announcing it here would be a harmless duplicate
// for that caller and the only signal at all for a pending-drained one. The
// server turns it into a user_updates row (see handleDaemonCommandFailed),
// which replays to any client that reconnects after the fact.
func (d *daemonClient) announceCommandFailure(requestID, commandType string, cmdErr error) {
	if err := d.send(&reliantv1.DaemonMessage{
		Message: &reliantv1.DaemonMessage_DaemonCommandFailed{
			DaemonCommandFailed: &reliantv1.DaemonCommandFailed{
				RequestId:       requestID,
				CommandType:     commandType,
				ErrorMessage:    cmdErr.Error(),
				TimestampUnixMs: time.Now().UTC().UnixMilli(),
			},
		},
	}); err != nil {
		logging.Warn(logPrefix+" Failed to announce command failure",
			"requestID", requestID, "commandType", commandType, "error", err)
	}
}

// sendCommandFailure reports a command that never ran. Refusals must still
// answer on the stream: the caller is blocked on a reply, and dropping it
// would surface as a timeout rather than as the denial it is.
func (d *daemonClient) sendCommandFailure(s *gatewaySession, req *reliantv1.DaemonCommandRequest, cause error) {
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
	if sendErr := d.reply(s, resp); sendErr != nil {
		logSendFailure(logPrefix+" Failed to send daemon command failure", sendErr, "request_id", req.RequestId, "command_type", req.CommandType)
	}
}

// runHeartbeats sends the daemon's keepalive on session s until ctx ends.
// Every open stream heartbeats, a draining one included, so neither end reaps
// it as dead while it still carries replies. liveness, when non-nil, is told
// about each heartbeat sent so the session can tell whether the gateway
// acknowledges them.
func (d *daemonClient) runHeartbeats(ctx context.Context, s *gatewaySession, liveness *sessionLiveness) {
	ticker := time.NewTicker(d.timing.withDefaults().heartbeatInterval)
	defer ticker.Stop()

	sendHeartbeat := func(now time.Time) error {
		hb := &reliantv1.DaemonHeartbeat{Timestamp: now.UnixMilli()}
		// Piggyback workspace memory telemetry when available (cloud daemons
		// in a cgroup-limited pod). Fields stay zero on hosts without cgroup
		// accounting — the gateway treats limit==0 as "not reported".
		if sample, ok := d.memWatcher.Latest(); ok {
			hb.MemoryUsedBytes = sample.UnreclaimableBytes
			hb.MemoryLimitBytes = sample.LimitBytes
			hb.MemoryPressure = sample.Pressure
			hb.OomKills = sample.OOMKills
			if !sample.LastOOMKillAt.IsZero() {
				hb.LastOomKillAtMs = sample.LastOOMKillAt.UnixMilli()
			}
		}
		// Piggyback detected listener ports (pod-hosted daemons only; the
		// watcher is nil-safe and ok stays false until the first scan).
		if ports, ok := d.portWatcher.Latest(); ok {
			hb.DetectedPorts = ports
		}
		// Piggyback the disk report (every daemon that can statfs; ok stays
		// false until the first sample).
		if rep, ok := d.diskWatcher.Report(); ok {
			d.diskGuard.Annotate(rep)
			hb.Disk = rep
		}
		hb.ToolCallsInFlight = uint32(d.results.runningCount())
		if err := s.send(&reliantv1.DaemonMessage{
			Message: &reliantv1.DaemonMessage_Heartbeat{Heartbeat: hb},
		}); err != nil {
			return err
		}
		liveness.heartbeatSent(hb.Timestamp, time.Now())
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := sendHeartbeat(now); err != nil {
				logSendFailure(logPrefix+" Failed to send heartbeat", err)
				return
			}
		case <-d.memWatcher.Changed():
			// Pressure flipped — tell the gateway now instead of waiting up
			// to a full heartbeat interval. Nil-safe: a nil channel (no
			// watcher) never fires.
			if err := sendHeartbeat(time.Now()); err != nil {
				logSendFailure(logPrefix+" Failed to send pressure heartbeat", err)
				return
			}
		case <-d.diskWatcher.Changed():
			// A volume's pressure flipped, its consumers were measured, a
			// command just failed with ENOSPC, or a cache clean-up started or
			// finished — the UI should know now, not up to 15s later.
			if err := sendHeartbeat(time.Now()); err != nil {
				logSendFailure(logPrefix+" Failed to send disk heartbeat", err)
				return
			}
		case <-d.diskGuard.Changed():
			// The disk guard started or stopped reclaiming or refusing heavy
			// commands, or finished a reclaim pass.
			if err := sendHeartbeat(time.Now()); err != nil {
				logSendFailure(logPrefix+" Failed to send disk-guard heartbeat", err)
				return
			}
		case <-d.portWatcher.Changed():
			// Listener set changed — surface the new port (or its
			// disappearance) immediately so the UI preview affordance tracks
			// dev-server starts without a heartbeat-interval lag.
			if err := sendHeartbeat(time.Now()); err != nil {
				logSendFailure(logPrefix+" Failed to send port-change heartbeat", err)
				return
			}
		}
	}
}

// executeTool runs one tool request that arrived on session s, and hands its
// result to sendToolResult, which holds it until a gateway acknowledges it.
// dispatchContext is the root context for executing req: it continues the
// trace context of the request that dispatched the call (the gateway injects
// it into ToolRequest.trace_context), so every log line written for the
// execution carries that trace's trace_id. A request with none is a bare
// background context.
func dispatchContext(req *reliantv1.ToolRequest) context.Context {
	return otel.GetTextMapPropagator().Extract(context.Background(), propagation.MapCarrier(req.GetTraceContext()))
}

// commandDispatchContext is dispatchContext for a daemon command: it continues
// the gateway's `gateway.daemon.command` span (DaemonCommandRequest.trace_context).
func commandDispatchContext(req *reliantv1.DaemonCommandRequest) context.Context {
	return otel.GetTextMapPropagator().Extract(context.Background(), propagation.MapCarrier(req.GetTraceContext()))
}

func (d *daemonClient) executeTool(s *gatewaySession, req *reliantv1.ToolRequest) {
	if req == nil {
		return
	}

	// Before the tool can do anything a restart would cut short.
	d.results.persistRunning(req.RequestId)

	releaseRecovery, admissionErr := beginRecoveryWork()
	if admissionErr != nil {
		d.sendToolResult(s, req.RequestId, &toolexec.ExecutionResult{
			Success: false, IsError: true, Content: admissionErr.Error(),
			ErrorMessage: admissionErr.Error(), ErrorCode: "RECOVERY_FINALIZING",
		})
		return
	}
	defer releaseRecovery()

	execCtx, cancel := context.WithCancel(dispatchContext(req))
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
	execCtx = daemon.WithBackgroundProbe(execCtx, func() (string, bool) {
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
		contextMap["user_id"] = d.currentUserID()
	}

	// The worktree or project this call works in: recorded the first time it
	// is seen, and refused while it (or $HOME) is no longer the directory the
	// daemon was serving. Empty for a call with no project context, which
	// only a $HOME fault refuses.
	workspaceRoot := resolveProjectPathFromContext(contextMap, "", "")
	if refused := d.guard.preflight(workspaceRoot); refused != nil {
		d.sendToolResult(s, req.RequestId, refused)
		return
	}

	projectPath := ""
	if isMCPToolName(req.ToolName) {
		projectPath = d.ensureMCPServersLoadedForRequest(execCtx, req, contextMap)
	}

	toolStart := time.Now()
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

	if execCtx.Err() != nil && !d.answersPreemption(req, result) {
		result = &toolexec.ExecutionResult{
			Success:      false,
			IsError:      true,
			Content:      "Tool execution cancelled",
			ErrorMessage: execCtx.Err().Error(),
			ErrorCode:    "CANCELLED",
		}
	}

	// A call in flight when its directory went away reports that, not the
	// cancellation or missing working directory that followed from it.
	if refused := d.guard.verdict(workspaceRoot); refused != nil {
		result = refused
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

	logToolFinished(req.ToolName, result, time.Since(toolStart))
	d.sendToolResult(s, req.RequestId, result)
}

// toolResponse is the wire reply to a tool request.
func (d *daemonClient) toolResponse(requestID string, result *toolexec.ExecutionResult) *reliantv1.DaemonMessage {
	// Sanitize all string fields to valid UTF-8 before protobuf serialization.
	// Protobuf3 string fields reject invalid UTF-8, which would silently drop
	// the response and leave the server-side poller waiting until timeout.
	result.Content = strings.ToValidUTF8(result.Content, "\uFFFD")
	result.Metadata = strings.ToValidUTF8(result.Metadata, "\uFFFD")
	result.ErrorMessage = strings.ToValidUTF8(result.ErrorMessage, "\uFFFD")
	if result.IsError || !result.Success {
		// A tool that failed for want of disk says which volume is full, so
		// the agent stops retrying the build blindly.
		if note := d.diskWatcher.ExplainNoSpace(result.Content + "\n" + result.ErrorMessage); note != "" {
			result.Content = appendLine(result.Content, note)
			if result.ErrorMessage != "" {
				result.ErrorMessage = appendLine(result.ErrorMessage, note)
			}
		}
	}

	return &reliantv1.DaemonMessage{
		Message: &reliantv1.DaemonMessage_ToolResponse{ToolResponse: &reliantv1.ToolResponse{
			RequestId:    requestID,
			Success:      result.Success,
			IsError:      result.IsError,
			Content:      result.Content,
			Metadata:     result.Metadata,
			ErrorMessage: result.ErrorMessage,
			ErrorCode:    result.ErrorCode,
			Backgrounded: result.Backgrounded,
		}},
	}
}

// registrationStream is the part of the client stream sendRegistration needs.
type registrationStream interface {
	messageSender
	Receive() (*reliantv1.ServerMessage, error)
}

// sendRegistration sends the first message on a freshly opened stream.
//
// connect-go reports a failed stream on Send as an error wrapping io.EOF and
// says to call Receive for the real one: the dial error (DNS, TLS, refused),
// the HTTP status, or the gateway's own rejection only surface from the
// response side. Without this the daemon logged "write envelope: EOF" for
// every cause alike — a pod whose DNS or CA bundle was broken looked the same
// as a gateway outage, and nothing in the logs said which.
//
// The returned error is the Receive error when there is one (it keeps its
// connect code, so the caller's fatal/retry classification sees the real
// code), with the Send error kept in the message.
func sendRegistration(stream registrationStream, register *reliantv1.DaemonMessage) error {
	sendErr := stream.Send(register)
	if sendErr == nil || !errors.Is(sendErr, io.EOF) {
		return sendErr
	}
	if _, recvErr := stream.Receive(); recvErr != nil && !errors.Is(recvErr, io.EOF) {
		return fmt.Errorf("%w (send: %v)", recvErr, sendErr)
	}
	return sendErr
}

// messageSender is the write half of the gateway stream. It is an interface so
// session teardown can be tested against a sender whose Send never returns —
// the condition that makes an unbounded drain wedge the whole process.
type messageSender interface {
	Send(*reliantv1.DaemonMessage) error
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
	delete(d.preemptedByReq, requestID)
}

// markToolExecutionPreempted records that the cancel about to reach this
// execution is a preemption, not an abandonment. Set BEFORE the cancel fires,
// so the execution reads it as it unwinds. A mark for an execution that has
// already finished is harmless: nothing reads it, and unregisterCancel clears
// it.
func (d *daemonClient) markToolExecutionPreempted(requestID string) {
	if strings.TrimSpace(requestID) == "" {
		return
	}
	d.cancelMu.Lock()
	defer d.cancelMu.Unlock()
	if d.preemptedByReq == nil {
		d.preemptedByReq = make(map[string]bool)
	}
	d.preemptedByReq[requestID] = true
}

// answersPreemption reports whether a cancelled execution's own result is its
// answer. That is so only for a preemption of a preemptable tool that answered
// cleanly: such a tool promises that being cut short loses nothing, and its
// "stopped waiting; still running" is what the agent needs, where the generic
// "Tool execution cancelled" would tell it nothing. Every other cancellation
// still reports as cancelled.
func (d *daemonClient) answersPreemption(req *reliantv1.ToolRequest, result *toolexec.ExecutionResult) bool {
	if result == nil || !result.Success || result.IsError || !tools.IsPreemptable(req.ToolName) {
		return false
	}
	d.cancelMu.Lock()
	defer d.cancelMu.Unlock()
	// The server names the execution by its tool call id; either id is
	// registered for it.
	return d.preemptedByReq[req.RequestId] || d.preemptedByReq[req.ToolCallId]
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

// cancelAllRequests cancels every in-flight tool execution and daemon
// command. Each one still sends its reply as it unwinds.
func (d *daemonClient) cancelAllRequests() {
	d.cancelMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(d.cancelByReq))
	for _, cancel := range d.cancelByReq {
		cancels = append(cancels, cancel)
	}
	d.cancelMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// sendProjectDiscovery announces the project under the daemon's working
// directory on session s, as part of opening it.
func (d *daemonClient) sendProjectDiscovery(s *gatewaySession) error {
	projectPath := strings.TrimSpace(d.host.ProjectPath)
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
	return s.send(msg)
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

func (d *daemonClient) sendLoadProjectConfigResponse(ctx context.Context, projectPath, requestID string) error {
	snapshot, err := d.projectSnapshot(ctx, projectPath)
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
		snapshot, err := d.projectSnapshot(ctx, projectPath)
		if err != nil || snapshot == nil {
			if err != nil {
				logging.WarnContext(ctx, logPrefix+" Failed to build project snapshot for watcher", "projectPath", projectPath, "error", err)
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
			logSendFailure(logPrefix+" Failed to send project config delta", err, "project_path", projectPath)
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
	return buildProjectSnapshotContext(context.Background(), projectPath)
}

func buildProjectSnapshotContext(ctx context.Context, projectPath string) (*reliantv1.ProjectConfigSnapshot, error) {
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

	userConfigYAML, _ := readOptionalFile(userConfigPath)
	projectConfigYAML, _ := readOptionalFile(projectConfigPath)
	localConfigYAML, _ := readOptionalFile(localConfigPath)
	userMCP, _ := readOptionalFile(userMCPPath)
	projectMCP, _ := readOptionalFile(projectMCPPath)
	localMCP, _ := readOptionalFile(localMCPPath)
	globalMemory, _ := readOptionalFile(globalMemoryPath)
	projectMemory := readProjectMemory(projectPath)

	workflowSources, err := readWorkflowSources(ctx, projectPath)
	if err != nil {
		return nil, err
	}
	workflows, workflowBytes := indexWorkflows(workflowSources)
	presets, presetBytes := indexPresets(projectPath, workflowSources)
	scenarios, scenarioBytes := indexScenarios(workflowSources)
	skills, skillBytes := indexSkills(projectPath)
	repoMemories, repoMemoriesBytes := collectRepoMemories(projectPath)
	heads := readRepoHeads(projectPath, discoverRepoSources(ctx, projectPath))
	states := repoStates(ctx, projectPath, heads)

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
		headsVersion(heads),
		statesVersion(states),
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
		RepoStates:      states,
	}, nil
}

// localMemoryFile is the personal, uncommitted companion of a reliant.md. It
// is read beside EVERY reliant.md — the project root's and each nested repo's
// — and placed after it, so personal notes have the last word.
const localMemoryFile = "reliant.local.md"

// withLocalMemory returns md followed by dir's reliant.local.md, if any. The
// root and the nested repos share this one rule: a root that skipped it left a
// single-repo project's local file silently unread.
func withLocalMemory(dir string, md []byte) []byte {
	local, _ := readOptionalFile(filepath.Join(dir, localMemoryFile))
	if len(local) == 0 {
		return md
	}
	if len(md) == 0 {
		return local
	}
	return prependMemory(md, local)
}

// readProjectMemory is the project root's memory: the forge framework guide
// (forge projects only — forge writes no top-level reliant.md, so the embedded
// template rendered here is the authoritative source), then reliant.md, then
// reliant.local.md.
func readProjectMemory(projectPath string) []byte {
	md, _ := readOptionalFile(filepath.Join(projectPath, "reliant.md"))
	return withLocalMemory(projectPath, projectMemoryWithForgeFramework(projectPath, md))
}

// collectRepoMemories reads reliant.md and reliant.local.md for each nested
// repo and returns a map[repoRelPath]->content suitable for the proto snapshot.
// The second return value is a deterministic byte summary for version hashing.
func collectRepoMemories(projectPath string) (map[string][]byte, []byte) {
	repoSources := discoverRepoSources(context.Background(), projectPath)
	if len(repoSources) == 0 {
		return nil, nil
	}

	sorted := append([]string(nil), repoSources...)
	sort.Strings(sorted)

	result := make(map[string][]byte, len(sorted))
	acc := strings.Builder{}

	// The Forge framework guide is ~18KB and the same for every repo one
	// forge renders it for, so it is carried once per forge per snapshot: by
	// the project root's memory when the root's forge renders it, otherwise
	// by the first forge repo in sorted order (the order the server emits
	// entries in). Repos pinned to different forges get different guides
	// (forgeFrameworkMemoryFrom asks each repo's pinned forge), and pointing
	// one at another's would hand it the wrong version's rules — so a repo
	// points only at a guide from the forge that renders its own.
	carriers := map[string]string{} // forge identity -> repo carrying its guide
	// Ask the same question buildProjectSnapshot does (rendered, not merely
	// forge.yaml present): a root forge.yaml that fails to render carries no
	// guide, and a pointer to it would name something the model never sees.
	_, rootForge, rootCarriesFramework := forgeFrameworkMemoryFrom(projectPath)
	// Byte-identical repo memories (e.g. two clones of one repo) collapse
	// to a pointer at the first repo carrying that content.
	seen := map[string]string{}

	for _, rel := range sorted {
		if rel == "" {
			continue
		}
		repoDir := filepath.Join(projectPath, rel)
		md, _ := readOptionalFile(filepath.Join(repoDir, "reliant.md"))
		if hasForgeYAML(repoDir) {
			if framework, forge, ok := forgeFrameworkMemoryFrom(repoDir); ok {
				switch carrier, carried := carriers[forge]; {
				case rootCarriesFramework && forge == rootForge:
					md = prependMemory([]byte("This repo is also a Forge project: the Forge framework guide in the project memory applies here too."), md)
				case carried:
					md = prependMemory([]byte(fmt.Sprintf("This repo is also a Forge project: the Forge framework guide included with the `%s` repo's memory applies here too.", carrier)), md)
				default:
					md = prependMemory(framework, md)
					carriers[forge] = rel
				}
			}
		}
		md = withLocalMemory(repoDir, md)
		if len(md) == 0 {
			continue
		}
		content := string(md)
		sum := hashBytes([]byte(content))
		if first, dup := seen[sum]; dup {
			content = fmt.Sprintf("This repo's memory is identical to the `%s` repo's memory; see that entry.", first)
		} else {
			seen[sum] = rel
		}
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

	// Each nested repo is its own discovery source, so a workspace holding
	// several clones of one repo (linked worktrees already collapse in
	// repo.Discover) — or several forge
	// projects, every one of which surfaces forge's whole embedded catalog —
	// yields the same skill once per checkout, differing only in Source.
	// Discovery keys them apart (NormalizedKey carries the source), but the
	// synced catalog is addressed by SkillPath alone: the skill tool has no
	// source parameter, so only the first copy of a path is ever loadable, and
	// the rest make every shorter spelling of it ambiguous. A prod workspace of
	// ~30 checkouts synced 23.6 MB of skills this way, which the server then
	// read and parsed on every LLM call.
	//
	// So a copy identical to one already indexed — same path, scope and
	// content — is dropped. Definitions are sorted by scope priority, then
	// path, so the copy kept is the one a load already resolved to (the
	// project root's when it has one). Copies that differ in content are all
	// kept: this removes repetition, never a distinct skill.
	type skillIdentity struct{ path, scope, hash string }
	indexed := make(map[skillIdentity]struct{}, len(snapshot.Definitions))

	for _, def := range snapshot.Definitions {
		h := hashBytes([]byte(def.Body), []byte(def.Description), []byte(def.SkillPath))
		id := skillIdentity{path: def.SkillPath, scope: string(def.Scope), hash: h}
		if _, dup := indexed[id]; dup {
			continue
		}
		indexed[id] = struct{}{}

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

// workflowSource is one workflows directory a project syncs: the project
// root's .reliant/workflows, or one nested repo's.
type workflowSource struct {
	// repo is the nested repo's slash-separated path relative to the project
	// root, "" for the project root itself.
	repo    string
	layout  *workflowref.Layout
	baseDir string
}

// readWorkflowSources reads the project's workflows directories — the project
// root's .reliant/workflows, then each nested repo's (the same repos
// reliant.md and skills are discovered in) — with the one locator the CLI
// uses too (workflowref.ReadLayout), so the server indexes exactly the
// workflows and scenarios `reliant workflow` sees on disk. The project root's
// source is always first; nested repos follow in path order. Which of them a
// bare name resolves to is the server's to decide (workflowref.Index).
func readWorkflowSources(ctx context.Context, projectPath string) ([]workflowSource, error) {
	root, err := readWorkflowSource(ctx, projectPath, "")
	if err != nil {
		return nil, err
	}
	sources := []workflowSource{root}
	repos := append([]string(nil), discoverRepoSources(ctx, projectPath)...)
	sort.Strings(repos)
	for _, rel := range repos {
		rel = filepath.ToSlash(filepath.Clean(rel))
		if rel == "" || rel == "." {
			continue
		}
		source, err := readWorkflowSource(ctx, projectPath, rel)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, nil
}

func readWorkflowSource(ctx context.Context, projectPath, repo string) (workflowSource, error) {
	sourceDir := filepath.Join(projectPath, filepath.FromSlash(repo))
	baseDir := filepath.Join(sourceDir, filepath.FromSlash(workflowref.Dir))
	layout, err := workflowref.ReadSourceLayout(ctx, sourceDir)
	if err != nil {
		return workflowSource{}, fmt.Errorf("read workflows in %s: %w", sourceDir, err)
	}
	return workflowSource{repo: repo, layout: layout, baseDir: baseDir}, nil
}

// indexWorkflows syncs every project workflow file of every source with its
// project-relative path and content. Problems (no name:, a name two files
// share, a name two repos share) are not resolved here: the server indexes the
// files with workflowref, as the CLI does, and reports them where a ref or the
// workflow list meets them.
func indexWorkflows(sources []workflowSource) ([]*reliantv1.IndexedWorkflow, []byte) {
	var results []*reliantv1.IndexedWorkflow
	acc := strings.Builder{}

	for _, src := range sources {
		for _, e := range src.layout.Workflows.Entries() {
			rel := workflowref.RelativePath(src.repo, e.Path)
			st, err := os.Stat(filepath.Join(src.baseDir, filepath.FromSlash(e.Path)))
			if err != nil {
				continue
			}
			h := hashBytes(e.Content)
			results = append(results, &reliantv1.IndexedWorkflow{
				Slug:         e.Slug,
				Name:         e.Name,
				RelativePath: rel,
				ContentHash:  h,
				MtimeUnixMs:  st.ModTime().UTC().UnixMilli(),
				YamlContent:  e.Content,
			})
			acc.WriteString(rel)
			acc.WriteString(":")
			acc.WriteString(h)
			acc.WriteString(";")
		}
	}
	return results, []byte(acc.String())
}

// indexPresets syncs the presets of the project root's .reliant/presets and of
// every nested repository's, as workflows are (readWorkflowSources), each with
// its project-relative path. A name two repos share is not resolved here: the
// server addresses a nested preset as "<repo>/<name>" and lets the root shadow
// the repos (config.FindStoredPresetByName).
func indexPresets(projectPath string, sources []workflowSource) ([]*reliantv1.IndexedPreset, []byte) {
	var results []*reliantv1.IndexedPreset
	acc := strings.Builder{}

	repos := []string{""}
	for _, src := range sources {
		if src.repo != "" {
			repos = append(repos, src.repo)
		}
	}
	for _, repo := range repos {
		baseDir := filepath.Join(projectPath, filepath.FromSlash(repo), ".reliant", "presets")
		for _, path := range listYAMLFiles(baseDir) {
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
	}

	return results, []byte(acc.String())
}

// indexScenarios syncs scenarios under the same address as their workflow.
// A cross-repo collision must not attach one repo's scenarios to another's.
func indexScenarios(sources []workflowSource) ([]*reliantv1.IndexedScenario, []byte) {
	root := sources[0]
	repos := map[string]*workflowref.Layout{}
	for _, src := range sources[1:] {
		repos[src.repo] = src.layout
	}
	layout := workflowref.MergeLayouts(root.layout, repos)
	baseDir := root.baseDir
	results := make([]*reliantv1.IndexedScenario, 0, len(layout.Scenarios))
	acc := strings.Builder{}

	for _, sc := range layout.Scenarios {
		rel := workflowref.RelativePath(sc.Repo, sc.Path)
		st, err := os.Stat(filepath.Join(baseDir, filepath.FromSlash(workflowref.FilePath(sc.Repo, sc.Path))))
		if err != nil {
			continue
		}
		h := hashBytes(sc.Content)
		results = append(results, &reliantv1.IndexedScenario{
			WorkflowSlug: sc.WorkflowSlug,
			Name:         sc.Name,
			RelativePath: rel,
			ContentHash:  h,
			MtimeUnixMs:  st.ModTime().UTC().UnixMilli(),
			YamlContent:  sc.Content,
		})
		acc.WriteString(rel)
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

	logging.WarnContext(ctx, logPrefix+" MCP server autoload completed with failures",
		"projectPath", projectPath,
		"failedServers", loadResult.FailedServers,
	)
	return projectPath
}
