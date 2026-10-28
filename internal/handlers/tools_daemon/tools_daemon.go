// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ospath"
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

	// staleConnectionThreshold is how long the gateway may go without READING
	// anything from a daemon before the sweeper treats the stream as dead and
	// removes it. Daemon heartbeat interval is 15s, so 90s = 6 missed
	// heartbeats — comfortably beyond any plausible transient network blip but
	// well within human-noticeable latency.
	staleConnectionThreshold = 90 * time.Second

	// leaseRenewTimeout bounds one daemon_attachment lease renewal. A renewal
	// is a single-row upsert; one that has not finished in this long is a
	// hung connection, and it must not hold the renewer (and with it every
	// later heartbeat's renewal) hostage.
	leaseRenewTimeout = 10 * time.Second

	// inboundStateApplyTimeout bounds the database work for ONE state message
	// on a connection's inbound state worker. The worker is a FIFO in front
	// of the receive loop (inboundStateQueueSize): a write that never returns
	// fills the queue, the receive loop stops reading, and every reply from
	// that daemon is held behind it. Generous on purpose — a 10MB
	// project-config snapshot persists in about a second.
	inboundStateApplyTimeout = 30 * time.Second
)

// daemonStream abstracts the send/receive operations for a daemon connection.
// This allows both inbound (ConnectDaemon) and outbound (ConnectGateway) streams
// to be handled uniformly.
type daemonStream interface {
	Send(msg *reliantv1.ServerMessage) error
	Receive() (*reliantv1.DaemonMessage, error)
	// abort makes a Receive pending on the stream, and every later one,
	// return an error. It is how the stream's owner stops the goroutine
	// reading it before the stream is released (releaseStream): nothing
	// else ends a server-side Receive while the daemon is still connected.
	// Idempotent; safe from any goroutine.
	abort()
}

// inboundStream wraps a connect.BidiStream (server-side, inbound daemon connection).
type inboundStream struct {
	stream *connect.BidiStream[reliantv1.DaemonMessage, reliantv1.ServerMessage]
	// ctx is the handler's: the listener served its request with a body
	// serverkit.AbortReceive can end, which is what abort does.
	ctx context.Context
}

func (s *inboundStream) Send(msg *reliantv1.ServerMessage) error    { return s.stream.Send(msg) }
func (s *inboundStream) Receive() (*reliantv1.DaemonMessage, error) { return s.stream.Receive() }
func (s *inboundStream) abort()                                     { serverkit.AbortReceive(s.ctx) }

// outboundStream wraps a connect.BidiStreamForClient (client-side, outbound gateway connection).
type outboundStream struct {
	stream *connect.BidiStreamForClient[reliantv1.ServerMessage, reliantv1.DaemonMessage]
	// cancel cancels the context the stream was opened with: the way to end
	// a client-side Receive (CloseResponse must not race one).
	cancel context.CancelFunc
}

func (s *outboundStream) Send(msg *reliantv1.ServerMessage) error    { return s.stream.Send(msg) }
func (s *outboundStream) Receive() (*reliantv1.DaemonMessage, error) { return s.stream.Receive() }
func (s *outboundStream) abort() {
	if s.cancel != nil {
		s.cancel()
	}
}

