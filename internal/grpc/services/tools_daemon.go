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
	"github.com/reliant-labs/reliant/internal/auth"
	cfgpkg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

const (
	LOG_PREFIX_TOOLS_DAEMON = "[🔧 ToolsDaemon]"

	// Heartbeat interval for keeping connections alive and for the stale sweep ticker.
	daemonHeartbeatInterval = 15 * time.Second
	// Daemon is considered stale when no heartbeat has been seen for this duration (2× heartbeat).
	daemonStaleAfter = 30 * time.Second
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

	monitorCancel context.CancelFunc
	monitorDone   chan struct{}

	// userUpdateHub is used to publish ephemeral daemon heartbeat events
	// to the frontend via the streaming connection. Set via SetUserUpdateHub.
	userUpdateHub streaming.UpdateHub[db.UserUpdate]
}

// daemonConnection represents an active daemon connection
type daemonConnection struct {
	userID       string
	daemonID     string
	name         string
	labels       map[string]string
	daemonType   string // "local" or "cloud"
	connectedAt  time.Time
	lastActivity time.Time
	stream       *connect.BidiStream[reliantv1.DaemonMessage, reliantv1.ServerMessage]
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
}

// NewToolsDaemonService creates a new ToolsDaemonService with a background
// stale-daemon monitor goroutine. Use NewToolsDaemonServiceWithoutMonitor for
// deployments (e.g. cloud api-server) that never accept daemon connections.
func NewToolsDaemonService(database db.Repository) *ToolsDaemonService {
	monitorCtx, monitorCancel := context.WithCancel(context.Background())

	service := &ToolsDaemonService{
		database:      database,
		connections:   make(map[string]*daemonConnection),
		userDaemons:   make(map[string][]string),
		monitorCancel: monitorCancel,
		monitorDone:   make(chan struct{}),
	}

	go service.runStaleDaemonMonitor(monitorCtx)

	return service
}

// NewToolsDaemonServiceWithoutMonitor creates a ToolsDaemonService that does
// not start the background stale-daemon monitor. This is intended for
// environments (like cloud api-server replicas) where no daemons connect
// directly, so the periodic DB sweep is unnecessary.
func NewToolsDaemonServiceWithoutMonitor(database db.Repository) *ToolsDaemonService {
	done := make(chan struct{})
	close(done) // no monitor goroutine, so mark done immediately
	return &ToolsDaemonService{
		database:    database,
		connections: make(map[string]*daemonConnection),
		userDaemons: make(map[string][]string),
		monitorDone: done,
	}
}

// SetUserUpdateHub configures the hub used to publish ephemeral daemon
// heartbeat events to the frontend streaming connection.
func (s *ToolsDaemonService) SetUserUpdateHub(hub streaming.UpdateHub[db.UserUpdate]) {
	s.userUpdateHub = hub
}

