// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	cfgpkg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/daemonstate"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

const (
	LOG_PREFIX_TOOLS_DAEMON = "[🔧 ToolsDaemon]"

	// Heartbeat interval for keeping connections alive.
	daemonHeartbeatInterval = 15 * time.Second

	// staleConnectionSweepInterval is how often the sweeper goroutine scans
	// the connections map for half-open (dead application stream, live TCP)
	// daemon connections.
	staleConnectionSweepInterval = 60 * time.Second

	// staleConnectionThreshold is how long a connection can be silent (no
	// heartbeats, no receives, no sends) before the sweeper treats it as
	// dead and removes it. Daemon heartbeat interval is 15s, so 90s = 6
	// missed heartbeats — comfortably beyond any plausible transient
	// network blip but well within human-noticeable latency.
	staleConnectionThreshold = 90 * time.Second
)

// ToolsDaemonService implements bidirectional streaming for tool execution.
// It manages daemon connections and routes tool requests/responses.
type ToolsDaemonService struct {
	reliantv1connect.UnimplementedToolsDaemonServiceHandler
	database db.Repository

	// Active daemon connections, keyed by daemonID.
	connections map[string]*daemonConnection
	// Secondary index: userID → list of connected daemonIDs.
	userDaemons map[string][]string
	mu          sync.RWMutex

	// listeners are notified (outside the mutex) when daemons connect/disconnect.
	listeners   []toolexec.DaemonConnectionListener
	listenersMu sync.RWMutex

	// userUpdateHub is used to publish ephemeral daemon heartbeat events
	// to the frontend via the streaming connection. Set via SetUserUpdateHub.
	userUpdateHub streaming.UpdateHub[db.UserUpdate]

	// statePublisher emits daemon.v1.state.* lifecycle events. Set via
	// SetStatePublisher; nil-safe (calls on a nil publisher are no-ops).
	statePublisher *daemonstate.Publisher

	// now returns the current time. Overridable for tests; defaults to
	// time.Now (UTC). Used by the stale-connection sweeper so tests can
	// jump the clock without sleeping.
	now func() time.Time
}

// daemonStream abstracts the send/receive operations for a daemon connection.
// This allows both inbound (ConnectDaemon) and outbound (ConnectGateway) streams
// to be handled uniformly.
type daemonStream interface {
	Send(msg *reliantv1.ServerMessage) error
	Receive() (*reliantv1.DaemonMessage, error)
}

// inboundStream wraps a connect.BidiStream (server-side, inbound daemon connection).
type inboundStream struct {
	stream *connect.BidiStream[reliantv1.DaemonMessage, reliantv1.ServerMessage]
}

func (s *inboundStream) Send(msg *reliantv1.ServerMessage) error    { return s.stream.Send(msg) }
func (s *inboundStream) Receive() (*reliantv1.DaemonMessage, error) { return s.stream.Receive() }

// outboundStream wraps a connect.BidiStreamForClient (client-side, outbound gateway connection).
type outboundStream struct {
	stream *connect.BidiStreamForClient[reliantv1.ServerMessage, reliantv1.DaemonMessage]
}

func (s *outboundStream) Send(msg *reliantv1.ServerMessage) error    { return s.stream.Send(msg) }
func (s *outboundStream) Receive() (*reliantv1.DaemonMessage, error) { return s.stream.Receive() }

// daemonConnection represents an active daemon connection
type daemonConnection struct {
	userID       string
	daemonID     string
	name         string
	labels       map[string]string
	daemonType   string // "local" or "cloud"
	connectedAt  time.Time
	lastActivity time.Time
	stream       daemonStream
	sendCh       chan *reliantv1.ServerMessage
	done         chan struct{}
	doneOnce     sync.Once

	// pendingCommands tracks in-flight DaemonCommandRequests awaiting responses.
	// Key is request_id, value is a channel that receives the response.
	pendingCommandsMu sync.Mutex
	pendingCommands   map[string]chan *reliantv1.DaemonCommandResponse

	// terminalSubs tracks subscribers for terminal output, keyed by sessionID.
	terminalSubsMu sync.Mutex
	terminalSubs   map[string][]chan *toolexec.TerminalOutputEvent

	// pendingToolRequests tracks in-flight synchronous tool requests awaiting responses.
	// Key is request_id, value is a channel that receives the response.
	pendingToolRequestsMu sync.Mutex
	pendingToolRequests   map[string]chan *toolexec.ToolExecutionResponse

	// processOutputSubs tracks subscribers for process output, keyed by processID.
	processOutputSubsMu sync.Mutex
	processOutputSubs   map[string][]chan *toolexec.ProcessOutputEvent

	// lastDetectedPortsKey is the encoding of the last heartbeat-reported
	// detected-ports set persisted to daemon_attachment. Heartbeats arrive
	// every 15s but the port set rarely changes; caching the last write
	// avoids a per-heartbeat UPDATE. Only the receive loop touches it.
	lastDetectedPortsKey string
}

// NewToolsDaemonService creates a new ToolsDaemonService.
// Daemon connection routability is now derived from the daemon_attachment table,
// so no background sweeper goroutine is required.
func NewToolsDaemonService(database db.Repository) *ToolsDaemonService {
	return &ToolsDaemonService{
		database:    database,
		connections: make(map[string]*daemonConnection),
		userDaemons: make(map[string][]string),
		now:         func() time.Time { return time.Now().UTC() },
	}
}

// NewToolsDaemonServiceWithoutMonitor is retained as an alias for callers
// that previously opted out of the stale-daemon monitor. The monitor has been
// removed entirely; this now behaves identically to NewToolsDaemonService.
func NewToolsDaemonServiceWithoutMonitor(database db.Repository) *ToolsDaemonService {
	return NewToolsDaemonService(database)
}

// SetUserUpdateHub configures the hub used to publish ephemeral daemon
// heartbeat events to the frontend streaming connection.
func (s *ToolsDaemonService) SetUserUpdateHub(hub streaming.UpdateHub[db.UserUpdate]) {
	s.userUpdateHub = hub
}

// SetStatePublisher wires the daemon lifecycle state publisher. Optional;
// if unset, lifecycle events are simply not emitted (the existing hand-rolled
// daemonevents publisher still covers the connect/disconnect path until
// Step 4 of the simplification proposal lands).
func (s *ToolsDaemonService) SetStatePublisher(p *daemonstate.Publisher) {
	s.statePublisher = p
}

// Close stops background workers owned by the daemon service.
// Kept for API stability. The stale-connection sweeper goroutine started by
// Start() is bound to the context passed to Start, not to Close — cancel
// that context to stop it.
func (s *ToolsDaemonService) Close() {}

// Start launches background goroutines owned by the service. Currently this
// is just the stale-connection sweeper, which periodically scans the
// connections map for half-open daemon streams (TCP socket alive but the
// application-level bidi stream is dead — observed in production when a
// gateway → workspace-pod connection stays ESTABLISHED long after the
// daemon's gRPC stream ended).
//
// Returns when ctx is cancelled. Safe to call once; the caller is
// responsible for running this in its own goroutine.
func (s *ToolsDaemonService) Start(ctx context.Context) {
	ticker := time.NewTicker(staleConnectionSweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepStaleConnections()
		}
	}
}

// sweepStaleConnections removes daemon connections whose lastActivity is
// older than staleConnectionThreshold, provided the connection has been
// registered for at least staleConnectionThreshold (so we don't reap a
// brand-new connection that hasn't sent its first message yet).
//
// For each reaped connection: fires OnDaemonDisconnected listeners, closes
// done + subscribers, and deletes the daemon_attachment row — same as a
// normal disconnect, just driven by the sweeper rather than the stream's
// receive-loop returning.
func (s *ToolsDaemonService) sweepStaleConnections() {
	now := s.now()
	threshold := staleConnectionThreshold

	// Collect stale connections under the read lock, then release before
	// calling teardownConnection (which acquires the write lock + fires
	// listeners outside it).
	var stale []*daemonConnection
	s.mu.RLock()
	for _, conn := range s.connections {
		if now.Sub(conn.connectedAt) < threshold {
			// Don't reap a freshly-registered connection that hasn't had a
			// chance to send anything yet.
			continue
		}
		if now.Sub(conn.lastActivity) <= threshold {
			continue
		}
		stale = append(stale, conn)
	}
	s.mu.RUnlock()

	for _, conn := range stale {
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Reaping stale daemon connection",
			"daemonID", conn.daemonID,
			"userID", conn.userID,
			"lastActivity", conn.lastActivity,
			"silentFor", now.Sub(conn.lastActivity).Round(time.Second),
		)
		s.teardownConnection(conn, "stale-connection sweeper")
	}
}

// AddConnectionListener registers a listener that will be notified when
// daemon connections are established or torn down. Safe to call before Start.
func (s *ToolsDaemonService) AddConnectionListener(l toolexec.DaemonConnectionListener) {
	s.listenersMu.Lock()
	s.listeners = append(s.listeners, l)
	s.listenersMu.Unlock()
}

// notifyConnected calls OnDaemonConnected on all registered listeners.
// Must be called OUTSIDE s.mu to avoid deadlocks.
func (s *ToolsDaemonService) notifyConnected(userID, daemonID string) {
	s.listenersMu.RLock()
	defer s.listenersMu.RUnlock()
	for _, l := range s.listeners {
		l.OnDaemonConnected(userID, daemonID)
	}
}

// notifyDisconnected calls OnDaemonDisconnected on all registered listeners.
// Must be called OUTSIDE s.mu to avoid deadlocks.
func (s *ToolsDaemonService) notifyDisconnected(userID, daemonID string) {
	s.listenersMu.RLock()
	defer s.listenersMu.RUnlock()
	for _, l := range s.listeners {
		l.OnDaemonDisconnected(userID, daemonID)
	}
}