// daemonConnection represents an active daemon connection
type daemonConnection struct {
	userID      string
	daemonID    string
	name        string
	hostname    string
	platform    string
	labels      map[string]string
	daemonType  string // "local" or "cloud"
	connectedAt time.Time
	// lastActivity is when anything last happened on this connection in
	// EITHER direction — a message read, a message sent, an LLM call the
	// control plane attributed to the user (TouchDaemonsForUser). It answers
	// "is this daemon in use" (DaemonStatus), not "is this stream alive".
	lastActivity time.Time
	// lastReceived is when the receive loop last READ a message off the
	// stream. It is the only liveness signal the stale-connection sweeper
	// trusts: the gateway's own 15s heartbeat sends succeed into a buffer
	// whether or not the daemon's side of the stream is moving, so a
	// liveness clock our own sends could bump never runs out on a stream
	// that has gone quiet inbound — exactly the stream the sweeper exists to
	// reap. Guarded by Service.mu, like lastActivity.
	lastReceived time.Time
	stream       daemonStream
	// streamUsers counts the goroutines besides the stream's owner that use
	// stream (the sender, startSender). The owner — ConnectDaemon's handler,
	// or the connector driving an outbound stream — joins them in
	// releaseStream before it lets the stream go: Connect forbids touching
	// a stream once its handler has returned, and what runs after the
	// handler (otelconnect's span bookkeeping) races a straggler that does.
	streamUsers sync.WaitGroup

	// The session's reachability lease (daemon_attachment), as its connect
	// wrote it. attachedAt is that row's attached_at, at the column's
	// microsecond precision (leaseTime): it names THIS session's lease, so
	// teardown releases only a row this session (or an older one) wrote and
	// never a newer session's (releaseLease). The source and pod address
	// ride along so a renewal can re-create a lost row exactly as the
	// connect wrote it (renewLease).
	attachedAt   time.Time
	attachSource db.DaemonAttachmentSource
	// build is the build the daemon reported at registration (zero = unknown
	// build). It is constant for the session, so renewLease re-creating a lost
	// lease row writes it back.
	build   db.DaemonBuild
	podIP   *string
	podPort *int
	// leaseMu orders lease renewals against teardown's release, so a renewal
	// can never land after teardownConnection released the row and leave a
	// lease for a stream that is gone.
	leaseMu sync.Mutex
	// leaseRecreated is set by the renewer when it had to re-create a
	// missing lease row, whose detected_ports then holds the column default;
	// the inbound state worker clears it and rewrites the ports
	// (lastDetectedPortsKey is the worker's alone).
	leaseRecreated atomic.Bool

	// ackedDaemonHeartbeat is the timestamp of the newest DaemonHeartbeat the
	// receive loop has read, echoed to the daemon in every ServerHeartbeat.
	// It is the daemon's end-to-end proof that the gateway is reading what it
	// sends; written only by the receive loop.
	ackedDaemonHeartbeat atomic.Int64
	sendCh               chan *reliantv1.ServerMessage
	done                 chan struct{}
	doneOnce             sync.Once
	// closeReason records why done was closed, so the stream handler can
	// return a distinguishable error instead of a bare EOF.
	closeReason atomic.Pointer[error]

	// movesOnDrain is the daemon's DaemonRegister.moves_on_drain: it acts on
	// a drain notice and holds its results until acknowledged. Set once, at
	// registration.
	movesOnDrain bool
	// admissionMu serializes restart with queueing new work. In-flight replies,
	// cancel and acknowledgements do not require new admission.
	admissionMu   sync.Mutex
	incarnationID string // registration process identity, not an authorization identity
	restart       atomic.Pointer[reliantv1.DaemonRestartStatus]
	// drainNoticeSent is set once this replica has told the daemon it is
	// draining (handOver). From then on the daemon may end the stream
	// itself once it has moved, which is the drain working, not a fault.
	drainNoticeSent atomic.Bool
	// daemonToolCallsInFlight is the daemon's own count of executing tool
	// calls, from its newest heartbeat. It covers calls that arrived over a
	// stream to another replica, which pendingToolRequests never saw.
	daemonToolCallsInFlight atomic.Uint32

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
	// avoids a per-heartbeat UPDATE. Only the inbound state worker touches it.
	lastDetectedPortsKey string

	// Disk report bookkeeping (see recordDiskReport). lastDiskSig is the
	// material signature last stored, lastDiskWrite when, and lastDiskUrgent
	// the part of it the UI must hear about at once. lastDiskStored is the
	// report last stored, read from the daemon row on this connection's first
	// write (lastDiskLoaded) so a pressure episode outlives the connection.
	// Only the inbound state worker touches them.
	lastDiskSig    string
	lastDiskUrgent string
	lastDiskWrite  time.Time
	lastDiskStored *reliantv1.DiskReport
	lastDiskLoaded bool

	// Local-model relay (see localmodel_gateway.go). Guarded by localModelMu.
	localModelMu sync.Mutex
	// localModelRelays maps request_id -> the bridge's chunk consumer.
	localModelRelays map[string]func(*reliantv1.LocalModelHTTPChunk)
	// inventoryWaiters receive the next inventory the daemon publishes.
	inventoryWaiters []chan *reliantv1.LocalModelInventory
	// lastInventoryJSON is the last stored inventory encoding; byte-identical
	// republishes skip the DB write. Only the inbound state worker touches it.
	lastInventoryJSON string
}

