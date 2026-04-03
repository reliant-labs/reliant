// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/nats-io/nats.go"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/observability"
)

// NATSToolBridge subscribes to per-daemon NATS subjects when a daemon connects
// and unsubscribes when it disconnects. It forwards messages to the local
// DaemonConnectionManager (ToolsDaemonService) that holds daemon gRPC streams.
// This runs on the daemon-gateway in distributed mode.
type NATSToolBridge struct {
	nc  *nats.Conn
	mgr DaemonConnectionManager // The local ToolsDaemonService

	// Per-daemon NATS subscriptions, keyed by daemonID.
	daemonSubs map[string][]*nats.Subscription
	subsMu     sync.Mutex

	// Per-daemon cancel functions for output-forwarder goroutines, keyed by daemonID.
	daemonCancels map[string]context.CancelFunc
	cancelsMu     sync.Mutex

	// ctx and cancel control the lifetime of the entire bridge.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewNATSToolBridge creates a new bridge.
func NewNATSToolBridge(nc *nats.Conn, mgr DaemonConnectionManager) *NATSToolBridge {
	ctx, cancel := context.WithCancel(context.Background())
	return &NATSToolBridge{
		nc:            nc,
		mgr:           mgr,
		daemonSubs:    make(map[string][]*nats.Subscription),
		daemonCancels: make(map[string]context.CancelFunc),
		ctx:           ctx,
		cancel:        cancel,
	}
}

// Start logs startup. Subscriptions are created lazily via OnDaemonConnected.
func (b *NATSToolBridge) Start() error {
	logging.Info("[NATSToolBridge] Started — subscriptions will be created per-user on daemon connect")
	return nil
}

// OnDaemonConnected implements DaemonConnectionListener. It creates per-daemon
// NATS subscriptions for all 11 subjects that the bridge handles.
func (b *NATSToolBridge) OnDaemonConnected(userID, daemonID string) {
	// First, tear down any leftover subscriptions (e.g. reconnecting daemon).
	b.OnDaemonDisconnected(userID, daemonID)

	// Create a per-daemon context for output forwarder goroutines.
	daemonCtx, daemonCancel := context.WithCancel(b.ctx)
	b.cancelsMu.Lock()
	b.daemonCancels[daemonID] = daemonCancel
	b.cancelsMu.Unlock()

	var subs []*nats.Subscription

	// Helper to collect subscriptions and log errors.
	addSub := func(sub *nats.Subscription, err error) {
		if err != nil {
			logging.Error("[NATSToolBridge] Failed to subscribe", "userID", userID, "error", err)
			return
		}
		subs = append(subs, sub)
	}

	// -----------------------------------------------------------------------
	// Fire-and-forget subjects (plain Subscribe — only this pod has the connection)
	// -----------------------------------------------------------------------

	// 1. tools.request.{userID}
	addSub(b.nc.Subscribe(toolRequestSubject+"."+userID, func(msg *nats.Msg) {
		ctx, span := observability.StartNATSSpan(context.Background(), msg, "nats.handle.tools.request")
		defer span.End()
		observability.NATSReceiveTotal.WithLabelValues("tools.request").Inc()

		var request ToolExecutionRequest
		if err := json.Unmarshal(msg.Data, &request); err != nil {
			logging.Error("[NATSToolBridge] Failed to unmarshal tool request", "error", err)
			return
		}

		if err := b.mgr.SendToolRequest(ctx, userID, &request); err != nil {
			observability.ToolExecutionErrorsTotal.WithLabelValues("forward_request").Inc()
			logging.Warn("[NATSToolBridge] Failed to forward tool request", "error", err, "userID", userID)
		}
	}))

	// 2. tools.cancel.{userID}
	addSub(b.nc.Subscribe(toolCancelSubject+"."+userID, func(msg *nats.Msg) {
		ctx, span := observability.StartNATSSpan(context.Background(), msg, "nats.handle.tools.cancel")
		defer span.End()
		observability.NATSReceiveTotal.WithLabelValues("tools.cancel").Inc()

		var cancel struct {
			RequestID string `json:"request_id"`
			Reason    string `json:"reason"`
		}
		if err := json.Unmarshal(msg.Data, &cancel); err != nil {
			logging.Error("[NATSToolBridge] Failed to unmarshal cancel request", "error", err)
			return
		}

		if err := b.mgr.SendToolExecutionCancel(ctx, userID, cancel.RequestID, cancel.Reason); err != nil {
			observability.ToolExecutionErrorsTotal.WithLabelValues("forward_cancel").Inc()
			logging.Warn("[NATSToolBridge] Failed to forward cancel", "error", err, "userID", userID, "requestID", cancel.RequestID)
		}
	}))

	// 3. daemon.config.load.{userID}
	addSub(b.nc.Subscribe(configLoadSubject+"."+userID, func(msg *nats.Msg) {
		ctx, span := observability.StartNATSSpan(context.Background(), msg, "nats.handle.daemon.config.load")
		defer span.End()
		observability.NATSReceiveTotal.WithLabelValues("daemon.config.load").Inc()

		var req struct {
			ProjectPath string `json:"project_path"`
			RequestID   string `json:"request_id"`
		}
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			logging.Error("[NATSToolBridge] Failed to unmarshal config load request", "error", err)
			return
		}

		if err := b.mgr.SendLoadProjectConfigs(ctx, userID, req.ProjectPath, req.RequestID); err != nil {
			observability.ToolExecutionErrorsTotal.WithLabelValues("forward_config_load").Inc()
			logging.Warn("[NATSToolBridge] Failed to forward config load", "error", err, "userID", userID)
		}
	}))

	// 4. daemon.config.watch.{userID}
	addSub(b.nc.Subscribe(configWatchSubject+"."+userID, func(msg *nats.Msg) {
		ctx, span := observability.StartNATSSpan(context.Background(), msg, "nats.handle.daemon.config.watch")
		defer span.End()
		observability.NATSReceiveTotal.WithLabelValues("daemon.config.watch").Inc()

		var req struct {
			ProjectPath    string `json:"project_path"`
			IncludeInitial bool   `json:"include_initial"`
		}
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			logging.Error("[NATSToolBridge] Failed to unmarshal config watch request", "error", err)
			return
		}

		if err := b.mgr.SendWatchProjectConfigs(ctx, userID, req.ProjectPath, req.IncludeInitial); err != nil {
			observability.ToolExecutionErrorsTotal.WithLabelValues("forward_config_watch").Inc()
			logging.Warn("[NATSToolBridge] Failed to forward config watch", "error", err, "userID", userID)
		}
	}))

	// 5. daemon.terminal.input.{userID}.> (wildcard on sessionID)
	addSub(b.nc.Subscribe(terminalInputSubject+"."+userID+".>", func(msg *nats.Msg) {
		_, span := observability.StartNATSSpan(context.Background(), msg, "nats.handle.daemon.terminal.input")
		defer span.End()
		observability.NATSReceiveTotal.WithLabelValues("daemon.terminal.input").Inc()

		// Subject: daemon.terminal.input.{userID}.{sessionID}
		parts := strings.SplitN(msg.Subject, ".", 5)
		if len(parts) < 5 {
			logging.Warn("[NATSToolBridge] Invalid terminal input subject", "subject", msg.Subject)
			return
		}
		sessionID := parts[4]

		var req struct {
			Data []byte `json:"data"`
		}
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			logging.Error("[NATSToolBridge] Failed to unmarshal terminal input", "error", err)
			return
		}

		if err := b.mgr.SendTerminalInput(userID, sessionID, req.Data); err != nil {
			logging.Warn("[NATSToolBridge] Failed to forward terminal input", "error", err, "userID", userID, "sessionID", sessionID)
		}
	}))

	// 6. daemon.terminal.resize.{userID}.> (wildcard on sessionID)
	addSub(b.nc.Subscribe(terminalResizeSubject+"."+userID+".>", func(msg *nats.Msg) {
		_, span := observability.StartNATSSpan(context.Background(), msg, "nats.handle.daemon.terminal.resize")
		defer span.End()
		observability.NATSReceiveTotal.WithLabelValues("daemon.terminal.resize").Inc()

		// Subject: daemon.terminal.resize.{userID}.{sessionID}
		parts := strings.SplitN(msg.Subject, ".", 5)
		if len(parts) < 5 {
			logging.Warn("[NATSToolBridge] Invalid terminal resize subject", "subject", msg.Subject)
			return
		}
		sessionID := parts[4]

		var req struct {
			Cols uint32 `json:"cols"`
			Rows uint32 `json:"rows"`
		}
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			logging.Error("[NATSToolBridge] Failed to unmarshal terminal resize", "error", err)
			return
		}

		if err := b.mgr.SendTerminalResize(userID, sessionID, req.Cols, req.Rows); err != nil {
			logging.Warn("[NATSToolBridge] Failed to forward terminal resize", "error", err, "userID", userID, "sessionID", sessionID)
		}
	}))

	// 7. daemon.process.subscribe.{userID}
	addSub(b.nc.Subscribe(processOutputSubscribeSubject+"."+userID, func(msg *nats.Msg) {
		_, span := observability.StartNATSSpan(context.Background(), msg, "nats.handle.daemon.process.subscribe")
		defer span.End()
		observability.NATSReceiveTotal.WithLabelValues("daemon.process.subscribe").Inc()

		var req struct {
			ProcessID string `json:"process_id"`
			NewOnly   bool   `json:"new_only"`
		}
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			logging.Error("[NATSToolBridge] Failed to unmarshal process subscribe request", "error", err)
			return
		}

		b.startProcessOutputForwarder(daemonCtx, userID, req.ProcessID, req.NewOnly)
	}))

	// -----------------------------------------------------------------------
	// Request-reply subjects (plain Subscribe — only this pod responds)
	// -----------------------------------------------------------------------

	// 8. tools.online.{userID}
	addSub(b.nc.Subscribe(toolOnlineSubject+"."+userID, func(msg *nats.Msg) {
		_, span := observability.StartNATSSpan(context.Background(), msg, "nats.handle.tools.online")
		defer span.End()
		observability.NATSReceiveTotal.WithLabelValues("tools.online").Inc()

		_ = msg.Respond([]byte("true"))
	}))

	// 9. daemon.process.kill.{userID}
	addSub(b.nc.Subscribe(daemonKillSubject+"."+userID, func(msg *nats.Msg) {
		_, span := observability.StartNATSSpan(context.Background(), msg, "nats.handle.daemon.process.kill")
		defer span.End()
		observability.NATSReceiveTotal.WithLabelValues("daemon.process.kill").Inc()

		var req struct {
			ProcessID string `json:"process_id"`
		}
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			_ = msg.Respond([]byte(`{"error":"invalid payload"}`))
			return
		}

		if err := b.mgr.SendKillProcess(userID, req.ProcessID); err != nil {
			resp, _ := json.Marshal(map[string]string{"error": err.Error()})
			_ = msg.Respond(resp)
			return
		}
		_ = msg.Respond([]byte(`{"ok":true}`))
	}))

	// 10. daemon.command.{userID}
	addSub(b.nc.Subscribe(daemonCommandSubject+"."+userID, func(msg *nats.Msg) {
		ctx, span := observability.StartNATSSpan(context.Background(), msg, "nats.handle.daemon.command")
		defer span.End()
		observability.NATSReceiveTotal.WithLabelValues("daemon.command").Inc()

		var req struct {
			RequestID   string          `json:"request_id"`
			CommandType string          `json:"command_type"`
			Payload     json.RawMessage `json:"payload"`
			TimeoutMs   int32           `json:"timeout_ms"`
		}
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			_ = msg.Respond([]byte(`{"success":false,"error_message":"invalid payload"}`))
			return
		}

		protoReq := &reliantv1.DaemonCommandRequest{
			RequestId:   req.RequestID,
			CommandType: req.CommandType,
			Payload:     req.Payload,
			TimeoutMs:   req.TimeoutMs,
		}

		resp, err := b.mgr.SendDaemonCommand(ctx, userID, protoReq)
		if err != nil {
			errResp, _ := json.Marshal(map[string]interface{}{
				"success":       false,
				"error_message": err.Error(),
			})
			_ = msg.Respond(errResp)
			return
		}

		respData, _ := json.Marshal(map[string]interface{}{
			"success":       resp.Success,
			"payload":       resp.Payload,
			"error_message": resp.ErrorMessage,
		})
		_ = msg.Respond(respData)

		// If this was a terminal.create command that succeeded, start
		// forwarding terminal output from the local daemon to NATS so
		// remote subscribers (NATSDaemonRouter) receive it.
		if req.CommandType == "terminal.create" && resp.Success {
			b.startTerminalOutputForwarder(daemonCtx, userID, resp.Payload)
		}
	}))

	// 11. tools.request.sync.{userID}
	addSub(b.nc.Subscribe(toolRequestSyncSubject+"."+userID, func(msg *nats.Msg) {
		ctx, span := observability.StartNATSSpan(context.Background(), msg, "nats.handle.tools.request.sync")
		defer span.End()
		observability.NATSReceiveTotal.WithLabelValues("tools.request.sync").Inc()

		var request ToolExecutionRequest
		if err := json.Unmarshal(msg.Data, &request); err != nil {
			_ = msg.Respond([]byte(`{"success":false,"is_error":true,"error_message":"invalid payload","error_code":"INVALID_REQUEST"}`))
			return
		}

		resp, err := b.mgr.SendToolRequestSync(ctx, userID, &request)
		if err != nil {
			errResp, _ := json.Marshal(&ToolExecutionResponse{
				Success:      false,
				IsError:      true,
				ErrorMessage: err.Error(),
				ErrorCode:    "DAEMON_ERROR",
			})
			_ = msg.Respond(errResp)
			return
		}

		respData, _ := json.Marshal(resp)
		_ = msg.Respond(respData)
	}))

	// Store all subscriptions for this daemon.
	b.subsMu.Lock()
	b.daemonSubs[daemonID] = subs
	b.subsMu.Unlock()

	logging.Info("[NATSToolBridge] Subscribing to daemon subjects",
		"userID", userID, "daemonID", daemonID, "count", len(subs))
}

