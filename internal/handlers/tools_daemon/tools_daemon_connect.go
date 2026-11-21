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
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
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
// this hostname or when hostname is empty (a machine that reported none).
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
				// A reserved row is a bound credential's machine; an unbound
				// daemon reusing it by hostname would only be refused.
				if d.RequiresBoundToken {
					continue
				}
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

// MachineOrg is what the control plane said about a machine's org when the
// gateway dials it in (the DAEMON_STATE snapshot's orgId / orgUnresolved): ID
// "" with Unresolved false is a control plane that named none; Unresolved is
// one that could not read it.
type MachineOrg struct {
	ID         string
	Unresolved bool
}

// machineOrgAtConnect is the org of the machine connecting as daemonID: the
// one the control plane just named (named.ID, recorded on its row), else the
// one its row already holds from an earlier connect, else "" — logged: the
// machine is then held to its owner's default org (authz.MachineScopeFor).
// unresolved is true when the org cannot be told at all: the control plane
// could not read it and no earlier connect recorded one, or the registry row
// could not be read. The connection then runs nothing org-sensitive.
func (s *Service) machineOrgAtConnect(ctx context.Context, userID, daemonID string, named MachineOrg) (org string, unresolved bool) {
	if named.ID != "" {
		return named.ID, false
	}
	d, err := s.deps.Repo.GetDaemon(ctx, daemonID)
	switch {
	case err != nil && !svcerr.IsNotFound(err):
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" could not read the machine's recorded organization; nothing org-sensitive runs on this connection",
			"error", err, "userID", userID, "daemonID", daemonID)
		return "", true
	case err == nil && d != nil && d.OrgID != nil && *d.OrgID != "":
		return *d.OrgID, false
	case named.Unresolved:
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" the control plane could not read the machine's organization; nothing org-sensitive runs on this connection",
			"userID", userID, "daemonID", daemonID)
		return "", true
	}
	logging.InfoContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" machine has no known organization (its credential or the control plane names none); "+
		"it holds only its owner's default organization's projects",
		"userID", userID, "daemonID", daemonID)
	return "", false
}

// accountEmail is the email of the account the daemon credential acts as, as
// the token authority named it when the daemon auth interceptor resolved the
// credential — "" when it named none. It is told only to that credential's
// own holder, so a daemon can say which account it is signed in as.
func accountEmail(ctx context.Context) string {
	email, _ := auth.GetUserEmailFromContext(ctx)
	return strings.TrimSpace(email)
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
	org MachineOrg,
) (*OutboundConn, error) {
	if daemonID == "" || userID == "" {
		return nil, fmt.Errorf("missing daemonID or userID")
	}
	if s.draining.Load() {
		return nil, errGatewayDraining
	}
	// The gateway dials only managed pods, and never refuses one for its
	// version (version_floor.go).
	floorRestart, err := s.applyVersionFloor(ctx, userID, daemonID, reg, true, true)
	if err != nil {
		return nil, err
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
		// The gateway dials only a pod the control plane's workspace
		// snapshot names, so this identity is as authoritative as a bound
		// credential and reserves the id the same way.
		RequiresBoundToken: true,
		// So is the org the same snapshot names for it.
		OrgID: daemonStringPtrOrNil(org.ID),
	}); err != nil {
		if errors.Is(err, db.ErrDaemonOwnedByAnotherUser) {
			return nil, s.rejectForeignDaemon(ctx, userID, daemonID)
		}
		return nil, fmt.Errorf("failed to persist daemon registration: %w", err)
	}
	// The gateway's dial is as authoritative as a bound credential (above).
	s.persistDeclaredLabels(ctx, userID, daemonID, true, reg.GetMachineLabels())

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

	outboundOrg, outboundOrgUnresolved := s.machineOrgAtConnect(ctx, userID, daemonID, org)
	conn := &daemonConnection{
		userID:                 userID,
		daemonID:               daemonID,
		orgID:                  outboundOrg,
		orgUnresolved:          outboundOrgUnresolved,
		name:                   reg.GetName(),
		hostname:               reg.GetHostname(),
		platform:               reg.GetPlatform(),
		labels:                 reg.GetLabels(),
		daemonType:             reg.GetDaemonType(),
		boundCredential:        true,
		drainsOnRestartRequest: reg.GetDrainsOnRestartRequest(),
		connectedAt:            now,
		lastActivity:           now,
		lastReceived:           now,
		attachedAt:             now,
		stream:                 &outboundStream{stream: stream, cancel: cancel},
		attachSource:           db.DaemonAttachmentSourceOutbound,
		podIP:                  &podIP,
		podPort:                &podPort,
		sendCh:                 make(chan *reliantv1.ServerMessage, 256),
		link:                   newLinkState(now),
		done:                   make(chan struct{}),
		pendingCommands:        make(map[string]chan *reliantv1.DaemonCommandResponse),
		pendingToolRequests:    make(map[string]chan *toolexec.ToolExecutionResponse),
		terminalSubs:           make(map[string][]*toolexec.TerminalOutputQueue),
		processOutputSubs:      make(map[string][]chan *toolexec.ProcessOutputEvent),
	}

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

	s.notifyConnected(userID, daemonID)
	if s.deps.StatePublisher != nil {
		s.deps.StatePublisher.Connected(daemonID, userID, conn.daemonType, wireBuild(conn.build))
	}
	s.publishDaemonHeartbeat(context.Background(), userID, daemonID, time.Now().UTC(), nil)
	s.publishDaemonListChanged(userID, daemonID)

	// Start sender and heartbeat goroutines.
	s.startSender(conn)
	s.sendFloorRestart(conn, floorRestart)
	go s.runHeartbeat(conn)
	// Prove the link before the link-ready listeners run (link_liveness.go).
	// A workspace pod's disk is reconciled by its own checkout flow, so no
	// reconcile pass here.
	go s.runConnectWork(conn, false)

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

// daemonProjectPaths is where THIS machine holds the user's projects: its own
// project_daemons rows, recorded by create, clone and every connect's
// reconcile. projects.path is where each project was created, on whichever
// machine created it, so it says nothing about this machine's disk. The
// daemon's project discovery replaces the list with what it finds.
func (s *Service) daemonProjectPaths(ctx context.Context, daemonID string) ([]string, error) {
	rows, err := s.deps.Repo.ListProjectDaemonsForDaemon(ctx, daemonID)
	if err != nil {
		return nil, err
	}

	paths := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if row == nil || row.InstallState != core.ProjectInstallInstalled {
			continue
		}
		normalizedPath := normalizeProjectPath(row.Path)
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