// errGatewayDraining ends a daemon stream because THIS gateway is shutting
// down. It is not a daemon-side fault and it is not permanent, so the daemon
// must redial — at which point the Service routes it to a surge pod that is
// already serving.
//
// CodeUnavailable is chosen deliberately. isFatalError
// (internal/toolexec/daemonruntime/runtime.go:543) treats PermissionDenied,
// Unimplemented and Aborted as terminal and everything else as recoverable, so
// Unavailable puts the daemon on the reconnect path with a 1s first delay
// (transport.ReconnectMinDelay; the backoff resets to it after any session
// that lasted 30s, which every real daemon session does).
//
// The alternative — letting http.Server.Shutdown handle it — does not work at
// all: Shutdown waits for active requests to finish and a bidi stream never
// finishes on its own, so the gateway burned its entire shutdown budget and
// then exited, cutting every daemon's TCP connection with no status at all.
//
// It matches svcerr.ErrServerDraining, which forge's logging treats as an
// expected outcome: every held stream ends this way on every rollout, and
// logging each one as a failed stream at ERROR paged on a rollout that went
// exactly as designed.
var errGatewayDraining = svcerr.Draining(
	"gateway draining: this daemon-gateway replica is shutting down; reconnect to reach a live replica")

// Close ends every daemon stream this service is holding, with a status the
// daemon can act on.
//
// This runs at gateway shutdown, AFTER the readiness flip and pre-stop pause,
// so by the time streams are cut the replica is already out of rotation and a
// redial lands somewhere else. Each connection's done channel is closed with
// errGatewayDraining, which unparks handleIncoming (it selects on conn.done)
// and makes the Connect handler return that error to the daemon — instead of
// leaving the handler blocked in Receive until the process is killed.
//
// Idempotent: closeWith is guarded by sync.Once per connection, so calling
// Close twice, or calling it alongside a stream that is ending on its own, is
// safe. The connections map is left intact — teardownConnection still runs
// from each stream handler's defer and owns the attachment-row cleanup.
//
// The stale-connection sweeper goroutine started by Start() is bound to the
// context passed to Start, not to Close — cancel that context to stop it.
func (s *Service) Close() {
	s.mu.RLock()
	conns := make([]*daemonConnection, 0, len(s.connections))
	for _, conn := range s.connections {
		conns = append(conns, conn)
	}
	s.mu.RUnlock()

	if len(conns) == 0 {
		return
	}

	logging.Info(LOG_PREFIX_TOOLS_DAEMON+" Draining daemon streams for shutdown",
		"connections", len(conns))
	for _, conn := range conns {
		conn.closeWith(errGatewayDraining)
	}
}

// Start launches background goroutines owned by the service. Currently this
// is just the stale-connection sweeper, which periodically scans the
// connections map for half-open daemon streams (TCP socket alive but the
// application-level bidi stream is dead — observed in production when a
// gateway → workspace-pod connection stays ESTABLISHED long after the
// daemon's gRPC stream ended).
//
// Returns when ctx is cancelled. Safe to call once; the caller is
// responsible for running this in its own goroutine.
func (s *Service) Start(ctx context.Context) {
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

// sweepStaleConnections removes daemon connections the gateway has not read
// anything from for staleConnectionThreshold, provided the connection has
// been registered for at least staleConnectionThreshold (so we don't reap a
// brand-new connection that hasn't sent its first message yet).
//
// Silence is measured INBOUND only (lastReceived). It used to be lastActivity,
// which our own sends and the control plane's LLM-activity pings also bump —
// so a stream whose daemon-to-gateway direction had stalled stayed "active"
// forever on the strength of the gateway's own 15s heartbeat, and was never
// reaped. Prod 2026-10-11: nothing was read from one daemon for twenty
// minutes, every command it was sent timed out, and the sweeper never fired.
//
// For each reaped connection: fires OnDaemonDisconnected listeners, closes
// done + subscribers, and releases its daemon_attachment lease — same as a
// normal disconnect, just driven by the sweeper rather than the stream's
// receive-loop returning. Closing done ends the stream handler, so the daemon
// redials and gets a fresh stream.
func (s *Service) sweepStaleConnections() {
	now := s.now()
	threshold := staleConnectionThreshold

	// Collect stale connections under the read lock, then release before
	// calling teardownConnection (which acquires the write lock + fires
	// listeners outside it).
	type staleConn struct {
		conn         *daemonConnection
		lastReceived time.Time
	}
	var stale []staleConn
	s.mu.RLock()
	for _, conn := range s.connections {
		if now.Sub(conn.connectedAt) < threshold {
			// Don't reap a freshly-registered connection that hasn't had a
			// chance to send anything yet.
			continue
		}
		lastReceived := conn.lastReceived
		if lastReceived.IsZero() {
			lastReceived = conn.connectedAt
		}
		if now.Sub(lastReceived) <= threshold {
			continue
		}
		stale = append(stale, staleConn{conn: conn, lastReceived: lastReceived})
	}
	s.mu.RUnlock()

	for _, sc := range stale {
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Reaping daemon connection that has gone silent inbound",
			"daemonID", sc.conn.daemonID,
			"userID", sc.conn.userID,
			"lastReceived", sc.lastReceived,
			"silentFor", now.Sub(sc.lastReceived).Round(time.Second),
		)
		// Say why, in case the daemon can still hear us: Unavailable puts it
		// on the redial path rather than leaving it with a bare EOF.
		sc.conn.closeWith(errDaemonStreamSilent)
		s.teardownConnection(sc.conn, "stale-connection sweeper")
	}
}