// OnDaemonDisconnected implements DaemonConnectionListener. It unsubscribes
// all per-daemon NATS subscriptions and stops output-forwarder goroutines.
func (b *NATSToolBridge) OnDaemonDisconnected(userID, daemonID string) {
	// Cancel per-daemon output forwarder goroutines.
	b.cancelsMu.Lock()
	if cancel, ok := b.daemonCancels[daemonID]; ok {
		cancel()
		delete(b.daemonCancels, daemonID)
	}
	b.cancelsMu.Unlock()

	// Unsubscribe all NATS subscriptions for this daemon.
	b.subsMu.Lock()
	subs := b.daemonSubs[daemonID]
	delete(b.daemonSubs, daemonID)
	b.subsMu.Unlock()

	if len(subs) > 0 {
		for _, sub := range subs {
			_ = sub.Unsubscribe()
		}
		logging.Info("[NATSToolBridge] Unsubscribed daemon subjects",
			"userID", userID, "daemonID", daemonID, "count", len(subs))
	}
}

// startTerminalOutputForwarder extracts the sessionID from a terminal.create
// response payload, subscribes to local terminal output, and publishes events
// to NATS on daemon.terminal.output.{userID}.{sessionID}.
func (b *NATSToolBridge) startTerminalOutputForwarder(userCtx context.Context, userID string, payload []byte) {
	var createResp struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(payload, &createResp); err != nil || createResp.SessionID == "" {
		logging.Warn("[NATSToolBridge] Could not extract session_id from terminal.create response",
			"error", err, "userID", userID)
		return
	}
	sessionID := createResp.SessionID

	outputCh, unsub, err := b.mgr.SubscribeTerminalOutput(userID, sessionID)
	if err != nil {
		logging.Warn("[NATSToolBridge] Failed to subscribe to terminal output for forwarding",
			"error", err, "userID", userID, "sessionID", sessionID)
		return
	}

	subject := terminalOutputSubject + "." + userID + "." + sessionID
	logging.Info("[NATSToolBridge] Starting terminal output forwarder",
		"userID", userID, "sessionID", sessionID, "subject", subject)

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer unsub()
		for {
			select {
			case <-userCtx.Done():
				return
			case evt, ok := <-outputCh:
				if !ok {
					// Channel closed — daemon disconnected or session ended.
					return
				}
				data, err := json.Marshal(evt)
				if err != nil {
					logging.Error("[NATSToolBridge] Failed to marshal terminal output event", "error", err)
					continue
				}
				if err := b.nc.Publish(subject, data); err != nil {
					logging.Warn("[NATSToolBridge] Failed to publish terminal output to NATS",
						"error", err, "subject", subject)
					return
				}
				observability.NATSPublishTotal.WithLabelValues("daemon.terminal.output").Inc()
			}
		}
	}()
}

