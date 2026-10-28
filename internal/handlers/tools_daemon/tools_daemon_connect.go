package tools_daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
)

func daemonRegistrationUserID(ctx context.Context, reg *reliantv1.DaemonRegister) (string, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || strings.TrimSpace(userID) == "" {
		return "", svcerr.Unauthenticated("PAT authentication required — no user identity in context")
	}
	return userID, nil
}

// daemonHostnameLookup is the slice of Repository needed to find an
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
			logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Failed to lookup existing daemons for hostname reuse",
				"error", lookupErr, "userID", userID, "hostname", hostname)
		} else {
			for _, d := range existing {
				if d.Hostname != nil && *d.Hostname == hostname {
					logging.InfoContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Reusing existing daemon_id for unbound PAT",
						"daemonID", d.ID, "userID", userID, "hostname", hostname)
					return d.ID
				}
			}
		}
	}
	id := uuid.NewString()
	logging.InfoContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Generated daemon_id for unbound PAT",
		"daemonID", id, "userID", userID, "hostname", hostname)
	return id
}

// assertedDaemonIDPattern is the shape of every legitimate daemon id: UUIDs
// (control-plane svcdaemon mints uuid.New().String(); the gateway mints
// uuid.NewString()). It is also what keeps an id safe to embed in NATS
// subjects and consumer names without the lossy sanitizer.
var assertedDaemonIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// validateAssertedDaemonID rejects (never sanitizes) a malformed client-asserted
// daemon id. An empty id is fine: it means "none asserted".
func validateAssertedDaemonID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" || assertedDaemonIDPattern.MatchString(id) {
		return nil
	}
	return svcerr.InvalidArgument("daemon_id must match [A-Za-z0-9-]{1,64}")
}

func truncateForLog(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

// rejectForeignDaemon refuses a registration whose daemon id belongs to
// another user. It runs before any connection-map or NATS side effect.
//
// The refusal carries bootstrap.ForeignDaemonIDReason so the daemon can tell
// it from a bad credential: a daemon that read the id from its own saved file
// sets the file aside and registers again with no id (see daemonruntime). The
// owner is logged here and never returned — the caller learns that the id is
// someone else's, not whose.
func (s *Service) rejectForeignDaemon(ctx context.Context, userID, daemonID string) error {
	ownerID := "unknown"
	if d, err := s.deps.Repo.GetDaemon(ctx, daemonID); err == nil && d != nil {
		ownerID = d.UserID
	} else {
		s.mu.RLock()
		if c := s.connections[daemonID]; c != nil {
			ownerID = c.userID
		}
		s.mu.RUnlock()
	}
	logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Rejected daemon registration for a daemon id owned by another user",
		"daemonID", daemonID, "requestingUserID", userID, "ownerUserID", ownerID)
	return bootstrap.NewForeignDaemonIDError()
}