// errDaemonStreamSilent ends a stream the stale-connection sweeper reaped.
//
// Unavailable is the code the daemon needs (it puts it on the redial path),
// but the fault is not ours: the other end went quiet — a laptop that slept,
// a network that dropped — which is the canceled class ("one end went away").
// Unclassified, every reaped stream was an ERROR "stream failed" issue. It is
// a svcerr kind, not a *connect.Error, so the class stays on the chain every
// logging layer classifies; the svcerr interceptor puts the code on the wire.
var errDaemonStreamSilent = svcerr.WithClass(svcerr.Unavailable(fmt.Sprintf(
	"daemon-gateway has read nothing from this stream for over %s; closing it so the daemon redials",
	staleConnectionThreshold)), svcerr.ClassCanceled)

// AddConnectionListener registers a listener that will be notified when
// daemon connections are established or torn down. Safe to call before Start.
func (s *Service) AddConnectionListener(l toolexec.DaemonConnectionListener) {
	s.listenersMu.Lock()
	s.listeners = append(s.listeners, l)
	s.listenersMu.Unlock()
}

// notifyConnected calls OnDaemonConnectedWithInfo on any listener that
// implements toolexec.DaemonConnectionInfoListener (currently just
// daemonevents.Publisher), falling back to plain OnDaemonConnected for
// listeners that only care about identity (NATSToolBridge,
// daemonquery.Responder/UserResponder). Each listener gets exactly one call.
// Must be called OUTSIDE s.mu to avoid deadlocks.
func (s *Service) notifyConnected(userID, daemonID, name, hostname, platform string) {
	s.listenersMu.RLock()
	defer s.listenersMu.RUnlock()
	for _, l := range s.listeners {
		if infoListener, ok := l.(toolexec.DaemonConnectionInfoListener); ok {
			infoListener.OnDaemonConnectedWithInfo(userID, daemonID, name, hostname, platform)
			continue
		}
		l.OnDaemonConnected(userID, daemonID)
	}
}