// removeConnection removes the given daemonID from s.connections and
// s.userDaemons, but ONLY if the currently-registered connection pointer
// matches `expected`. This avoids racing a fresh reconnect that has already
// taken over the slot. Returns true if a removal happened; the caller is
// responsible for firing OnDaemonDisconnected listeners and closing the
// connection's done/subscriber channels.
//
// Passing expected == nil unconditionally removes whatever is at the key —
// only the sweeper should do this.
func (s *ToolsDaemonService) removeConnection(daemonID, userID string, expected *daemonConnection) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.connections[daemonID]
	if !ok {
		return false
	}
	if expected != nil && cur != expected {
		// The slot has been taken over by a newer connection (reconnect).
		// Leave it alone.
		return false
	}
	delete(s.connections, daemonID)
	ids := s.userDaemons[userID]
	for i, id := range ids {
		if id == daemonID {
			s.userDaemons[userID] = append(ids[:i], ids[i+1:]...)
			break
		}
	}
	if len(s.userDaemons[userID]) == 0 {
		delete(s.userDaemons, userID)
	}
	return true
}

// teardownConnection performs the full disconnect path for a daemon
// connection: remove from the connections/userDaemons maps, close the
// connection's done channel + subscribers, fire OnDaemonDisconnected
// listeners, and best-effort delete the daemon_attachment row.
//
// Idempotent and safe to call multiple times (e.g. from both a `defer` in
// the stream handler AND from the stale-connection sweeper) — only the
// first call that finds the connection still registered actually does the
// work.
func (s *ToolsDaemonService) teardownConnection(conn *daemonConnection, reason string) {
	if conn == nil {
		return
	}
	userID := conn.userID
	daemonID := conn.daemonID
	daemonType := conn.daemonType

	removed := s.removeConnection(daemonID, userID, conn)
	// Always close done + subscribers so any goroutines waiting on this
	// specific connection unblock, even if the slot was already taken over.
	conn.closeDone()
	conn.closeAllSubscribers()

	if removed {
		s.notifyDisconnected(userID, daemonID)
		s.statePublisher.Disconnected(daemonID, userID, daemonType)
		if err := s.database.DeleteDaemonAttachment(context.Background(), daemonID); err != nil {
			logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Failed to delete daemon attachment", "error", err, "daemonID", daemonID, "reason", reason)
		}
	}
}

// DaemonStatus implements daemonquery.StatusSource. Returns the connection's
// lastActivity timestamp and daemonType if this gateway currently holds a
// stream for the daemon. ok=false means no connection — the caller's NATS
// status query will time out and the caller will treat the daemon as
// disconnected, which is correct.
//
// Reads from the connections map under the read lock; safe for high QPS as
// long as listeners don't fan out into it under the write lock (they don't).
func (s *ToolsDaemonService) DaemonStatus(daemonID string) (lastActive time.Time, daemonType string, ok bool) {
	s.mu.RLock()
	conn, found := s.connections[daemonID]
	s.mu.RUnlock()
	if !found {
		return time.Time{}, "", false
	}
	return conn.lastActivity, conn.daemonType, true
}

// TouchDaemonsForUser bumps lastActivity on every daemon connection currently
// held for the given user. Used by the cross-process activity ping (the
// control-plane LLM proxy publishes a NATS message when it forwards a call so
// the daemon's status query reflects real usage, not just heartbeat cadence).
func (s *ToolsDaemonService) TouchDaemonsForUser(userID string) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, daemonID := range s.userDaemons[userID] {
		if conn, ok := s.connections[daemonID]; ok {
			conn.lastActivity = now
		}
	}
}

// publishDaemonHeartbeat publishes an ephemeral daemon heartbeat event
// through the UserUpdateHub so the frontend knows the daemon is alive.
// This is NOT persisted to the database — it's a fire-and-forget notification.
// When the heartbeat carries workspace memory telemetry (cloud daemons in a
// cgroup-limited pod), it is included so the UI can react in real time.
func (s *ToolsDaemonService) publishDaemonHeartbeat(_ context.Context, userID, daemonID string, ts time.Time, hb *reliantv1.DaemonHeartbeat) {
	if s.userUpdateHub == nil {
		return
	}
	payload := map[string]interface{}{
		"daemon_id":      daemonID,
		"last_heartbeat": ts.Unix(),
	}
	if hb != nil && hb.MemoryLimitBytes > 0 {
		payload["memory_used_bytes"] = hb.MemoryUsedBytes
		payload["memory_limit_bytes"] = hb.MemoryLimitBytes
		payload["memory_pressure"] = hb.MemoryPressure
	}
	if hb != nil {
		// Always included (may be empty) so the UI can clear its preview
		// affordance when the last detected listener goes away.
		ports := hb.DetectedPorts
		if ports == nil {
			ports = []uint32{}
		}
		payload["detected_ports"] = ports
	}
	data, _ := json.Marshal(payload)
	s.userUpdateHub.Publish(context.Background(), streaming.UpdateEvent[db.UserUpdate]{
		Key: userID,
		Payload: db.UserUpdate{
			UserID:     userID,
			UpdateType: db.UserUpdateDaemonHeartbeat,
			EntityType: db.EntityTypeSystem,
			EntityID:   daemonID,
			Data:       data,
			CreatedAt:  ts,
		},
	})
}

func (c *daemonConnection) closeDone() {
	if c == nil {
		return
	}
	c.doneOnce.Do(func() {
		close(c.done)
	})
}

func daemonRegistrationUserID(ctx context.Context, reg *reliantv1.DaemonRegister) (string, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || strings.TrimSpace(userID) == "" {
		return "", connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("PAT authentication required — no user identity in context"))
	}
	return userID, nil
}

// daemonHostnameLookup is the subset of db.Repository needed to find an
// existing daemon row by (userID, hostname). Kept narrow so the
// hostname-reuse helper is trivially testable with a fake.
type daemonHostnameLookup interface {
	ListDaemonsByUserID(ctx context.Context, userID string) ([]*db.Daemon, error)
}

// resolveDaemonID applies the daemon-id precedence for a registering daemon:
//
//  1. patBoundID — the PAT itself pins a specific daemon. Most authoritative;
//     used verbatim when present.
//  2. assertedID — the client-asserted stable id from the register message.
//     Daemons persist their server-assigned id per-origin (in daemon.json) and
//     re-assert it on every reconnect, so identity survives daemon restarts and
//     machine hostname changes (macOS flipping between *.lan and *.local).
//     Trusted verbatim, but only for unbound PATs.
//  3. resolveUnbound() — hostname-based resolution, evaluated lazily only when
//     neither id is present. Kept as a fallback so pre-update daemons (which
//     don't send a stable id yet) still reuse their existing row.
//
// resolveUnbound is a thunk so the (DB-touching) hostname lookup is skipped
// entirely whenever a higher-precedence id is available.
func resolveDaemonID(patBoundID, assertedID string, resolveUnbound func() string) string {
	if id := strings.TrimSpace(patBoundID); id != "" {
		return id
	}
	if id := strings.TrimSpace(assertedID); id != "" {
		logging.Info(LOG_PREFIX_TOOLS_DAEMON+" Trusting client-asserted stable daemon_id for unbound PAT",
			"daemonID", id)
		return id
	}
	return resolveUnbound()
}

// resolveUnboundDaemonID picks the daemon_id to use when the authenticating
// PAT is not bound to a specific daemon (the common path for external /
// self-hosted daemons). It reuses the existing (userID, hostname) row if
// present so reconnects don't accumulate ghost rows — every reconnect
// minting a fresh ID would let the daemons table grow without bound and
// the frontend's status==ACTIVE filter would never lock onto a stable row.
//
// Falls back to a freshly-minted UUID only when there's no prior row for
// this hostname or when hostname is empty (older daemons that don't send it).
// Lookup failures degrade to a new UUID so a transient DB error never blocks
// registration — the alternative is to fail the connection, which is worse
// than mis-attributing one row that the next reconnect will reuse.
func resolveUnboundDaemonID(ctx context.Context, repo daemonHostnameLookup, userID, rawHostname string) string {
	hostname := strings.TrimSpace(rawHostname)
	if hostname != "" && repo != nil {
		existing, lookupErr := repo.ListDaemonsByUserID(ctx, userID)
		if lookupErr != nil {
			logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Failed to lookup existing daemons for hostname reuse",
				"error", lookupErr, "userID", userID, "hostname", hostname)
		} else {
			for _, d := range existing {
				if d.Hostname != nil && *d.Hostname == hostname {
					logging.Info(LOG_PREFIX_TOOLS_DAEMON+" Reusing existing daemon_id for unbound PAT",
						"daemonID", d.ID, "userID", userID, "hostname", hostname)
					return d.ID
				}
			}
		}
	}
	id := uuid.NewString()
	logging.Info(LOG_PREFIX_TOOLS_DAEMON+" Generated daemon_id for unbound PAT",
		"daemonID", id, "userID", userID, "hostname", hostname)
	return id
}