// startProcessOutputForwarder subscribes to local process output and publishes
// events to NATS on daemon.process.output.{userID}.{processID}.
func (b *NATSToolBridge) startProcessOutputForwarder(userCtx context.Context, userID, processID string, newOnly bool) {
	outputCh, unsub, err := b.mgr.SubscribeProcessOutput(userID, processID, newOnly)
	if err != nil {
		logging.Warn("[NATSToolBridge] Failed to subscribe to process output for forwarding",
			"error", err, "userID", userID, "processID", processID)
		return
	}

	subject := processOutputSubject + "." + userID + "." + processID
	logging.Info("[NATSToolBridge] Starting process output forwarder",
		"userID", userID, "processID", processID, "subject", subject)

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer unsub()
		for {
			select {
			case <-userCtx.Done():
				return
			case evt, ok := <-outputCh:
				if !ok {
					// Channel closed — daemon disconnected or process ended.
					return
				}
				data, err := json.Marshal(evt)
				if err != nil {
					logging.Error("[NATSToolBridge] Failed to marshal process output event", "error", err)
					continue
				}
				if err := b.nc.Publish(subject, data); err != nil {
					logging.Warn("[NATSToolBridge] Failed to publish process output to NATS",
						"error", err, "subject", subject)
					return
				}
				observability.NATSPublishTotal.WithLabelValues("daemon.process.output").Inc()
			}
		}
	}()
}

// Close cancels the bridge context, waits for goroutines, and unsubscribes everything.
func (b *NATSToolBridge) Close() error {
	// Cancel all goroutines (bridge-level context).
	b.cancel()
	b.wg.Wait()

	// Unsubscribe all remaining per-daemon subscriptions.
	b.subsMu.Lock()
	for daemonID, subs := range b.daemonSubs {
		for _, sub := range subs {
			_ = sub.Unsubscribe()
		}
		delete(b.daemonSubs, daemonID)
	}
	b.subsMu.Unlock()

	return nil
}
