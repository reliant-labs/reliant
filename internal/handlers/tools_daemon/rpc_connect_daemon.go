package tools_daemon

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
)

func truncateForLog(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

// rejectReservedDaemon refuses an unbound credential (a login token)
// connecting as a daemon id reserved for daemon-bound credentials — every
// managed cloud machine, and any daemon a bound credential has connected as.
// The caller owns the daemon; what is refused is standing in for the
// machine's own credential, which would take over its identity and supersede
// its live connection. Like rejectForeignDaemon it runs before any
// connection-map or NATS side effect.
//
// The refusal carries bootstrap.ReservedDaemonIDReason so the daemon treats
// it as an identity problem, never a credential one: a daemon that read the
// id from its own saved file sets it aside and registers as a new machine.
func (s *Service) rejectReservedDaemon(ctx context.Context, userID, daemonID string) error {
	logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Rejected an unbound credential connecting as a daemon id reserved for daemon-bound credentials",
		"daemonID", daemonID, "userID", userID)
	return bootstrap.NewReservedDaemonIDError()
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

	if err := validateAssertedDaemonID(reg.GetDaemonId()); err != nil {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Rejected malformed client-asserted daemon_id",
			"userID", userID, "assertedDaemonID", truncateForLog(reg.GetDaemonId()))
		return err
	}

	// A credential bound to a daemon id reserves that id (and only that id:
	// the bound id wins over any asserted one); an unbound login token may
	// not connect as a reserved id.
	boundID := auth.GetDaemonIDFromContext(ctx)
	boundCredential := strings.TrimSpace(boundID) != ""
	daemonID := resolveDaemonID(
		boundID,
		reg.GetDaemonId(),
		func() string {
			return resolveUnboundDaemonID(ctx, s.deps.Repo, userID, reg.GetHostname())
		},
	)

	// The machine's org, as its daemon-bound credential's introspection
	// named it (control-plane's daemons.org_id). Only a bound credential
	// speaks for the machine: the bound id is this daemonID (resolveDaemonID).
	credentialOrg := ""
	if boundCredential {
		credentialOrg = auth.GetDaemonOrgIDFromContext(ctx)
	}

	requestedProjectPaths, err := s.daemonProjectPaths(ctx, daemonID)
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
		// Reserves the id for bound credentials; an unbound registration on a
		// reserved id writes nothing and is refused below.
		RequiresBoundToken: boundCredential,
		OrgID:              daemonStringPtrOrNil(credentialOrg),
	}); err != nil {
		if errors.Is(err, db.ErrDaemonOwnedByAnotherUser) {
			return s.rejectForeignDaemon(ctx, userID, daemonID)
		}
		if errors.Is(err, db.ErrDaemonRequiresBoundToken) {
			return s.rejectReservedDaemon(ctx, userID, daemonID)
		}
		return svcerr.WrapOr(err, svcerr.Internal("failed to persist daemon registration"))
	}
	// Before the list-changed signal below, so a refetch reads the labels.
	s.persistDeclaredLabels(ctx, userID, daemonID, boundCredential, reg.GetMachineLabels())

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

	machineOrg, machineOrgUnresolved := s.machineOrgAtConnect(ctx, userID, daemonID, MachineOrg{ID: credentialOrg})

	// Create daemon connection
	now2 := time.Now().UTC()
	conn := &daemonConnection{
		userID:                 userID,
		daemonID:               daemonID,
		orgID:                  machineOrg,
		orgUnresolved:          machineOrgUnresolved,
		name:                   reg.GetName(),
		hostname:               reg.GetHostname(),
		platform:               reg.GetPlatform(),
		credentialOrgID:        auth.GetCredentialOrgFromContext(ctx),
		labels:                 reg.GetLabels(),
		daemonType:             reg.GetDaemonType(),
		boundCredential:        boundCredential,
		connectedAt:            now2,
		lastActivity:           now2,
		lastReceived:           now2,
		attachedAt:             now,
		stream:                 &inboundStream{stream: stream, ctx: ctx},
		attachSource:           db.DaemonAttachmentSourceInbound,
		network:                daemonNetworkOf(stream.RequestHeader(), stream.Peer().Addr),
		movesOnDrain:           reg.GetMovesOnDrain(),
		drainsOnRestartRequest: reg.GetDrainsOnRestartRequest(),
		build:                  daemonBuildFromRegister(reg),
		sendCh:                 make(chan *reliantv1.ServerMessage, 256),
		link:                   newLinkState(now2),
		done:                   make(chan struct{}),
		pendingCommands:        make(map[string]chan *reliantv1.DaemonCommandResponse),
		pendingToolRequests:    make(map[string]chan *toolexec.ToolExecutionResponse),
		terminalSubs:           make(map[string][]*toolexec.TerminalOutputQueue),
		processOutputSubs:      make(map[string][]chan *toolexec.ProcessOutputEvent),
	}

	// The stream's server span: which daemon this is and where it dialed from.
	trace.SpanFromContext(ctx).SetAttributes(daemonConnAttributes(conn)...)
	// Register connection: keyed by daemonID, with secondary index by userID.
	s.mu.Lock()
	oldConn := s.connections[daemonID]
	if oldConn != nil && oldConn.userID != userID {
		s.mu.Unlock()
		return s.rejectForeignDaemon(ctx, userID, daemonID)
	}
	if oldConn != nil && oldConn.boundCredential && !boundCredential {
		s.mu.Unlock()
		return s.rejectReservedDaemon(ctx, userID, daemonID)
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

	// Notify listeners outside the mutex.
	s.notifyConnected(userID, daemonID)
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
				AccountEmail:          accountEmail(ctx),
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

	// Prove the link, then run the connect-time work: the link-ready
	// listeners and the reconcile of project_daemons rows against what
	// actually exists on this daemon's disk (which self-heals legacy rows,
	// daemon-id churn, and the create-while-daemon-pending case). Off the
	// hot connect path, so it never blocks registration. See
	// link_liveness.go.
	//
	// The project configs are NOT requested here. The registration_ack
	// above names every project path (RequestedProjectPaths), and the
	// daemon loads and watches each on that alone. Sending
	// LoadProjectConfigs + WatchProjectConfigs for every path as well — as
	// this did until 2026-10-31 — made the daemon build each full snapshot
	// twice more (one goroutine per path, at once) and restart every
	// watcher, right after it woke: that CPU and those snapshot messages,
	// queued ahead of every command reply on its one send stream, were part
	// of the 5-8s transit a waking laptop showed.
	go s.runConnectWork(conn, true)

	// When the listener drains, tell this daemon to move.
	defer s.handOverOnDrain(ctx, conn)()

	// Handle incoming messages (blocking)
	return s.handleIncoming(ctx, conn)
}