// ConnectDaemon implements the bidirectional streaming RPC for daemon connections
func (s *ToolsDaemonService) ConnectDaemon(
	ctx context.Context,
	stream *connect.BidiStream[reliantv1.DaemonMessage, reliantv1.ServerMessage],
) error {
	// Wait for registration message
	msg, err := stream.Receive()
	if err != nil {
		logging.Error(LOG_PREFIX_TOOLS_DAEMON+" Failed to receive registration", "error", err)
		return err
	}

	reg := msg.GetRegister()
	if reg == nil {
		logging.Error(LOG_PREFIX_TOOLS_DAEMON + " First message must be registration")
		return connect.NewError(connect.CodeInvalidArgument, nil)
	}

	userID, err := daemonRegistrationUserID(ctx, reg)
	if err != nil {
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Failed daemon registration identity validation",
			"error", err,
		)
		return err
	}

	daemonID := resolveDaemonID(
		auth.GetDaemonIDFromContext(ctx),
		reg.GetDaemonId(),
		func() string {
			return resolveUnboundDaemonID(ctx, s.database, userID, reg.GetHostname())
		},
	)

	requestedProjectPaths, err := s.listAllProjectPaths(ctx, userID)
	if err != nil {
		logging.Error(LOG_PREFIX_TOOLS_DAEMON+" Failed to list project paths", "error", err, "userID", userID)
		return connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list project paths: %w", err))
	}

	now := time.Now().UTC()
	capabilitiesJSON, err := jsonStringSlicePtr(reg.Capabilities)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("failed to encode capabilities: %w", err))
	}
	projectPathsJSON, err := jsonStringSlicePtr(requestedProjectPaths)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("failed to encode project paths: %w", err))
	}

	if err := s.database.UpsertDaemon(ctx, &db.Daemon{
		ID:           daemonID,
		UserID:       userID,
		Hostname:     daemonStringPtrOrNil(reg.Hostname),
		Platform:     daemonStringPtrOrNil(reg.Platform),
		Capabilities: capabilitiesJSON,
		ProjectPaths: projectPathsJSON,
		DaemonType:   normalizeRegisteredDaemonType(reg.GetDaemonType()),
	}); err != nil {
		logging.Error(LOG_PREFIX_TOOLS_DAEMON+" Failed to persist daemon registration", "error", err, "daemonID", daemonID)
		return connect.NewError(connect.CodeInternal, fmt.Errorf("failed to persist daemon registration: %w", err))
	}

	if err := s.database.UpsertDaemonAttachment(ctx, &db.DaemonAttachment{
		DaemonID:           daemonID,
		UserID:             userID,
		Source:             db.DaemonAttachmentSourceInbound,
		AttachedAt:         now,
		LastStreamActivity: now,
	}); err != nil {
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Failed to upsert daemon attachment", "error", err, "daemonID", daemonID)
	}

	logging.Info(LOG_PREFIX_TOOLS_DAEMON+" Daemon registered",
		"userID", userID,
		"daemonID", daemonID,
		"hostname", reg.Hostname,
		"platform", reg.Platform)

	// Create daemon connection
	now2 := time.Now().UTC()
	conn := &daemonConnection{
		userID:              userID,
		daemonID:            daemonID,
		name:                reg.GetName(),
		labels:              reg.GetLabels(),
		daemonType:          reg.GetDaemonType(),
		connectedAt:         now2,
		lastActivity:        now2,
		stream:              &inboundStream{stream: stream},
		sendCh:              make(chan *reliantv1.ServerMessage, 256),
		done:                make(chan struct{}),
		pendingCommands:     make(map[string]chan *reliantv1.DaemonCommandResponse),
		pendingToolRequests: make(map[string]chan *toolexec.ToolExecutionResponse),
		terminalSubs:        make(map[string][]chan *toolexec.TerminalOutputEvent),
		processOutputSubs:   make(map[string][]chan *toolexec.ProcessOutputEvent),
	}

	// Register connection: keyed by daemonID, with secondary index by userID.
	s.mu.Lock()
	oldConn := s.connections[daemonID]
	s.connections[daemonID] = conn
	if oldConn == nil {
		// New daemon — add to user's daemon list.
		s.userDaemons[userID] = append(s.userDaemons[userID], daemonID)
	}
	s.mu.Unlock()

	// Close old connection if this daemon was already connected (reconnect).
	if oldConn != nil {
		oldConn.closeDone()
		logging.Info(LOG_PREFIX_TOOLS_DAEMON+" Replaced old daemon connection", "userID", userID, "daemonID", daemonID)
	}

	// Notify listeners outside the mutex.
	s.notifyConnected(userID, daemonID)
	s.statePublisher.Connected(daemonID, userID, conn.daemonType)

	// Publish an immediate heartbeat so the frontend knows the daemon is
	// online without waiting for the first periodic heartbeat (up to 15s).
	s.publishDaemonHeartbeat(context.Background(), userID, daemonID, time.Now().UTC(), nil)

	// Send cloud-refactor registration acknowledgment containing config pull hints.
	regAck := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_RegistrationAck{
			RegistrationAck: &reliantv1.RegistrationAck{
				Accepted:              true,
				RequestedProjectPaths: requestedProjectPaths,
				DaemonId:              daemonID,
				UserId:                userID,
			},
		},
	}
	if err := stream.Send(regAck); err != nil {
		logging.Error(LOG_PREFIX_TOOLS_DAEMON+" Failed to send registration_ack", "error", err)
		return err
	}

	// Start sender goroutine.
	go s.runSender(conn)

	// Start heartbeat goroutine
	go s.runHeartbeat(conn)

	// Reconcile project_daemons rows against what actually exists on this
	// daemon's disk. Runs off the hot connect path so it never blocks
	// registration; self-heals legacy rows, daemon-id churn, and the
	// create-while-daemon-pending case. See reconcileProjectDaemonsOnConnect.
	go s.reconcileProjectDaemonsOnConnect(conn)

	for _, projectPath := range requestedProjectPaths {
		if err := s.sendLoadAndWatchProjectConfig(ctx, conn, projectPath, true); err != nil {
			logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Failed to request initial project config load/watch",
				"error", err,
				"daemonID", conn.daemonID,
				"projectPath", projectPath,
			)
		}
	}

	// Cleanup on disconnect via defer so it runs even on panic from the
	// receive loop. Idempotent — safe if the sweeper or a reconnect-driven
	// replace already removed the slot.
	defer func() {
		s.teardownConnection(conn, "inbound stream ended")
		logging.Info(LOG_PREFIX_TOOLS_DAEMON+" Daemon disconnected", "userID", userID, "daemonID", daemonID)
	}()

	// Handle incoming messages (blocking)
	return s.handleIncoming(ctx, conn)
}

// Repo returns the underlying repository. Used by the daemon connector for resync queries.
func (s *ToolsDaemonService) Repo() db.Repository {
	return s.database
}

// RegisterOutboundConnection registers an outbound gateway→daemon connection.
// The DaemonConnector calls this after opening a ConnectGateway bidi stream and
// receiving the DaemonRegister message. Identity (userID, daemonID) is provided
// by the caller (from the NATS command), not from the daemon's register message.
// The caller is responsible for running the receive loop via HandleIncomingLoop.
func (s *ToolsDaemonService) RegisterOutboundConnection(
	ctx context.Context,
	userID, daemonID, podIP string,
	podPort int,
	reg *reliantv1.DaemonRegister,
	stream *connect.BidiStreamForClient[reliantv1.ServerMessage, reliantv1.DaemonMessage],
) (*OutboundConn, error) {
	if daemonID == "" || userID == "" {
		return nil, fmt.Errorf("missing daemonID or userID")
	}

	now := time.Now().UTC()
	capabilitiesJSON, err := jsonStringSlicePtr(reg.Capabilities)
	if err != nil {
		return nil, fmt.Errorf("failed to encode capabilities: %w", err)
	}

	if err := s.database.UpsertDaemon(ctx, &db.Daemon{
		ID:           daemonID,
		UserID:       userID,
		Hostname:     daemonStringPtrOrNil(reg.Hostname),
		Platform:     daemonStringPtrOrNil(reg.Platform),
		Capabilities: capabilitiesJSON,
		DaemonType:   normalizeRegisteredDaemonType(reg.GetDaemonType()),
	}); err != nil {
		return nil, fmt.Errorf("failed to persist daemon registration: %w", err)
	}

	if err := s.database.UpsertDaemonAttachment(ctx, &db.DaemonAttachment{
		DaemonID:           daemonID,
		UserID:             userID,
		Source:             db.DaemonAttachmentSourceOutbound,
		PodIP:              &podIP,
		PodPort:            &podPort,
		AttachedAt:         now,
		LastStreamActivity: now,
	}); err != nil {
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Failed to upsert daemon attachment", "error", err, "daemonID", daemonID)
	}

	conn := &daemonConnection{
		userID:              userID,
		daemonID:            daemonID,
		name:                reg.GetName(),
		labels:              reg.GetLabels(),
		daemonType:          reg.GetDaemonType(),
		connectedAt:         now,
		lastActivity:        now,
		stream:              &outboundStream{stream: stream},
		sendCh:              make(chan *reliantv1.ServerMessage, 256),
		done:                make(chan struct{}),
		pendingCommands:     make(map[string]chan *reliantv1.DaemonCommandResponse),
		pendingToolRequests: make(map[string]chan *toolexec.ToolExecutionResponse),
		terminalSubs:        make(map[string][]chan *toolexec.TerminalOutputEvent),
		processOutputSubs:   make(map[string][]chan *toolexec.ProcessOutputEvent),
	}

	// Register connection.
	s.mu.Lock()
	oldConn := s.connections[daemonID]
	s.connections[daemonID] = conn
	if oldConn == nil {
		s.userDaemons[userID] = append(s.userDaemons[userID], daemonID)
	}
	s.mu.Unlock()

	if oldConn != nil {
		oldConn.closeDone()
		logging.Info(LOG_PREFIX_TOOLS_DAEMON+" Replaced old daemon connection (outbound)", "userID", userID, "daemonID", daemonID)
	}

	s.notifyConnected(userID, daemonID)
	s.statePublisher.Connected(daemonID, userID, conn.daemonType)
	s.publishDaemonHeartbeat(context.Background(), userID, daemonID, time.Now().UTC(), nil)

	// Start sender and heartbeat goroutines.
	go s.runSender(conn)
	go s.runHeartbeat(conn)

	return &OutboundConn{service: s, conn: conn}, nil
}

// OutboundConn wraps a daemonConnection created via RegisterOutboundConnection.
// It exposes HandleIncoming (blocking receive loop) and Disconnect (cleanup).
type OutboundConn struct {
	service *ToolsDaemonService
	conn    *daemonConnection
}

