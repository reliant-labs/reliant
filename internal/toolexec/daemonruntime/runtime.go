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
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/daemon"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/terminal"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
	"github.com/reliant-labs/reliant/internal/toolexec/transport"
)

const (
	logPrefix              = "[🔧 ToolsDaemonClient]"
	defaultProjectScanPath = "."
)

type daemonClient struct {
	daemonID string
	userID   string
	hostname string
	platform string
	cwd      string
	bootCfg  bootstrap.DaemonBootstrapConfig

	mcpManager    *mcp.Manager
	localExecutor *toolexec.LocalToolExecutor
	capabilities  []string

	// sendCh decouples message producers from the stream I/O.
	// All goroutines push messages here; a single runSender goroutine
	// drains the channel and calls stream.Send(). This prevents work
	// goroutines from blocking on the stream write mutex/I/O.
	sendCh   chan *reliantv1.DaemonMessage
	sendDone chan struct{} // closed when runSender exits

	cancelMu     sync.Mutex
	cancelByReq  map[string]context.CancelFunc
	watchersMu   sync.Mutex
	watchersByPr map[string]context.CancelFunc

	terminalPumps     *terminalPumpTracker
	processOutputSubs *processOutputSubTracker
}

type StartOptions struct {
	BootstrapConfig bootstrap.DaemonBootstrapConfig
}