// Close stops background workers owned by the daemon service.
func (s *ToolsDaemonService) Close() {
	if s == nil {
		return
	}
	if s.monitorCancel != nil {
		s.monitorCancel()
	}
	if s.monitorDone != nil {
		<-s.monitorDone
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

// publishDaemonHeartbeat publishes an ephemeral daemon heartbeat event
// through the UserUpdateHub so the frontend knows the daemon is alive.
// This is NOT persisted to the database — it's a fire-and-forget notification.
func (s *ToolsDaemonService) publishDaemonHeartbeat(_ context.Context, userID, daemonID string, ts time.Time) {
	if s.userUpdateHub == nil {
		return
	}
	data, _ := json.Marshal(map[string]interface{}{
		"daemon_id":      daemonID,
		"last_heartbeat": ts.Unix(),
	})
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

func (s *ToolsDaemonService) runStaleDaemonMonitor(ctx context.Context) {
	defer close(s.monitorDone)

	ticker := time.NewTicker(daemonHeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.sweepStaleDaemons(ctx, time.Now().UTC()); err != nil {
				logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Failed stale-daemon sweep", "error", err)
			}
		}
	}
}

func (s *ToolsDaemonService) sweepStaleDaemons(ctx context.Context, now time.Time) error {
	cutoff := now.Add(-daemonStaleAfter)
	staleDaemons, err := s.database.ListStaleActiveDaemons(ctx, cutoff)
	if err != nil {
		return err
	}
	if len(staleDaemons) == 0 {
		return nil
	}

	daemonIDs := make([]string, 0, len(staleDaemons))
	daemonIDSet := make(map[string]struct{}, len(staleDaemons))
	for _, daemon := range staleDaemons {
		if daemon == nil || strings.TrimSpace(daemon.ID) == "" {
			continue
		}
		daemonIDs = append(daemonIDs, daemon.ID)
		daemonIDSet[daemon.ID] = struct{}{}
	}
	if len(daemonIDs) == 0 {
		return nil
	}

	if err := s.database.MarkDaemonsDisconnected(ctx, daemonIDs, now); err != nil {
		return err
	}

	type removedConn struct{ userID, daemonID string }
	var removed []removedConn
	s.mu.Lock()
	for dID, conn := range s.connections {
		if conn == nil {
			continue
		}
		if _, ok := daemonIDSet[dID]; !ok {
			continue
		}
		delete(s.connections, dID)
		// Remove from userDaemons secondary index.
		uIDs := s.userDaemons[conn.userID]
		for i, id := range uIDs {
			if id == dID {
				s.userDaemons[conn.userID] = append(uIDs[:i], uIDs[i+1:]...)
				break
			}
		}
		if len(s.userDaemons[conn.userID]) == 0 {
			delete(s.userDaemons, conn.userID)
		}
		conn.closeDone()
		removed = append(removed, removedConn{conn.userID, dID})
	}
	s.mu.Unlock()

	// Notify listeners outside the mutex.
	for _, rc := range removed {
		s.notifyDisconnected(rc.userID, rc.daemonID)
	}

	logging.Info(LOG_PREFIX_TOOLS_DAEMON+" Marked stale daemons disconnected",
		"count", len(daemonIDs),
		"cutoff", cutoff.Format(time.RFC3339),
	)

	return nil
}

func daemonRegistrationUserID(ctx context.Context, reg *reliantv1.DaemonRegister) (string, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || strings.TrimSpace(userID) == "" {
		return "", connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("PAT authentication required — no user identity in context"))
	}
	return userID, nil
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
			"registerUserID", reg.UserId,
		)
		return err
	}

	daemonID := reg.DaemonId

	if daemonID == "" {
		logging.Error(LOG_PREFIX_TOOLS_DAEMON + " Missing daemon_id in registration")
		return connect.NewError(connect.CodeInvalidArgument, nil)
	}

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
		ID:            daemonID,
		UserID:        userID,
		Hostname:      daemonStringPtrOrNil(reg.Hostname),
		Platform:      daemonStringPtrOrNil(reg.Platform),
		Status:        db.DaemonStatusActive,
		Capabilities:  capabilitiesJSON,
		ProjectPaths:  projectPathsJSON,
		ConnectedAt:   &now,
		LastHeartbeat: &now,
	}); err != nil {
		logging.Error(LOG_PREFIX_TOOLS_DAEMON+" Failed to persist daemon registration", "error", err, "daemonID", daemonID)
		return connect.NewError(connect.CodeInternal, fmt.Errorf("failed to persist daemon registration: %w", err))
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
		stream:              stream,
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

	// Publish an immediate heartbeat so the frontend knows the daemon is
	// online without waiting for the first periodic heartbeat (up to 15s).
	s.publishDaemonHeartbeat(context.Background(), userID, daemonID, time.Now().UTC())

	// Send cloud-refactor registration acknowledgment containing config pull hints.
	regAck := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_RegistrationAck{
			RegistrationAck: &reliantv1.RegistrationAck{
				Accepted:              true,
				RequestedProjectPaths: requestedProjectPaths,
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

	for _, projectPath := range requestedProjectPaths {
		if err := s.sendLoadAndWatchProjectConfig(ctx, conn, projectPath, true); err != nil {
			logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Failed to request initial project config load/watch",
				"error", err,
				"daemonID", conn.daemonID,
				"projectPath", projectPath,
			)
		}
	}

	// Handle incoming messages (blocking)
	err = s.handleIncoming(ctx, conn)

	// Cleanup on disconnect: remove from primary map and user's daemon list.
	var wasConnected bool
	s.mu.Lock()
	if s.connections[daemonID] == conn {
		delete(s.connections, daemonID)
		// Remove from userDaemons secondary index.
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
		wasConnected = true
	}
	s.mu.Unlock()
	conn.closeDone()
	conn.closeAllSubscribers()

	// Notify listeners outside the mutex.
	if wasConnected {
		s.notifyDisconnected(userID, daemonID)
	}

	disconnectedAt := time.Now().UTC()
	if err := s.database.UpdateDaemonStatus(context.Background(), daemonID, db.DaemonStatusDisconnected, nil, nil, &disconnectedAt); err != nil {
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Failed to mark daemon disconnected", "daemonID", daemonID, "error", err)
	}

	logging.Info(LOG_PREFIX_TOOLS_DAEMON+" Daemon disconnected", "userID", userID)
	return err
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
			now := time.Now().UTC()
			if err := s.database.UpdateDaemonHeartbeat(ctx, conn.daemonID, now); err != nil {
				logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Failed to update heartbeat", "daemonID", conn.daemonID, "error", err)
			}
			s.publishDaemonHeartbeat(ctx, conn.userID, conn.daemonID, now)

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

	// Refresh git status for existing projects if daemon discovery reports a different value
	if discovered != nil && discovered.IsGitRepo != project.IsGitRepo {
		project.IsGitRepo = discovered.IsGitRepo
		project.UpdatedAt = time.Now().UTC()
		if updateErr := s.database.UpdateProject(ctx, project, project.UserID); updateErr != nil {
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

	select {
	case conn.sendCh <- msg:
		return nil
	case <-conn.done:
		return fmt.Errorf("daemon connection closed for user %s", userID)
	default:
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" daemon send buffer full, message dropped", "userID", userID)
		return fmt.Errorf("daemon send buffer full, message dropped for user %s", userID)
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

// SendDaemonCommand sends a generic command to the daemon and waits for a correlated response.
func (s *ToolsDaemonService) SendDaemonCommand(ctx context.Context, userID string, req *reliantv1.DaemonCommandRequest) (*reliantv1.DaemonCommandResponse, error) {
	s.mu.RLock()
	conn := s.defaultDaemonForUser(userID)
	s.mu.RUnlock()
	if conn == nil {
		return nil, fmt.Errorf("no daemon connected for user %s", userID)
	}

	// Register a pending response channel.
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

	// Send the command to the daemon.
	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_DaemonCommand{
			DaemonCommand: req,
		},
	}
	if err := s.sendToUserDaemon(userID, msg); err != nil {
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
		s.cancelDaemonCommand(userID, req.RequestId, "daemon command timed out")
		return nil, fmt.Errorf("daemon command %q timed out after %s", req.CommandType, timeout)
	case <-conn.done:
		return nil, fmt.Errorf("daemon disconnected while waiting for command %q response", req.CommandType)
	case <-ctx.Done():
		s.cancelDaemonCommand(userID, req.RequestId, "daemon command caller cancelled")
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

	conn.terminalSubsMu.Lock()
	conn.terminalSubs[sessionID] = append(conn.terminalSubs[sessionID], ch)
	conn.terminalSubsMu.Unlock()

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