// HandleIncoming runs the blocking receive loop, dispatching incoming daemon
// messages exactly like ConnectDaemon does. Returns when the stream ends.
func (o *OutboundConn) HandleIncoming(ctx context.Context) error {
	return o.service.handleIncoming(ctx, o.conn)
}

// Disconnect removes the connection from the service's internal state and
// notifies listeners. Must be called when the stream ends. Idempotent —
// safe to call multiple times (e.g. from a `defer` in the caller AND from
// the stale-connection sweeper).
func (o *OutboundConn) Disconnect() {
	o.service.teardownConnection(o.conn, "outbound stream ended")
}

// handleIncoming handles incoming messages from the daemon
func (s *ToolsDaemonService) handleIncoming(ctx context.Context, conn *daemonConnection) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-conn.done:
			return nil
		default:
		}

		msg, err := conn.stream.Receive()
		if err != nil {
			return err
		}

		now := time.Now().UTC()
		s.mu.Lock()
		conn.lastActivity = now
		s.mu.Unlock()
		// conn.lastActivity (above) feeds the dead-connection sweeper and MUST
		// bump on every message, heartbeats included. But workspace idle-suspend
		// keys off statePublisher.Activity -> Workspace.Status.LastActivity, and a
		// heartbeat is the daemon's own 15s keepalive, NOT user activity. Counting
		// heartbeats as activity refreshes LastActivity forever, so the idle reaper
		// never fires and the workspace never auto-suspends. Only publish real,
		// user-driven inbound traffic as activity.
		if _, isHeartbeat := msg.Message.(*reliantv1.DaemonMessage_Heartbeat); !isHeartbeat {
			s.statePublisher.Activity(conn.daemonID, conn.userID, conn.daemonType)
		}

		switch m := msg.Message.(type) {
		case *reliantv1.DaemonMessage_ToolResponse:
			if resp := m.ToolResponse; resp != nil {
				conn.pendingToolRequestsMu.Lock()
				ch, ok := conn.pendingToolRequests[resp.RequestId]
				if ok {
					delete(conn.pendingToolRequests, resp.RequestId)
				}
				conn.pendingToolRequestsMu.Unlock()
				if ok {
					ch <- &toolexec.ToolExecutionResponse{
						RequestID:    resp.RequestId,
						Success:      resp.Success,
						IsError:      resp.IsError,
						Content:      resp.Content,
						Metadata:     resp.Metadata,
						ErrorMessage: resp.ErrorMessage,
						ErrorCode:    resp.ErrorCode,
						Backgrounded: resp.Backgrounded,
					}
				}
			}

		case *reliantv1.DaemonMessage_Heartbeat:
			s.publishDaemonHeartbeat(ctx, conn.userID, conn.daemonID, time.Now().UTC(), m.Heartbeat)
			// Renew the reachability lease (last_stream_activity) on the
			// keepalive so an idle-but-connected daemon doesn't decay to
			// "offline". We write daemon_attachment DIRECTLY here (the gateway
			// already owns direct writes to this table — see teardownConnection's
			// DeleteDaemonAttachment) instead of publishing a daemonstate event.
			// A dedicated EventHeartbeat would land on the shared daemon.v1.state.*
			// stream, whose authoritative consumer lives in the control-plane repo
			// and STRICTLY rejects unknown event types ("daemon state event unknown
			// type") — so it would both error-spam and, worse, drop the event
			// without renewing the lease. A direct touch renews the lease
			// unconditionally and never feeds the workspace idle-suspend timer
			// (which keys off EventActivity), preserving the exclusion above.
			if err := s.database.TouchDaemonAttachmentIfNewer(ctx, conn.daemonID, time.Now().UTC()); err != nil {
				logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Failed to renew daemon reachability lease", "error", err, "daemonID", conn.daemonID)
			}
			// Persist heartbeat-carried workspace memory telemetry on the
			// attachment record so the daemon registry (and the UI behind it)
			// can surface memory pressure. limit==0 means the daemon has no
			// cgroup accounting (local/mac) — nothing to record.
			if hb := m.Heartbeat; hb != nil && hb.MemoryLimitBytes > 0 {
				if err := s.database.UpdateDaemonAttachmentMemory(ctx, conn.daemonID,
					int64(hb.MemoryUsedBytes), int64(hb.MemoryLimitBytes), hb.MemoryPressure); err != nil {
					logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Failed to record daemon memory telemetry", "error", err, "daemonID", conn.daemonID)
				}
			}
			// Persist heartbeat-carried detected listener ports on the
			// attachment record (same flow as the memory telemetry above) so
			// the daemon registry can surface preview affordances. Written
			// only when the set changed — heartbeats are 15s apart but port
			// churn is rare. UpsertDaemonAttachment resets the column on
			// re-attach, matching the empty initial cache key here.
			if hb := m.Heartbeat; hb != nil {
				portsKey := fmt.Sprint(hb.DetectedPorts)
				if portsKey != conn.lastDetectedPortsKey {
					if err := s.database.UpdateDaemonAttachmentPorts(ctx, conn.daemonID, hb.DetectedPorts); err != nil {
						logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Failed to record daemon detected ports", "error", err, "daemonID", conn.daemonID)
					} else {
						conn.lastDetectedPortsKey = portsKey
					}
				}
			}

		case *reliantv1.DaemonMessage_ProjectDiscovery:
			if err := s.handleProjectDiscovery(ctx, conn, m.ProjectDiscovery); err != nil {
				logging.Error(LOG_PREFIX_TOOLS_DAEMON+" Failed to handle project discovery", "error", err, "daemonID", conn.daemonID)
			}

		case *reliantv1.DaemonMessage_LoadProjectConfigsResponse:
			if err := s.handleLoadProjectConfigsResponse(ctx, conn, m.LoadProjectConfigsResponse); err != nil {
				logging.Error(LOG_PREFIX_TOOLS_DAEMON+" Failed to handle load project configs response", "error", err, "daemonID", conn.daemonID)
			}

		case *reliantv1.DaemonMessage_ProjectConfigDelta:
			if err := s.handleProjectConfigDelta(ctx, conn, m.ProjectConfigDelta); err != nil {
				logging.Error(LOG_PREFIX_TOOLS_DAEMON+" Failed to handle project config delta", "error", err, "daemonID", conn.daemonID)
			}

		case *reliantv1.DaemonMessage_KillProcessResponse:
			if resp := m.KillProcessResponse; resp != nil {
				if resp.Success {
					logging.Info(LOG_PREFIX_TOOLS_DAEMON+" Kill process succeeded", "processID", resp.ProcessId)
				} else {
					logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Kill process failed", "processID", resp.ProcessId, "error", resp.ErrorMessage)
				}
			}

		case *reliantv1.DaemonMessage_DaemonCommandResponse:
			if resp := m.DaemonCommandResponse; resp != nil {
				conn.pendingCommandsMu.Lock()
				ch, ok := conn.pendingCommands[resp.RequestId]
				if ok {
					delete(conn.pendingCommands, resp.RequestId)
				}
				conn.pendingCommandsMu.Unlock()
				if ok {
					ch <- resp
				} else {
					logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Received daemon command response with no pending request",
						"requestID", resp.RequestId, "commandType", resp.CommandType)
				}
			}

		case *reliantv1.DaemonMessage_TerminalOutput:
			if out := m.TerminalOutput; out != nil {
				evt := &toolexec.TerminalOutputEvent{
					SessionID: out.GetSessionId(),
					Data:      out.GetData(),
				}
				conn.dispatchTerminalEvent(evt)
			}

		case *reliantv1.DaemonMessage_TerminalSessionEvent:
			if evt := m.TerminalSessionEvent; evt != nil {
				outEvt := &toolexec.TerminalOutputEvent{
					SessionID: evt.GetSessionId(),
				}
				switch evt.GetEventType() {
				case reliantv1.TerminalSessionEvent_EVENT_TYPE_CLOSED:
					outEvt.Closed = true
				case reliantv1.TerminalSessionEvent_EVENT_TYPE_ERROR:
					outEvt.Error = evt.GetMessage()
				}
				conn.dispatchTerminalEvent(outEvt)
			}

		case *reliantv1.DaemonMessage_ProcessOutputChunk:
			if chunk := m.ProcessOutputChunk; chunk != nil {
				evt := &toolexec.ProcessOutputEvent{
					ProcessID:  chunk.GetProcessId(),
					Data:       chunk.GetData(),
					Stream:     chunk.GetStream(),
					Sequence:   chunk.GetSequence(),
					IsComplete: chunk.GetIsComplete(),
					ExitCode:   chunk.GetExitCode(),
				}
				conn.dispatchProcessOutputEvent(evt)
			}

		case *reliantv1.DaemonMessage_FileSystemChanged:
			if err := s.handleFileSystemChanged(ctx, conn, m.FileSystemChanged); err != nil {
				logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Failed to handle filesystem changed", "error", err)
			}

		default:
			logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Unknown message type", "userID", conn.userID)
		}
	}
}

// ReportToolResult is a unary RPC handler that was previously used by daemons
// to report tool execution results when the bidirectional stream was unavailable.
// This path has been removed in favour of the bidi stream.
func (s *ToolsDaemonService) ReportToolResult(ctx context.Context, req *connect.Request[reliantv1.ReportToolResultRequest]) (*connect.Response[reliantv1.ReportToolResultResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("ReportToolResult is no longer supported; use the bidi stream"))
}