func Start(ctx context.Context, opts StartOptions) error {
	if err := opts.BootstrapConfig.Validate(); err != nil {
		return fmt.Errorf("daemon runtime bootstrap config invalid: %w", err)
	}

	client, err := newDaemonClient(opts.BootstrapConfig)
	if err != nil {
		return err
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

	hostname, _ := os.Hostname()
	if strings.TrimSpace(hostname) == "" {
		hostname = "unknown-host"
	}

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
	// The daemon runtime IS the local machine, so give the executor a
	// LocalClient so filesystem tools (glob, view, etc.) can operate.
	localExec.SetDaemonClient(daemon.NewLocalClient())

	// Initialize the terminal manager so terminal.* daemon commands can
	// create and manage PTY sessions on the user's machine.
	SetTerminalManager(terminal.NewManager())
	SetMCPManager(mcpManager)

	return &daemonClient{
		daemonID:          uuid.New().String(),
		userID:            bootCfg.UserID,
		hostname:          hostname,
		platform:          runtime.GOOS,
		cwd:               cwd,
		bootCfg:           bootCfg,
		mcpManager:        mcpManager,
		localExecutor:     localExec,
		capabilities:      caps,
		cancelByReq:       make(map[string]context.CancelFunc),
		watchersByPr:      make(map[string]context.CancelFunc),
		terminalPumps:     newTerminalPumpTracker(),
		processOutputSubs: newProcessOutputSubTracker(),
	}, nil
}

// isFatalError returns true for errors that should not be retried (e.g. auth failures).
func isFatalError(err error) bool {
	if err == nil {
		return false
	}
	code := connect.CodeOf(err)
	switch code {
	case connect.CodeUnauthenticated, connect.CodePermissionDenied:
		return true
	case connect.CodeUnimplemented:
		// Server doesn't have the ConnectDaemon endpoint — wrong URL.
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
		err := d.runSession(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			if isFatalError(err) {
				logging.Error(logPrefix+" Fatal error — not reconnecting",
					"error", err,
					"code", connect.CodeOf(err).String(),
					"grpc_url", d.bootCfg.GRPCURL,
				)
				d.stopAllStreams()
				return fmt.Errorf("daemon connection failed (not retrying): %w", err)
			}
			logging.Warn(logPrefix+" Session ended; reconnecting",
				"error", err,
				"code", connect.CodeOf(err).String(),
				"delay", delay,
				"grpc_url", d.bootCfg.GRPCURL,
			)
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
	httpClient, baseURL, err := transport.NewDaemonHTTPClient(d.bootCfg)
	if err != nil {
		return fmt.Errorf("creating daemon HTTP client: %w", err)
	}

	logging.Info(logPrefix+" Connecting to gateway", "url", baseURL)

	client := reliantv1connect.NewToolsDaemonServiceClient(httpClient, baseURL, connect.WithGRPC())

	stream := client.ConnectDaemon(ctx)

	// --- Registration: send directly before starting the sender goroutine ---
	register := &reliantv1.DaemonMessage{
		Message: &reliantv1.DaemonMessage_Register{Register: &reliantv1.DaemonRegister{
			DaemonId:     d.daemonID,
			UserId:       d.userID,
			Hostname:     d.hostname,
			Platform:     d.platform,
			WorkingDir:   d.cwd,
			Capabilities: d.capabilities,
		}},
	}
	if err = stream.Send(register); err != nil {
		return fmt.Errorf("sending daemon registration to %s: %w", baseURL, err)
	}

	// --- Start the send channel + single writer goroutine ---
	d.sendCh = make(chan *reliantv1.DaemonMessage, 256)
	d.sendDone = make(chan struct{})
	go d.runSender(stream)
	defer func() {
		close(d.sendCh) // signal runSender to exit
		<-d.sendDone    // wait for it to drain
	}()

	if err := d.sendProjectDiscovery(); err != nil {
		logging.Warn(logPrefix+" Failed to send project discovery", "error", err)
	}

	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat()
	go d.runHeartbeats(heartbeatCtx)

	for {
		msg, err := stream.Receive()
		if err != nil {
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

	case *reliantv1.ServerMessage_Heartbeat:
		return nil

	case *reliantv1.ServerMessage_RegistrationAck:
		if m.RegistrationAck != nil {
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
		return d.sendLoadProjectConfigResponse(m.LoadProjectConfigs.ProjectPath, m.LoadProjectConfigs.RequestId)

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

	resultPayload, err := defaultRegistry.Handle(ctx, req.CommandType, req.Payload)

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

	// After a successful terminal.create, start the output pump so PTY
	// output is streamed back to the server over the bidi stream.
	if err == nil && req.CommandType == "terminal.create" {
		var created struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(resultPayload, &created) == nil && created.SessionID != "" {
			d.startTerminalOutputPump(created.SessionID)
		}
	}
}

func (d *daemonClient) runHeartbeats(ctx context.Context) {
	ticker := time.NewTicker(transport.DaemonHeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			err := d.send(&reliantv1.DaemonMessage{
				Message: &reliantv1.DaemonMessage_Heartbeat{Heartbeat: &reliantv1.DaemonHeartbeat{Timestamp: now.UnixMilli()}},
			})
			if err != nil {
				logging.Warn(logPrefix+" Failed to send heartbeat", "error", err)
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
	d.registerCancel(req.RequestId, cancel)
	defer d.unregisterCancel(req.RequestId)
	defer cancel()

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
// It returns an error only if the stream is shutting down.
func (d *daemonClient) send(msg *reliantv1.DaemonMessage) error {
	select {
	case d.sendCh <- msg:
		return nil
	case <-d.sendDone:
		return fmt.Errorf("daemon send channel closed")
	}
}

// runSender is the single goroutine that drains sendCh and writes to the
// bidi stream. This serialises writes (required — stream.Send is not
// thread-safe) without blocking producers on I/O.
func (d *daemonClient) runSender(stream *connect.BidiStreamForClient[reliantv1.DaemonMessage, reliantv1.ServerMessage]) {
	defer close(d.sendDone)
	for msg := range d.sendCh {
		if err := stream.Send(msg); err != nil {
			logging.Warn(logPrefix+" runSender: stream.Send failed", "error", err)
			return
		}
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
	st, err := os.Stat(filepath.Join(projectPath, ".git"))
	if err != nil {
		return false
	}
	return st.IsDir() || !st.IsDir()
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
}

func (d *daemonClient) stopProjectWatcher(projectPath string) {
	projectPath = normalizePath(projectPath)
	d.watchersMu.Lock()
	defer d.watchersMu.Unlock()
	if cancel := d.watchersByPr[projectPath]; cancel != nil {
		cancel()
	}
	delete(d.watchersByPr, projectPath)
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
	d.terminalPumps.stopAll()
	d.processOutputSubs.stopAll()
	if terminalManager != nil {
		terminalManager.Cleanup()
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

	workflows, workflowBytes := indexWorkflows(projectPath)
	presets, presetBytes := indexPresets(projectPath)
	scenarios, scenarioBytes := indexScenarios(projectPath)

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
		GlobalMemoryMd:  globalMemory,
		ProjectMemoryMd: projectMemory,
	}, nil
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