// WhoAmI names the account the presented daemon credential acts as. The daemon
// auth interceptor has already resolved the credential; this reads the result
// and touches nothing else — no daemon row, no connection, no NATS.
//
// `reliant daemon start --token` asks before deriving the daemon's instance
// directory, so each account on a machine keeps its own saved daemon id.
func (s *Service) WhoAmI(
	ctx context.Context,
	_ *connect.Request[reliantv1.WhoAmIRequest],
) (*connect.Response[reliantv1.WhoAmIResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || strings.TrimSpace(userID) == "" {
		return nil, svcerr.Unauthenticated("a daemon credential is required")
	}
	return connect.NewResponse(&reliantv1.WhoAmIResponse{UserId: userID}), nil
}

// ConnectDaemon implements the bidirectional streaming RPC for daemon connections
func (s *Service) ConnectDaemon(
	ctx context.Context,
	stream *connect.BidiStream[reliantv1.DaemonMessage, reliantv1.ServerMessage],
) error {
	// This stream says its own goodbye — its hand-over tells the daemon to
	// move (GatewayDrain, handOverOnDrain below) — so it claims the listener
	// drain's soft phase: the drain leaves it open for the replies still in
	// flight on it, and ends it only at the drain's hard deadline.
	listenerDraining := serverkit.Draining(ctx)

	// A draining replica takes no new daemon: one moving off it, or one the
	// load balancer has not stopped sending yet, redials and lands on a
	// replica that is still serving. Unavailable keeps it on that path.
	select {
	case <-listenerDraining:
		return errGatewayDraining
	default:
	}
	if s.draining.Load() {
		return errGatewayDraining
	}

	// The stream is read on a goroutine of its own (handleIncoming), which
	// this handler must stop before it returns; only an abortable request
	// body lets it stop one parked in Receive. Every serverkit listener
	// serves one (serverkit.AbortReceive); a server built any other way does
	// not, and the stream is refused rather than one day hanging on, or
	// racing, a parked Receive.
	if !serverkit.CanAbortReceive(ctx) {
		logging.ErrorContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" ConnectDaemon is served without an abortable request body; serve it through a serverkit listener")
		return svcerr.Internal("daemon stream handler is misconfigured")
	}

	// Wait for registration message
	msg, err := stream.Receive()
	if err != nil {
		logging.ErrorContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Failed to receive registration", "error", err)
		return err
	}

	reg := msg.GetRegister()
	if reg == nil {
		return svcerr.InvalidArgument("first message must be registration")
	}

	userID, err := daemonRegistrationUserID(ctx, reg)
	if err != nil {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Failed daemon registration identity validation",
			"error", err,
		)
		return err
	}

	if _, err := uuid.Parse(reg.GetIncarnationId()); err != nil {
		return svcerr.FailedPrecondition("daemon process identity is required")
	}
	if err := validateAssertedDaemonID(reg.GetDaemonId()); err != nil {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Rejected malformed client-asserted daemon_id",
			"userID", userID, "assertedDaemonID", truncateForLog(reg.GetDaemonId()))
		return err
	}

	daemonID := resolveDaemonID(
		auth.GetDaemonIDFromContext(ctx),
		reg.GetDaemonId(),
		func() string {
			return resolveUnboundDaemonID(ctx, s.deps.Repo, userID, reg.GetHostname())
		},
	)

	requestedProjectPaths, err := s.listAllProjectPaths(ctx, userID)
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("failed to list project paths"))
	}

	now := leaseTime(time.Now())
	capabilitiesJSON, err := jsonStringSlicePtr(reg.Capabilities)
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("failed to encode capabilities"))
	}
	projectPathsJSON, err := jsonStringSlicePtr(requestedProjectPaths)
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("failed to encode project paths"))
	}

	if err := s.deps.Repo.UpsertDaemon(ctx, &db.Daemon{
		ID:           daemonID,
		UserID:       userID,
		Hostname:     daemonStringPtrOrNil(reg.Hostname),
		Platform:     daemonStringPtrOrNil(reg.Platform),
		Capabilities: capabilitiesJSON,
		ProjectPaths: projectPathsJSON,
		DaemonType:   normalizeRegisteredDaemonType(reg.GetDaemonType()),
	}); err != nil {
		if errors.Is(err, db.ErrDaemonOwnedByAnotherUser) {
			return s.rejectForeignDaemon(ctx, userID, daemonID)
		}
		return svcerr.WrapOr(err, svcerr.Internal("failed to persist daemon registration"))
	}

	if err := s.deps.Repo.UpsertDaemonAttachment(ctx, &db.DaemonAttachment{
		DaemonID:           daemonID,
		UserID:             userID,
		Source:             db.DaemonAttachmentSourceInbound,
		AttachedAt:         now,
		LastStreamActivity: now,
		Build:              daemonBuildFromRegister(reg),
	}); err != nil {
		// The stream is still accepted: the first keepalive re-creates the
		// lease (renewLease) and says so. Until then the registry reports
		// this daemon detached, so this is an error, not a warning.
		logging.ErrorContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Failed to write the reachability lease for a new daemon stream; "+
			"the registry reports it detached until its first keepalive re-creates the lease",
			"error", err, "daemonID", daemonID, "userID", userID)
	}

	logging.InfoContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Daemon registered",
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
		hostname:            reg.GetHostname(),
		platform:            reg.GetPlatform(),
		labels:              reg.GetLabels(),
		daemonType:          reg.GetDaemonType(),
		connectedAt:         now2,
		lastActivity:        now2,
		lastReceived:        now2,
		attachedAt:          now,
		stream:              &inboundStream{stream: stream, ctx: ctx},
		attachSource:        db.DaemonAttachmentSourceInbound,
		movesOnDrain:        reg.GetMovesOnDrain(),
		build:               daemonBuildFromRegister(reg),
		sendCh:              make(chan *reliantv1.ServerMessage, 256),
		done:                make(chan struct{}),
		pendingCommands:     make(map[string]chan *reliantv1.DaemonCommandResponse),
		pendingToolRequests: make(map[string]chan *toolexec.ToolExecutionResponse),
		terminalSubs:        make(map[string][]chan *toolexec.TerminalOutputEvent),
		processOutputSubs:   make(map[string][]chan *toolexec.ProcessOutputEvent),
	}

	conn.incarnationID = reg.GetIncarnationId()
	// Closed to new work until durable intent has been restored below.
	conn.restart.Store(&reliantv1.DaemonRestartStatus{})
	// Register connection: keyed by daemonID, with secondary index by userID.
	s.mu.Lock()
	oldConn := s.connections[daemonID]
	if oldConn != nil && oldConn.userID != userID {
		s.mu.Unlock()
		return s.rejectForeignDaemon(ctx, userID, daemonID)
	}
	s.connections[daemonID] = conn
	if oldConn == nil {
		// New daemon — add to user's daemon list.
		s.userDaemons[userID] = append(s.userDaemons[userID], daemonID)
	}
	s.mu.Unlock()

	// Cleanup on disconnect via defer so it runs even on panic from the
	// receive loop, and on a registration_ack that never went out.
	// Idempotent — safe if the sweeper or a reconnect-driven replace already
	// removed the slot.
	defer func() {
		s.teardownConnection(conn, "inbound stream ended")
		logging.InfoContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Daemon disconnected", "userID", userID, "daemonID", daemonID)
	}()

	// Close old connection if this daemon id was already connected.
	s.supersedeIncumbent(oldConn)

	// Notify listeners outside the mutex. Admission was set before the slot
	// became visible; persist after ownership checks, before announcing it.
	if err := s.restoreRestart(ctx, conn, reg); err != nil {
		return svcerr.Wrap(err)
	}
	s.notifyConnected(userID, daemonID, conn.name, conn.hostname, conn.platform)
	if conn.restart.Load() != nil {
		s.notifyUpdating(userID, daemonID)
	}
	if s.deps.StatePublisher != nil {
		s.deps.StatePublisher.Connected(daemonID, userID, conn.daemonType, wireBuild(conn.build))
	}

	// Publish an immediate heartbeat so the frontend knows the daemon is
	// online without waiting for the first periodic heartbeat (up to 15s).
	s.publishDaemonHeartbeat(context.Background(), userID, daemonID, time.Now().UTC(), nil)
	// The registry rows (daemons + daemon_attachment) were written above, so
	// a client refetching on this signal already reads the daemon as attached.
	s.publishDaemonListChanged(userID, daemonID)

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
		logging.ErrorContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Failed to send registration_ack", "error", err)
		return err
	}

	// Start sender goroutine. It uses the stream, so it is joined before
	// this handler returns (deferred after teardown's defer, so it runs
	// first; handleIncoming has normally released the stream already).
	s.startSender(conn)
	defer conn.releaseStream()

	// Start heartbeat goroutine
	go s.runHeartbeat(conn)

	// Reconcile project_daemons rows against what actually exists on this
	// daemon's disk. Runs off the hot connect path so it never blocks
	// registration; self-heals legacy rows, daemon-id churn, and the
	// create-while-daemon-pending case. See reconcileProjectDaemonsOnConnect.
	go s.reconcileProjectDaemonsOnConnect(conn)

	for _, projectPath := range requestedProjectPaths {
		if err := s.sendLoadAndWatchProjectConfig(ctx, conn, projectPath, true); err != nil {
			logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Failed to request initial project config load/watch",
				"error", err,
				"daemonID", conn.daemonID,
				"projectPath", projectPath,
			)
		}
	}

	// When the listener drains, tell this daemon to move.
	defer s.handOverOnDrain(ctx, conn)()

	// Handle incoming messages (blocking)
	return s.handleIncoming(ctx, conn)
}