func (s *ToolsDaemonService) handleProjectDiscovery(ctx context.Context, conn *daemonConnection, discovery *reliantv1.ProjectDiscovery) error {
	if discovery == nil {
		return nil
	}

	paths := make([]string, 0, len(discovery.Projects))
	projectsByPath := make(map[string]*reliantv1.DiscoveredProject, len(discovery.Projects))
	seen := make(map[string]struct{})
	for _, p := range discovery.Projects {
		if p == nil {
			continue
		}
		path := normalizeProjectPath(p.Path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
		projectsByPath[path] = p
	}
	sort.Strings(paths)

	daemon, err := s.database.GetDaemon(ctx, conn.daemonID)
	if err != nil {
		logging.Debug(LOG_PREFIX_TOOLS_DAEMON+" Project discovery ignored for unknown daemon", "daemonID", conn.daemonID)
		return nil
	}

	projectPathsJSON, err := jsonStringSlicePtr(paths)
	if err != nil {
		return fmt.Errorf("failed to encode discovered project paths: %w", err)
	}
	daemon.ProjectPaths = projectPathsJSON
	if err := s.database.UpsertDaemon(ctx, daemon); err != nil {
		return err
	}

	for _, path := range paths {
		discovered := projectsByPath[path]
		project, err := s.ensureOwnedProjectForPath(ctx, conn, path, discovered)
		if err != nil {
			return err
		}
		if project == nil {
			continue
		}

		if sendErr := s.sendLoadAndWatchProjectConfig(ctx, conn, path, true); sendErr != nil {
			logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Failed requesting config load/watch for discovered project",
				"error", sendErr,
				"projectPath", path,
				"daemonID", conn.daemonID,
			)
		}
	}

	return nil
}

func (s *ToolsDaemonService) handleLoadProjectConfigsResponse(ctx context.Context, conn *daemonConnection, resp *reliantv1.LoadProjectConfigsResponse) error {
	if resp == nil {
		return nil
	}
	if strings.TrimSpace(resp.Error) != "" {
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Daemon returned load_project_configs error",
			"requestID", resp.RequestId,
			"error", resp.Error,
			"daemonID", conn.daemonID,
		)
		return nil
	}
	if resp.Snapshot == nil {
		return nil
	}

	return s.persistProjectConfigSnapshot(ctx, conn, resp.Snapshot, true)
}

func (s *ToolsDaemonService) handleFileSystemChanged(ctx context.Context, conn *daemonConnection, msg *reliantv1.FileSystemChanged) error {
	if msg == nil {
		return nil
	}

	projectPath := normalizeProjectPath(msg.ProjectPath)
	if projectPath == "" {
		return nil
	}

	project, err := s.database.GetProjectByPath(ctx, projectPath)
	if err != nil {
		return nil // project might not exist yet, that's fine
	}
	if project == nil {
		return nil
	}

	return s.database.EmitUserRefetch(ctx, conn.userID, db.RefetchFileTree, db.RefetchOpts{
		ProjectID: &project.ID,
	})
}

func (s *ToolsDaemonService) handleProjectConfigDelta(ctx context.Context, conn *daemonConnection, delta *reliantv1.ProjectConfigDelta) error {
	if delta == nil {
		return nil
	}

	projectPath := normalizeProjectPath(delta.ProjectPath)
	if projectPath == "" {
		return nil
	}

	project, err := s.ensureOwnedProjectForPath(ctx, conn, projectPath, nil)
	if err != nil {
		return err
	}
	if project == nil {
		return nil
	}

	applyUpdate, err := s.shouldApplyProjectConfigUpdate(ctx, project.ID, delta.DaemonTimestampUnixMs)
	if err != nil {
		return err
	}
	if !applyUpdate {
		return nil
	}

	if snapshot := delta.SnapshotIfCompacted; snapshot != nil {
		if snapshot.ProjectPath == "" {
			snapshot.ProjectPath = projectPath
		}
		return s.persistProjectConfigSnapshot(ctx, conn, snapshot, false)
	}

	return s.SendLoadProjectConfigs(ctx, conn.userID, projectPath, uuid.New().String())
}

// daemonRuntimeTypeFromLabels extracts the daemon's runtime/sandbox type
// ("kata", "gvisor", ...) from its registration labels. Returns nil when the
// label is absent or empty (local/unknown daemons), so it persists as NULL.
func daemonRuntimeTypeFromLabels(labels map[string]string) *string {
	rt := strings.TrimSpace(labels[cfgpkg.DaemonRuntimeTypeLabelKey])
	if rt == "" {
		return nil
	}
	return &rt
}

func (s *ToolsDaemonService) persistProjectConfigSnapshot(ctx context.Context, conn *daemonConnection, snapshot *reliantv1.ProjectConfigSnapshot, force bool) error {
	if snapshot == nil {
		return nil
	}

	projectPath := normalizeProjectPath(snapshot.ProjectPath)
	if projectPath == "" {
		return nil
	}

	project, err := s.ensureOwnedProjectForPath(ctx, conn, projectPath, nil)
	if err != nil {
		return err
	}
	if project == nil {
		logging.Warn("[ToolsDaemon] persistProjectConfigSnapshot: project not found for path", "projectPath", projectPath, "daemonID", conn.daemonID)
		return nil
	}

	if !force {
		applyUpdate, err := s.shouldApplyProjectConfigUpdate(ctx, project.ID, snapshot.DaemonTimestampUnixMs)
		if err != nil {
			return err
		}
		if !applyUpdate {
			return nil
		}
	}

	record := &db.ProjectConfigRecord{
		ProjectID:            project.ID,
		DaemonID:             conn.daemonID,
		UserConfigYAML:       bytesToStringPtr(snapshot.UserConfigYaml),
		ProjectConfigYAML:    bytesToStringPtr(snapshot.ProjectConfigYaml),
		LocalConfigYAML:      bytesToStringPtr(snapshot.LocalConfigYaml),
		GlobalMemoryMD:       bytesToStringPtr(snapshot.GlobalMemoryMd),
		ProjectMemoryMD:      bytesToStringPtr(snapshot.ProjectMemoryMd),
		MCPConfigs:           flattenMCPConfigs(snapshot.McpConfigs),
		ProjectWorkflowsJSON: flattenIndexedWorkflows(snapshot.Workflows),
		ProjectPresetsJSON:   flattenIndexedPresets(snapshot.Presets),
		ProjectScenariosJSON: flattenIndexedScenarios(snapshot.Scenarios),
		ProjectSkillsJSON:    flattenIndexedSkills(snapshot.Skills),
		RepoMemoriesJSON:     flattenRepoMemories(snapshot.RepoMemoriesMd),
		RuntimeType:          daemonRuntimeTypeFromLabels(conn.labels),
		PushedAt:             daemonTimestampToTime(snapshot.DaemonTimestampUnixMs),
	}

	if err := s.database.UpsertProjectConfigRecord(ctx, record); err != nil {
		return err
	}

	return nil
}

func (s *ToolsDaemonService) ensureOwnedProjectForPath(ctx context.Context, conn *daemonConnection, path string, discovered *reliantv1.DiscoveredProject) (*db.Project, error) {
	if !filepath.IsAbs(path) {
		logging.Warn("[ToolsDaemon] Rejecting non-absolute project path", "path", path)
		return nil, nil
	}

	project, err := s.database.GetProjectByPath(ctx, path)
	if err != nil {
		if !isNotFoundErr(err) {
			return nil, err
		}

		name := filepath.Base(path)
		isGitRepo := false
		if discovered != nil {
			if strings.TrimSpace(discovered.Name) != "" {
				name = strings.TrimSpace(discovered.Name)
			}
			isGitRepo = discovered.IsGitRepo
		}

		now := time.Now().UTC()
		createErr := s.database.CreateProject(ctx, &db.Project{
			ID:         uuid.New().String(),
			UserID:     conn.userID,
			Name:       name,
			Path:       path,
			IsGitRepo:  isGitRepo,
			CreatedAt:  now,
			UpdatedAt:  now,
			LastActive: now,
		})
		if createErr != nil {
			project, err = s.database.GetProjectByPath(ctx, path)
			if err != nil {
				return nil, fmt.Errorf("failed to create discovered project %s: %w", path, createErr)
			}
		} else {
			project, err = s.database.GetProjectByPath(ctx, path)
			if err != nil {
				return nil, err
			}
		}
	}

	if project.UserID != conn.userID {
		return nil, nil
	}

	// Reconcile the cached git flag against what daemon discovery actually
	// observed on disk (bidirectional; see reconcileProjectGitRepo).
	if discovered != nil {
		if updateErr := reconcileProjectGitRepo(ctx, s.database, project, discovered.IsGitRepo); updateErr != nil {
			logging.Warn("ensureOwnedProjectForPath: failed to update git status", "error", updateErr, "projectID", project.ID)
		}
	}

	return project, nil
}

func (s *ToolsDaemonService) shouldApplyProjectConfigUpdate(ctx context.Context, projectID string, daemonTSUnixMs int64) (bool, error) {
	if daemonTSUnixMs <= 0 {
		return true, nil
	}

	record, err := s.database.GetProjectConfigRecord(ctx, projectID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return true, nil
		}
		return false, err
	}

	existingTSUnixMs := record.PushedAt.UTC().UnixMilli()
	if existingTSUnixMs <= 0 {
		return true, nil
	}
	if daemonTSUnixMs <= existingTSUnixMs {
		return false, nil
	}
	return true, nil
}

func daemonTimestampToTime(unixMs int64) time.Time {
	if unixMs <= 0 {
		return time.Now().UTC()
	}
	return time.UnixMilli(unixMs).UTC()
}

func flattenMCPConfigs(mcpConfigs map[string][]byte) *string {
	if len(mcpConfigs) == 0 {
		return nil
	}
	flat := make(map[string]string, 3)
	for _, scope := range []string{"user", "project", "local"} {
		payload, ok := mcpConfigs[scope]
		if !ok || len(payload) == 0 {
			continue
		}
		flat[scope] = string(payload)
	}
	if len(flat) == 0 {
		return nil
	}
	encoded, err := json.Marshal(flat)
	if err != nil {
		return nil
	}
	value := string(encoded)
	return &value
}

type storedWorkflowJSON struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	YAMLContent string `json:"yaml_content"`
	ContentHash string `json:"content_hash"`
}