// notifyDisconnected calls OnDaemonDisconnected on all registered listeners.
// Must be called OUTSIDE s.mu to avoid deadlocks.
func (s *Service) notifyDisconnected(userID, daemonID string) {
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
func (s *Service) removeConnection(daemonID, userID string, expected *daemonConnection) bool {
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
// listeners, and best-effort release the session's daemon_attachment lease.
//
// Idempotent and safe to call multiple times (e.g. from both a `defer` in
// the stream handler AND from the stale-connection sweeper) — only the
// first call that finds the connection still registered actually does the
// work.
func (s *Service) teardownConnection(conn *daemonConnection, reason string) {
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
	conn.failLocalModelRelays("daemon disconnected")

	if removed {
		s.notifyDisconnected(userID, daemonID)
		if s.deps.StatePublisher != nil {
			s.deps.StatePublisher.Disconnected(daemonID, userID, daemonType)
		}
		// Only when this connection still held the slot: one superseded in
		// this process leaves the lease to its successor, which wrote its
		// own.
		if err := s.releaseLease(context.Background(), conn); err != nil {
			logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Failed to release daemon attachment", "error", err, "daemonID", daemonID, "reason", reason)
		}
		// After the release, so a client refetching on this signal already
		// reads the daemon as detached.
		s.publishDaemonListChanged(userID, daemonID)
	}
}

// leaseTime is a lease timestamp at daemon_attachment's precision
// (timestamptz, microseconds), so the attached_at a session remembers is the
// attached_at it wrote, and ReleaseDaemonAttachment's bound compares equal.
func leaseTime(t time.Time) time.Time {
	return t.UTC().Truncate(time.Microsecond)
}

// releaseLease deletes this session's reachability lease as its stream ends:
// the row for the daemon whose attached_at is this session's or older
// (db.Repo.ReleaseDaemonAttachment), never a newer session's.
//
// A daemon id is held by one stream at a time, but not by one PROCESS. A
// daemon that loses this gateway reconnects to another replica, while this
// replica may only tear its half of the stream down a sweep later — and a
// delete keyed by daemon id alone then removed the lease the new session had
// just written, so the registry read a serving daemon as DISCONNECTED, and the
// list-changed announcement below sent every client to read exactly that.
//
// Under leaseMu, and after teardown closed done: a renewal already in flight
// finishes first and this release wins; any later one sees done closed and
// does nothing (renewLease).
func (s *Service) releaseLease(ctx context.Context, conn *daemonConnection) error {
	if s.deps.Repo == nil || conn.attachedAt.IsZero() {
		return nil // no lease was written for this connection
	}
	conn.leaseMu.Lock()
	defer conn.leaseMu.Unlock()
	return s.deps.Repo.ReleaseDaemonAttachment(ctx, conn.daemonID, conn.attachedAt)
}

// errDaemonSuperseded ends the stream of a connection whose daemonID slot was
// claimed by another daemon PROCESS.
//
// A daemon process never holds two sessions at once — daemonClient.run calls
// runSession sequentially and only redials after the previous session's
// Receive() returned. So a connection that is still being served when a fresh
// registration takes its slot is, by construction, a DIFFERENT process
// asserting the same identity. Left alone the two evict each other forever, on
// a period set by the heartbeat interval (measured 2026-07-27: two local
// daemons sharing one daemonID, sessions 30.00s ±12ms for 37 minutes, every
// in-flight tool request lost with each flap).
//
// CodeAborted ("concurrency conflict") is the daemon's cue to stop rather than
// redial — see isFatalError in internal/toolexec/daemonruntime.
var errDaemonSuperseded = svcerr.ToConnect(svcerr.Aborted("daemon identity claimed by another daemon process on the same gateway; " +
	"this process is superseded and must not reconnect (only one daemon may hold a daemon id)"))

func (c *daemonConnection) closeDone() { c.closeWith(nil) }

// releaseStream ends every use of the connection's stream and returns once
// there is none left: done closes (the sender stops), the stream is aborted
// (a pending Receive returns), and the sender is joined. The stream's owner
// calls it before it lets the stream go — before ConnectDaemon returns,
// before the connector closes an outbound stream. Idempotent; call it only
// from the owner's goroutine.
//
// It can wait out one Send already blocked on a daemon that stopped reading,
// which the listener's write-stall guard bounds (serverkit.WriteStallGuard).
func (c *daemonConnection) releaseStream() {
	c.closeDone()
	if c.stream != nil {
		c.stream.abort()
	}
	c.streamUsers.Wait()
}

// closeWith closes the connection's done channel, recording why. The reason is
// what the stream handler returns to the daemon, so a superseded daemon can
// tell "another process took my identity" from an ordinary EOF.
func (c *daemonConnection) closeWith(reason error) {
	if c == nil {
		return
	}
	c.doneOnce.Do(func() {
		c.closeReason.Store(&reason)
		// A connection built without a done channel has no handler parked on
		// it and nothing to unblock. Guard rather than panic: this runs on
		// the shutdown path, where a panic takes out the drain for every
		// OTHER daemon still holding a stream.
		if c.done != nil {
			close(c.done)
		}
	})
}

// supersedeIncumbent ends the connection that previously held a daemon id
// because a NEWER inbound registration claimed it. It is not an eviction of a
// stale duplicate of the same process: a daemon process runs its sessions
// strictly one at a time, so a connection still being served when a fresh
// registration arrives belongs to a different process. Ending it with
// errDaemonSuperseded is what stops the two from evicting each other forever.
func (s *Service) supersedeIncumbent(old *daemonConnection) {
	if old == nil {
		return
	}
	old.closeWith(errDaemonSuperseded)
	logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Daemon id claimed by another daemon process; superseding the incumbent connection",
		"userID", old.userID, "daemonID", old.daemonID)
}

// closedReason returns the error recorded by closeWith, or nil.
func (c *daemonConnection) closedReason() error {
	if c == nil {
		return nil
	}
	if r := c.closeReason.Load(); r != nil {
		return *r
	}
	return nil
}

// normalizeProjectPath canonicalizes a project path reported by a daemon, or
// returns "" when the daemon sent something that names no location.
//
// It uses ospath rather than path/filepath because the path describes the
// DAEMON's filesystem. filepath.Clean on a Linux server leaves a Windows
// path's backslashes untouched while filepath.IsAbs judges it relative, so
// every Windows daemon's project path normalized to "" — and "" is a silent
// drop at all three call sites.
func normalizeProjectPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	cleaned := ospath.Clean(trimmed)
	if !ospath.IsAbs(cleaned) {
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