// RegisterOutboundConnection registers an outbound gateway→daemon connection.
// The DaemonConnector calls this after opening a ConnectGateway bidi stream and
// receiving the DaemonRegister message. Identity (userID, daemonID) is provided
// by the caller (from the NATS command), not from the daemon's register message.
// The caller is responsible for running the receive loop via HandleIncoming.
// cancel cancels the context stream was opened with; the connection calls it
// to end a pending Receive when it stops reading (releaseStream).
func (s *Service) RegisterOutboundConnection(
	ctx context.Context,
	userID, daemonID, podIP string,
	podPort int,
	reg *reliantv1.DaemonRegister,
	stream *connect.BidiStreamForClient[reliantv1.ServerMessage, reliantv1.DaemonMessage],
	cancel context.CancelFunc,
) (*OutboundConn, error) {
	if daemonID == "" || userID == "" {
		return nil, fmt.Errorf("missing daemonID or userID")
	}
	if s.draining.Load() {
		return nil, errGatewayDraining
	}
	if _, err := uuid.Parse(reg.GetIncarnationId()); err != nil {
		return nil, svcerr.FailedPrecondition("daemon process identity is required")
	}

	now := leaseTime(time.Now())
	capabilitiesJSON, err := jsonStringSlicePtr(reg.Capabilities)
	if err != nil {
		return nil, fmt.Errorf("failed to encode capabilities: %w", err)
	}

	if err := s.deps.Repo.UpsertDaemon(ctx, &db.Daemon{
		ID:           daemonID,
		UserID:       userID,
		Hostname:     daemonStringPtrOrNil(reg.Hostname),
		Platform:     daemonStringPtrOrNil(reg.Platform),
		Capabilities: capabilitiesJSON,
		DaemonType:   normalizeRegisteredDaemonType(reg.GetDaemonType()),
	}); err != nil {
		if errors.Is(err, db.ErrDaemonOwnedByAnotherUser) {
			return nil, s.rejectForeignDaemon(ctx, userID, daemonID)
		}
		return nil, fmt.Errorf("failed to persist daemon registration: %w", err)
	}

	if err := s.deps.Repo.UpsertDaemonAttachment(ctx, &db.DaemonAttachment{
		DaemonID:           daemonID,
		UserID:             userID,
		Source:             db.DaemonAttachmentSourceOutbound,
		PodIP:              &podIP,
		PodPort:            &podPort,
		AttachedAt:         now,
		LastStreamActivity: now,
		Build:              daemonBuildFromRegister(reg),
	}); err != nil {
		// See the inbound path: the first keepalive re-creates it.
		logging.ErrorContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Failed to write the reachability lease for a new daemon stream; "+
			"the registry reports it detached until its first keepalive re-creates the lease",
			"error", err, "daemonID", daemonID, "userID", userID)
	}

	conn := &daemonConnection{
		userID:              userID,
		daemonID:            daemonID,
		name:                reg.GetName(),
		hostname:            reg.GetHostname(),
		platform:            reg.GetPlatform(),
		labels:              reg.GetLabels(),
		daemonType:          reg.GetDaemonType(),
		connectedAt:         now,
		lastActivity:        now,
		lastReceived:        now,
		attachedAt:          now,
		stream:              &outboundStream{stream: stream, cancel: cancel},
		attachSource:        db.DaemonAttachmentSourceOutbound,
		podIP:               &podIP,
		podPort:             &podPort,
		sendCh:              make(chan *reliantv1.ServerMessage, 256),
		done:                make(chan struct{}),
		pendingCommands:     make(map[string]chan *reliantv1.DaemonCommandResponse),
		pendingToolRequests: make(map[string]chan *toolexec.ToolExecutionResponse),
		terminalSubs:        make(map[string][]chan *toolexec.TerminalOutputEvent),
		processOutputSubs:   make(map[string][]chan *toolexec.ProcessOutputEvent),
	}

	conn.incarnationID = reg.GetIncarnationId()
	// Closed to new work until durable intent has been restored below.
	conn.restart.Store(&reliantv1.DaemonRestartStatus{})
	// Register connection.
	s.mu.Lock()
	oldConn := s.connections[daemonID]
	if oldConn != nil && oldConn.userID != userID {
		s.mu.Unlock()
		return nil, s.rejectForeignDaemon(ctx, userID, daemonID)
	}
	s.connections[daemonID] = conn
	if oldConn == nil {
		s.userDaemons[userID] = append(s.userDaemons[userID], daemonID)
	}
	s.mu.Unlock()

	if oldConn != nil {
		oldConn.closeDone()
		logging.InfoContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Replaced old daemon connection (outbound)", "userID", userID, "daemonID", daemonID)
	}

	if err := s.restoreRestart(ctx, conn, reg); err != nil {
		s.teardownConnection(conn, "restart state write failed")
		return nil, svcerr.Wrap(err)
	}
	s.notifyConnected(userID, daemonID, conn.name, conn.hostname, conn.platform)
	if conn.restart.Load() != nil {
		s.notifyUpdating(userID, daemonID)
	}
	if s.deps.StatePublisher != nil {
		s.deps.StatePublisher.Connected(daemonID, userID, conn.daemonType, wireBuild(conn.build))
	}
	s.publishDaemonHeartbeat(context.Background(), userID, daemonID, time.Now().UTC(), nil)
	s.publishDaemonListChanged(userID, daemonID)

	// Start sender and heartbeat goroutines.
	s.startSender(conn)
	go s.runHeartbeat(conn)

	return &OutboundConn{service: s, conn: conn}, nil
}

// OutboundConn wraps a daemonConnection created via RegisterOutboundConnection.
// It exposes HandleIncoming (blocking receive loop) and Disconnect (cleanup).
type OutboundConn struct {
	service *Service
	conn    *daemonConnection
}

// HandleIncoming runs the blocking receive loop, dispatching incoming daemon
// messages exactly like ConnectDaemon does. Returns when the stream ends, and
// only once nothing of this connection uses the stream any more, so the
// caller may close it then.
func (o *OutboundConn) HandleIncoming(ctx context.Context) error {
	return o.service.handleIncoming(ctx, o.conn)
}

// Disconnect stops every use of the stream (releaseStream), removes the
// connection from the service's internal state and notifies listeners. Must
// be called by the stream's owner when the stream ends, before it closes the
// stream. Idempotent — safe to call more than once, and after the
// stale-connection sweeper already tore the connection down.
func (o *OutboundConn) Disconnect() {
	o.conn.releaseStream()
	o.service.teardownConnection(o.conn, "outbound stream ended")
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

func (s *Service) listAllProjectPaths(ctx context.Context, userID string) ([]string, error) {
	projects, err := s.deps.Repo.ListProjects(ctx, db.ProjectFilters{UserID: userID, Limit: 1000, Offset: 0})
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