type storedPresetJSON struct {
	Name        string `json:"name"`
	YAMLContent string `json:"yaml_content"`
	ContentHash string `json:"content_hash"`
}

type storedScenarioJSON struct {
	WorkflowSlug string `json:"workflow_slug"`
	Name         string `json:"name"`
	YAMLContent  string `json:"yaml_content"`
	ContentHash  string `json:"content_hash"`
}

func flattenIndexedWorkflows(workflows []*reliantv1.IndexedWorkflow) *string {
	if len(workflows) == 0 {
		return nil
	}
	items := make([]storedWorkflowJSON, 0, len(workflows))
	for _, w := range workflows {
		items = append(items, storedWorkflowJSON{
			Slug:        w.Slug,
			Name:        w.Name,
			YAMLContent: string(w.YamlContent),
			ContentHash: w.ContentHash,
		})
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil
	}
	value := string(encoded)
	return &value
}

func flattenIndexedPresets(presets []*reliantv1.IndexedPreset) *string {
	if len(presets) == 0 {
		return nil
	}
	items := make([]storedPresetJSON, 0, len(presets))
	for _, p := range presets {
		items = append(items, storedPresetJSON{
			Name:        p.Name,
			YAMLContent: string(p.YamlContent),
			ContentHash: p.ContentHash,
		})
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil
	}
	value := string(encoded)
	return &value
}

func flattenIndexedScenarios(scenarios []*reliantv1.IndexedScenario) *string {
	if len(scenarios) == 0 {
		return nil
	}
	items := make([]storedScenarioJSON, 0, len(scenarios))
	for _, s := range scenarios {
		items = append(items, storedScenarioJSON{
			WorkflowSlug: s.WorkflowSlug,
			Name:         s.Name,
			YAMLContent:  string(s.YamlContent),
			ContentHash:  s.ContentHash,
		})
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil
	}
	value := string(encoded)
	return &value
}

// flattenIndexedSkills converts the proto skills snapshot into the JSON blob
// stored in project_configs.project_skills_json. The JSON shape matches
// config.StoredSkill so StoredConfigProvider can deserialize identically.
func flattenIndexedSkills(skills []*reliantv1.IndexedSkill) *string {
	if len(skills) == 0 {
		return nil
	}
	items := make([]cfgpkg.StoredSkill, 0, len(skills))
	for _, s := range skills {
		if s == nil {
			continue
		}
		var userInvocable *bool
		switch s.UserInvocable {
		case "true":
			v := true
			userInvocable = &v
		case "false":
			v := false
			userInvocable = &v
		}
		items = append(items, cfgpkg.StoredSkill{
			SkillPath:              s.SkillPath,
			Name:                   s.Name,
			Description:            s.Description,
			Scope:                  s.Scope,
			Body:                   s.Body,
			AllowedTools:           s.AllowedTools,
			Metadata:               s.Metadata,
			HasChildren:            s.HasChildren,
			DisableModelInvocation: s.DisableModelInvocation,
			UserInvocable:          userInvocable,
			ArgumentHint:           s.ArgumentHint,
			Paths:                  s.Paths,
			ContentHash:            s.ContentHash,
			Source:                 s.Source,
		})
	}
	if len(items) == 0 {
		return nil
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil
	}
	value := string(encoded)
	return &value
}

// flattenRepoMemories converts the proto repo memories map into a JSON string
// for DB storage.
func flattenRepoMemories(memories map[string][]byte) *string {
	if len(memories) == 0 {
		return nil
	}
	strMap := make(map[string]string, len(memories))
	for k, v := range memories {
		strMap[k] = string(v)
	}
	encoded, err := json.Marshal(strMap)
	if err != nil {
		return nil
	}
	s := string(encoded)
	return &s
}

func (s *ToolsDaemonService) sendLoadAndWatchProjectConfig(ctx context.Context, conn *daemonConnection, projectPath string, includeInitial bool) error {
	if conn == nil {
		return nil
	}
	if err := s.SendLoadProjectConfigs(ctx, conn.userID, projectPath, uuid.New().String()); err != nil {
		return err
	}
	return s.SendWatchProjectConfigs(ctx, conn.userID, projectPath, includeInitial)
}

// defaultDaemonForUser returns the "best" connected daemon for a user.
// It prefers local daemons over cloud daemons, then most recently connected.
// Returns nil if no daemon is connected.
func (s *ToolsDaemonService) defaultDaemonForUser(userID string) *daemonConnection {
	// Must be called with s.mu held (at least RLock).
	daemonIDs := s.userDaemons[userID]
	if len(daemonIDs) == 0 {
		return nil
	}
	// Single daemon: fast path.
	if len(daemonIDs) == 1 {
		return s.connections[daemonIDs[0]]
	}
	// Prefer local daemons, then most recently connected.
	var best *daemonConnection
	for _, dID := range daemonIDs {
		c := s.connections[dID]
		if c == nil {
			continue
		}
		if best == nil {
			best = c
			continue
		}
		// Prefer local over cloud. Empty daemonType is treated as "local" for backward compat.
		cIsLocal := c.daemonType == "local" || c.daemonType == ""
		bestIsLocal := best.daemonType == "local" || best.daemonType == ""
		if cIsLocal && !bestIsLocal {
			best = c
			continue
		}
		// Same type: prefer most recently connected.
		if cIsLocal == bestIsLocal && c.connectedAt.After(best.connectedAt) {
			best = c
		}
	}
	return best
}

// daemonForUser returns the connection for a specific daemonID, or the default daemon for the user.
func (s *ToolsDaemonService) daemonForUser(userID string) *daemonConnection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.defaultDaemonForUser(userID)
}

func (s *ToolsDaemonService) sendToUserDaemon(userID string, msg *reliantv1.ServerMessage) error {
	if msg == nil {
		return nil
	}
	s.mu.RLock()
	conn := s.defaultDaemonForUser(userID)
	s.mu.RUnlock()
	if conn == nil {
		return fmt.Errorf("daemon not connected for user %s", userID)
	}
	return s.sendToConn(conn, msg)
}

// sendToConn enqueues a message on a specific connection's send channel. Used
// when the caller already holds the exact connection (e.g. reconcile) rather
// than resolving the user's default daemon.
func (s *ToolsDaemonService) sendToConn(conn *daemonConnection, msg *reliantv1.ServerMessage) error {
	if msg == nil {
		return nil
	}
	if conn == nil {
		return fmt.Errorf("nil daemon connection")
	}

	select {
	case conn.sendCh <- msg:
		return nil
	case <-conn.done:
		return fmt.Errorf("daemon connection closed for user %s", conn.userID)
	default:
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" daemon send buffer full, message dropped", "userID", conn.userID)
		return fmt.Errorf("daemon send buffer full, message dropped for user %s", conn.userID)
	}
}

// runSender handles sending messages to the daemon
func (s *ToolsDaemonService) runSender(conn *daemonConnection) {
	for {
		select {
		case <-conn.done:
			return
		case msg := <-conn.sendCh:
			if err := conn.stream.Send(msg); err != nil {
				logging.Error(LOG_PREFIX_TOOLS_DAEMON+" Failed to send message",
					"error", err,
					"userID", conn.userID)
				conn.closeDone()
				return
			}
			s.mu.Lock()
			conn.lastActivity = time.Now().UTC()
			s.mu.Unlock()
		}
	}
}

// runHeartbeat sends periodic heartbeats to keep the connection alive
func (s *ToolsDaemonService) runHeartbeat(conn *daemonConnection) {
	ticker := time.NewTicker(daemonHeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-conn.done:
			return
		case <-ticker.C:
			msg := &reliantv1.ServerMessage{
				Message: &reliantv1.ServerMessage_Heartbeat{
					Heartbeat: &reliantv1.ServerHeartbeat{
						Timestamp: time.Now().Unix(),
					},
				},
			}
			select {
			case conn.sendCh <- msg:
			default:
				// Buffer full, skip heartbeat
			}
		}
	}
}

// ============================================
// DaemonConnectionManager interface implementation
// ============================================

// ListConnectedDaemons returns info about all daemons connected for a user.
// Implements toolexec.ConnectedDaemonLister.
func (s *ToolsDaemonService) ListConnectedDaemons(userID string) []toolexec.DaemonInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	daemonIDs := s.userDaemons[userID]
	result := make([]toolexec.DaemonInfo, 0, len(daemonIDs))
	for _, dID := range daemonIDs {
		c := s.connections[dID]
		if c == nil {
			continue
		}
		result = append(result, toolexec.DaemonInfo{
			DaemonID:   c.daemonID,
			Name:       c.name,
			Labels:     c.labels,
			Type:       c.daemonType,
			Status:     "connected",
			LastActive: c.lastActivity,
		})
	}
	return result
}

// IsDaemonOnline checks if a daemon is currently connected for the user
func (s *ToolsDaemonService) IsDaemonOnline(_ context.Context, userID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.userDaemons[userID]) > 0
}

// HasConnectedDaemonsForUser reports whether this gateway currently holds any
// daemon stream for the user. Implements toolexec.LocalConnectionChecker so the
// NATS router can skip the DB freshness check on the hot path.
func (s *ToolsDaemonService) HasConnectedDaemonsForUser(userID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.userDaemons[userID]) > 0
}

// ConnectedDaemonCountForUser reports how many daemon streams this gateway
// currently holds for the user. Implements daemonquery.UserLivenessSource so
// the per-user any-live NATS responder answers from the in-memory connection
// map — authoritative for the streams this gateway owns.
func (s *ToolsDaemonService) ConnectedDaemonCountForUser(userID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.userDaemons[userID])
}

// SendToolRequest pushes a tool request to the daemon
// Context is accepted for interface compliance but not used - daemon connections have their own lifecycle
func (s *ToolsDaemonService) SendToolRequest(ctx context.Context, userID string, request *toolexec.ToolExecutionRequest) error {
	s.mu.RLock()
	conn := s.defaultDaemonForUser(userID)
	s.mu.RUnlock()

	if conn == nil {
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Cannot send request - daemon offline",
			"userID", userID,
			"requestID", request.RequestID)
		return fmt.Errorf("daemon offline for user %s, request %s could not be delivered", userID, request.RequestID)
	}

	// Convert context map to JSON
	contextJSON := ""
	if request.Context != nil {
		if data, err := json.Marshal(request.Context); err == nil {
			contextJSON = string(data)
		}
	}

	// Create proto message
	toolReq := &reliantv1.ToolRequest{
		RequestId:      request.RequestID,
		ToolName:       request.ToolName,
		ToolInput:      request.ToolInput,
		ToolCallId:     request.ToolCallID,
		ContentBlockId: request.ContentBlockID,
		ContextJson:    contextJSON,
		TimeoutMs:      int32(request.TimeoutMs),
	}

	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_ToolRequest{
			ToolRequest: toolReq,
		},
	}

	select {
	case conn.sendCh <- msg:
		return nil
	case <-conn.done:
		return fmt.Errorf("daemon connection closed for user %s while sending tool request %s", userID, request.RequestID)
	default:
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Send buffer full",
			"userID", userID,
			"requestID", request.RequestID)
		return fmt.Errorf("daemon send buffer full for user %s, request %s dropped", userID, request.RequestID)
	}
}

// GetActiveConnections returns count of active connections (for monitoring)
func (s *ToolsDaemonService) GetActiveConnections() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.connections)
}

// GetConnectedUsers returns list of connected user IDs (for monitoring)
func (s *ToolsDaemonService) GetConnectedUsers() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	users := make([]string, 0, len(s.userDaemons))
	for userID := range s.userDaemons {
		users = append(users, userID)
	}
	return users
}

// SendLoadProjectConfigs requests a full snapshot load for a project path.
func (s *ToolsDaemonService) SendLoadProjectConfigs(_ context.Context, userID string, projectPath string, requestID string) error {
	projectPath = normalizeProjectPath(projectPath)
	if projectPath == "" {
		return nil
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		requestID = uuid.New().String()
	}

	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_LoadProjectConfigs{
			LoadProjectConfigs: &reliantv1.LoadProjectConfigsRequest{
				ProjectPath: projectPath,
				RequestId:   requestID,
			},
		},
	}
	return s.sendToUserDaemon(userID, msg)
}

// SendWatchProjectConfigs subscribes daemon-side watchers for project config changes.
func (s *ToolsDaemonService) SendWatchProjectConfigs(_ context.Context, userID string, projectPath string, includeInitial bool) error {
	projectPath = normalizeProjectPath(projectPath)
	if projectPath == "" {
		return nil
	}

	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_WatchProjectConfigs{
			WatchProjectConfigs: &reliantv1.WatchProjectConfigsRequest{
				ProjectPath:    projectPath,
				IncludeInitial: includeInitial,
			},
		},
	}
	return s.sendToUserDaemon(userID, msg)
}

// SendUnwatchProjectConfigs unsubscribes daemon-side watchers for project config changes.
func (s *ToolsDaemonService) SendUnwatchProjectConfigs(userID string, projectPath string) error {
	projectPath = normalizeProjectPath(projectPath)
	if projectPath == "" {
		return nil
	}

	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_UnwatchProjectConfigs{
			UnwatchProjectConfigs: &reliantv1.UnwatchProjectConfigsRequest{ProjectPath: projectPath},
		},
	}
	return s.sendToUserDaemon(userID, msg)
}

// SendKillProcess sends a kill request to the daemon for a specific background process.
func (s *ToolsDaemonService) SendKillProcess(userID, processID string) error {
	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_KillProcess{
			KillProcess: &reliantv1.DaemonKillProcessRequest{
				ProcessId: processID,
			},
		},
	}
	return s.sendToUserDaemon(userID, msg)
}

// SendDaemonCommand sends a generic command to the user's default daemon and
// waits for a correlated response. For commands that must target a specific
// connection (e.g. reconcile asking THIS daemon what's on its disk) use
// sendCommandToConn directly.
func (s *ToolsDaemonService) SendDaemonCommand(ctx context.Context, userID string, req *reliantv1.DaemonCommandRequest) (*reliantv1.DaemonCommandResponse, error) {
	s.mu.RLock()
	conn := s.defaultDaemonForUser(userID)
	s.mu.RUnlock()
	if conn == nil {
		return nil, fmt.Errorf("no daemon connected for user %s", userID)
	}
	return s.sendCommandToConn(ctx, conn, req)
}

// sendCommandToConn sends a generic command to a specific daemon connection and
// waits for the correlated DaemonCommandResponse. It mirrors SendDaemonCommand's
// correlation logic but targets the passed conn directly rather than resolving
// the user's default daemon — required when a user has multiple daemons and the
// caller needs to ask one particular connection about its own disk.
func (s *ToolsDaemonService) sendCommandToConn(ctx context.Context, conn *daemonConnection, req *reliantv1.DaemonCommandRequest) (*reliantv1.DaemonCommandResponse, error) {
	if conn == nil {
		return nil, fmt.Errorf("nil daemon connection")
	}

	// Register a pending response channel keyed by request id on this conn.
	respCh := make(chan *reliantv1.DaemonCommandResponse, 1)
	conn.pendingCommandsMu.Lock()
	conn.pendingCommands[req.RequestId] = respCh
	conn.pendingCommandsMu.Unlock()

	// Ensure cleanup on all exit paths.
	defer func() {
		conn.pendingCommandsMu.Lock()
		delete(conn.pendingCommands, req.RequestId)
		conn.pendingCommandsMu.Unlock()
	}()

	// Send the command to this specific connection.
	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_DaemonCommand{
			DaemonCommand: req,
		},
	}
	if err := s.sendToConn(conn, msg); err != nil {
		return nil, fmt.Errorf("send daemon command: %w", err)
	}

	// Wait for the correlated response.
	timeout := 30 * time.Second
	if req.TimeoutMs > 0 {
		timeout = time.Duration(req.TimeoutMs) * time.Millisecond
	}

	select {
	case resp := <-respCh:
		return resp, nil
	case <-time.After(timeout):
		s.cancelDaemonCommand(conn.userID, req.RequestId, "daemon command timed out")
		return nil, fmt.Errorf("daemon command %q timed out after %s", req.CommandType, timeout)
	case <-conn.done:
		return nil, fmt.Errorf("daemon disconnected while waiting for command %q response", req.CommandType)
	case <-ctx.Done():
		s.cancelDaemonCommand(conn.userID, req.RequestId, "daemon command caller cancelled")
		return nil, ctx.Err()
	}
}

// SendToolRequestSync sends a tool execution request to the daemon and waits for the correlated response.
func (s *ToolsDaemonService) SendToolRequestSync(ctx context.Context, userID string, request *toolexec.ToolExecutionRequest) (*toolexec.ToolExecutionResponse, error) {
	s.mu.RLock()
	conn := s.defaultDaemonForUser(userID)
	s.mu.RUnlock()
	if conn == nil {
		return nil, fmt.Errorf("no daemon connected for user %s", userID)
	}

	// Register a pending response channel.
	respCh := make(chan *toolexec.ToolExecutionResponse, 1)
	conn.pendingToolRequestsMu.Lock()
	conn.pendingToolRequests[request.RequestID] = respCh
	conn.pendingToolRequestsMu.Unlock()

	// Ensure cleanup on all exit paths.
	defer func() {
		conn.pendingToolRequestsMu.Lock()
		delete(conn.pendingToolRequests, request.RequestID)
		conn.pendingToolRequestsMu.Unlock()
	}()

	// Convert context map to JSON
	contextJSON := ""
	if request.Context != nil {
		if data, err := json.Marshal(request.Context); err == nil {
			contextJSON = string(data)
		}
	}

	// Create and send the tool request message.
	toolReq := &reliantv1.ToolRequest{
		RequestId:      request.RequestID,
		ToolName:       request.ToolName,
		ToolInput:      request.ToolInput,
		ToolCallId:     request.ToolCallID,
		ContentBlockId: request.ContentBlockID,
		ContextJson:    contextJSON,
		TimeoutMs:      int32(request.TimeoutMs),
	}

	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_ToolRequest{
			ToolRequest: toolReq,
		},
	}
	if err := s.sendToUserDaemon(userID, msg); err != nil {
		return nil, fmt.Errorf("send tool request: %w", err)
	}

	// Wait for the correlated response.
	// Default to 10 minutes for bash commands; use request timeout if set.
	timeout := 600 * time.Second
	if request.TimeoutMs > 0 {
		timeout = time.Duration(request.TimeoutMs) * time.Millisecond
	}

	select {
	case resp := <-respCh:
		return resp, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("tool request %q timed out after %s", request.RequestID, timeout)
	case <-conn.done:
		return nil, fmt.Errorf("daemon disconnected while waiting for tool request %q response", request.RequestID)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *ToolsDaemonService) cancelDaemonCommand(userID, requestID, reason string) {
	if err := s.SendToolExecutionCancel(context.Background(), userID, requestID, reason); err != nil {
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Failed to send daemon command cancel", "userID", userID, "requestID", requestID, "error", err)
	}
}

// SendToolExecutionCancel sends a cancellation request to connected daemon.
// This currently reuses the tool_cancel transport and request_id correlation for
// both tool executions and generic daemon commands.
func (s *ToolsDaemonService) SendToolExecutionCancel(_ context.Context, userID, requestID, reason string) error {
	if strings.TrimSpace(requestID) == "" {
		return nil
	}

	s.mu.RLock()
	conn := s.defaultDaemonForUser(userID)
	s.mu.RUnlock()
	if conn == nil {
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Cannot cancel - daemon offline", "userID", userID, "requestID", requestID)
		return fmt.Errorf("daemon offline for user %s, cannot deliver cancel for %s", userID, requestID)
	}

	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_ToolCancel{
			ToolCancel: &reliantv1.ToolExecutionCancel{RequestId: requestID, Reason: reason},
		},
	}

	select {
	case conn.sendCh <- msg:
		return nil
	case <-conn.done:
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Cancel dropped - connection closed", "userID", userID, "requestID", requestID)
		return fmt.Errorf("daemon connection closed for user %s, cancel for %s dropped", userID, requestID)
	default:
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Cancel dropped - buffer full", "userID", userID, "requestID", requestID)
		return fmt.Errorf("daemon buffer full for user %s, cancel for %s dropped", userID, requestID)
	}
}

// dispatchTerminalEvent sends a terminal output event to all subscribers for the session.
func (c *daemonConnection) dispatchTerminalEvent(evt *toolexec.TerminalOutputEvent) {
	c.terminalSubsMu.Lock()
	subs := c.terminalSubs[evt.SessionID]
	// Copy slice to release lock quickly.
	subsCopy := make([]chan *toolexec.TerminalOutputEvent, len(subs))
	copy(subsCopy, subs)
	c.terminalSubsMu.Unlock()

	for _, ch := range subsCopy {
		select {
		case ch <- evt:
		default:
			// Subscriber is slow; drop to avoid blocking the receive loop.
		}
	}
}

// dispatchProcessOutputEvent sends a process output event to all subscribers for the process.
func (c *daemonConnection) dispatchProcessOutputEvent(evt *toolexec.ProcessOutputEvent) {
	c.processOutputSubsMu.Lock()
	subs := c.processOutputSubs[evt.ProcessID]
	subsCopy := make([]chan *toolexec.ProcessOutputEvent, len(subs))
	copy(subsCopy, subs)
	c.processOutputSubsMu.Unlock()

	for _, ch := range subsCopy {
		select {
		case ch <- evt:
		default:
		}
	}
}

// closeAllSubscribers closes all terminal and process output subscriber channels.
// Called when a daemon disconnects.
func (c *daemonConnection) closeAllSubscribers() {
	c.terminalSubsMu.Lock()
	for sessionID, subs := range c.terminalSubs {
		for _, ch := range subs {
			close(ch)
		}
		delete(c.terminalSubs, sessionID)
	}
	c.terminalSubsMu.Unlock()

	c.processOutputSubsMu.Lock()
	for processID, subs := range c.processOutputSubs {
		for _, ch := range subs {
			close(ch)
		}
		delete(c.processOutputSubs, processID)
	}
	c.processOutputSubsMu.Unlock()
}

// SendTerminalInput sends raw PTY input bytes to the daemon for a terminal session.
func (s *ToolsDaemonService) SendTerminalInput(userID string, sessionID string, data []byte) error {
	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_TerminalInput{
			TerminalInput: &reliantv1.TerminalInputMessage{
				SessionId: sessionID,
				Data:      data,
			},
		},
	}
	return s.sendToUserDaemon(userID, msg)
}

// SendTerminalResize sends a terminal resize request to the daemon.
func (s *ToolsDaemonService) SendTerminalResize(userID string, sessionID string, cols, rows uint32) error {
	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_TerminalResize{
			TerminalResize: &reliantv1.TerminalResizeMessage{
				SessionId: sessionID,
				Cols:      cols,
				Rows:      rows,
			},
		},
	}
	return s.sendToUserDaemon(userID, msg)
}

// SubscribeTerminalOutput registers a subscriber for terminal output for a session.
// Returns a channel that receives events, an unsubscribe function, and an error.
func (s *ToolsDaemonService) SubscribeTerminalOutput(userID string, sessionID string) (<-chan *toolexec.TerminalOutputEvent, func(), error) {
	s.mu.RLock()
	conn := s.defaultDaemonForUser(userID)
	s.mu.RUnlock()
	if conn == nil {
		return nil, nil, fmt.Errorf("no daemon connected for user %s", userID)
	}

	ch := make(chan *toolexec.TerminalOutputEvent, 64)

	// Register the subscriber channel BEFORE sending the subscribe message so no
	// dispatched output can be dropped between the daemon starting its pump and
	// the channel being registered.
	conn.terminalSubsMu.Lock()
	conn.terminalSubs[sessionID] = append(conn.terminalSubs[sessionID], ch)
	conn.terminalSubsMu.Unlock()

	// Send the terminal-output-subscribe message to the daemon. This is what
	// starts the PTY output pump on the daemon side — mirroring the
	// process-output subscribe flow — so the initial shell prompt is buffered
	// until the full subscriber chain is established.
	subMsg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_TerminalOutputSubscribe{
			TerminalOutputSubscribe: &reliantv1.TerminalOutputSubscribeMessage{
				SessionId: sessionID,
			},
		},
	}
	if err := s.sendToUserDaemon(userID, subMsg); err != nil {
		// Roll back the registration on failure.
		conn.terminalSubsMu.Lock()
		subs := conn.terminalSubs[sessionID]
		for i, sub := range subs {
			if sub == ch {
				conn.terminalSubs[sessionID] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		if len(conn.terminalSubs[sessionID]) == 0 {
			delete(conn.terminalSubs, sessionID)
		}
		conn.terminalSubsMu.Unlock()
		return nil, nil, fmt.Errorf("send terminal output subscribe: %w", err)
	}

	unsub := func() {
		conn.terminalSubsMu.Lock()
		defer conn.terminalSubsMu.Unlock()
		subs := conn.terminalSubs[sessionID]
		for i, sub := range subs {
			if sub == ch {
				conn.terminalSubs[sessionID] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		if len(conn.terminalSubs[sessionID]) == 0 {
			delete(conn.terminalSubs, sessionID)
		}
	}

	return ch, unsub, nil
}

// SubscribeProcessOutput registers a subscriber for process output.
// It sends a ProcessOutputSubscribe message to the daemon and returns a channel
// that receives output events. The unsubscribe function sends an unsubscribe message
// and removes the subscriber.
func (s *ToolsDaemonService) SubscribeProcessOutput(userID string, processID string, newOnly bool) (<-chan *toolexec.ProcessOutputEvent, func(), error) {
	s.mu.RLock()
	conn := s.defaultDaemonForUser(userID)
	s.mu.RUnlock()
	if conn == nil {
		return nil, nil, fmt.Errorf("no daemon connected for user %s", userID)
	}

	// Send subscribe message to daemon.
	subMsg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_ProcessOutputSubscribe{
			ProcessOutputSubscribe: &reliantv1.ProcessOutputSubscribeMessage{
				ProcessId: processID,
				NewOnly:   newOnly,
			},
		},
	}
	if err := s.sendToUserDaemon(userID, subMsg); err != nil {
		return nil, nil, fmt.Errorf("send process output subscribe: %w", err)
	}

	ch := make(chan *toolexec.ProcessOutputEvent, 128)

	conn.processOutputSubsMu.Lock()
	conn.processOutputSubs[processID] = append(conn.processOutputSubs[processID], ch)
	conn.processOutputSubsMu.Unlock()

	unsub := func() {
		// Remove subscriber.
		conn.processOutputSubsMu.Lock()
		subs := conn.processOutputSubs[processID]
		for i, sub := range subs {
			if sub == ch {
				conn.processOutputSubs[processID] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		remaining := len(conn.processOutputSubs[processID])
		if remaining == 0 {
			delete(conn.processOutputSubs, processID)
		}
		conn.processOutputSubsMu.Unlock()

		// Send unsubscribe message to daemon if no more subscribers.
		if remaining == 0 {
			unsubMsg := &reliantv1.ServerMessage{
				Message: &reliantv1.ServerMessage_ProcessOutputUnsubscribe{
					ProcessOutputUnsubscribe: &reliantv1.ProcessOutputUnsubscribeMessage{
						ProcessId: processID,
					},
				},
			}
			_ = s.sendToUserDaemon(userID, unsubMsg) // best-effort
		}
	}

	return ch, unsub, nil
}

func daemonStringPtrOrNil(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// normalizeRegisteredDaemonType translates the daemon's self-reported daemon_type
// (from DaemonRegister.daemon_type — historically "local" or "cloud") into the
// vocabulary used on DaemonInfo.daemon_type ("self_hosted" or "managed").
// Returns nil for unknown/empty values so the existing column value is preserved
// on re-registration (the COALESCE in UpsertDaemon handles that).
func normalizeRegisteredDaemonType(value string) *string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "cloud", "managed":
		s := "managed"
		return &s
	case "local", "self_hosted", "self-hosted":
		s := "self_hosted"
		return &s
	default:
		return nil
	}
}

func bytesToStringPtr(value []byte) *string {
	if len(value) == 0 {
		return nil
	}
	v := string(value)
	return &v
}

func jsonStringSlicePtr(values []string) (*string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	data, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	v := string(data)
	return &v, nil
}

func (s *ToolsDaemonService) listAllProjectPaths(ctx context.Context, userID string) ([]string, error) {
	projects, err := s.database.ListProjects(ctx, db.ProjectFilters{UserID: userID, Limit: 1000, Offset: 0})
	if err != nil {
		return nil, err
	}

	paths := make([]string, 0, len(projects))
	seen := make(map[string]struct{}, len(projects))
	for _, project := range projects {
		if project == nil || strings.TrimSpace(project.ID) == "" || strings.TrimSpace(project.Path) == "" {
			continue
		}

		normalizedPath := normalizeProjectPath(project.Path)
		if normalizedPath == "" {
			continue
		}
		if _, ok := seen[normalizedPath]; ok {
			continue
		}
		seen[normalizedPath] = struct{}{}
		paths = append(paths, normalizedPath)
	}

	sort.Strings(paths)
	return paths, nil
}

func normalizeProjectPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	cleaned := filepath.Clean(trimmed)
	if !filepath.IsAbs(cleaned) {
		return "" // reject relative paths
	}
	return cleaned
}

func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "not found") || strings.Contains(errText, "no rows")
}