// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/nats-io/nats.go"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
)

// NATSToolBridge subscribes to NATS tool/daemon request notifications and forwards
// them to the local DaemonConnectionManager (ToolsDaemonService) that holds daemon
// gRPC streams. This runs on the api-server in distributed mode.
type NATSToolBridge struct {
	nc   *nats.Conn
	mgr  DaemonConnectionManager // The local ToolsDaemonService
	subs []*nats.Subscription
}

// NewNATSToolBridge creates a new bridge.
func NewNATSToolBridge(nc *nats.Conn, mgr DaemonConnectionManager) *NATSToolBridge {
	return &NATSToolBridge{
		nc:  nc,
		mgr: mgr,
	}
}

// daemonBridgeQueue is the NATS queue group used by NATSToolBridge for
// fire-and-forget subscriptions so only one instance processes each message.
const daemonBridgeQueue = "daemon-bridge"

// Start subscribes to NATS subjects and begins forwarding.
func (b *NATSToolBridge) Start() error {
	// --- Fire-and-forget subjects: use QueueSubscribe so only one instance processes each message ---

	// Subscribe to tool request notifications: tools.request.>
	reqSub, err := b.nc.QueueSubscribe(toolRequestSubject+".>", daemonBridgeQueue, func(msg *nats.Msg) {
		// Extract userID from subject: tools.request.{userID}
		parts := strings.SplitN(msg.Subject, ".", 3)
		if len(parts) < 3 {
			logging.Warn("[NATSToolBridge] Invalid tool request subject", "subject", msg.Subject)
			return
		}
		userID := parts[2]

		var request ToolExecutionRequest
		if err := json.Unmarshal(msg.Data, &request); err != nil {
			logging.Error("[NATSToolBridge] Failed to unmarshal tool request", "error", err)
			return
		}

		// Forward to local daemon connection
		if err := b.mgr.SendToolRequest(context.Background(), userID, &request); err != nil {
			logging.Warn("[NATSToolBridge] Failed to forward tool request", "error", err, "userID", userID)
		}
	})
	if err != nil {
		return err
	}
	b.subs = append(b.subs, reqSub)

	// Subscribe to cancel notifications: tools.cancel.>
	cancelSub, err := b.nc.QueueSubscribe(toolCancelSubject+".>", daemonBridgeQueue, func(msg *nats.Msg) {
		parts := strings.SplitN(msg.Subject, ".", 3)
		if len(parts) < 3 {
			logging.Warn("[NATSToolBridge] Invalid cancel subject", "subject", msg.Subject)
			return
		}
		userID := parts[2]

		var cancel struct {
			RequestID string `json:"request_id"`
			Reason    string `json:"reason"`
		}
		if err := json.Unmarshal(msg.Data, &cancel); err != nil {
			logging.Error("[NATSToolBridge] Failed to unmarshal cancel request", "error", err)
			return
		}

		if err := b.mgr.SendToolExecutionCancel(userID, cancel.RequestID, cancel.Reason); err != nil {
			logging.Warn("[NATSToolBridge] Failed to forward cancel", "error", err, "userID", userID, "requestID", cancel.RequestID)
		}
	})
	if err != nil {
		return err
	}
	b.subs = append(b.subs, cancelSub)

	// Subscribe to config load notifications: daemon.config.load.>
	configLoadSub, err := b.nc.QueueSubscribe(configLoadSubject+".>", daemonBridgeQueue, func(msg *nats.Msg) {
		parts := strings.SplitN(msg.Subject, ".", 4)
		if len(parts) < 4 {
			logging.Warn("[NATSToolBridge] Invalid config load subject", "subject", msg.Subject)
			return
		}
		userID := parts[3]

		var req struct {
			ProjectPath string `json:"project_path"`
			RequestID   string `json:"request_id"`
		}
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			logging.Error("[NATSToolBridge] Failed to unmarshal config load request", "error", err)
			return
		}

		if err := b.mgr.SendLoadProjectConfigs(userID, req.ProjectPath, req.RequestID); err != nil {
			logging.Warn("[NATSToolBridge] Failed to forward config load", "error", err, "userID", userID)
		}
	})
	if err != nil {
		return err
	}
	b.subs = append(b.subs, configLoadSub)

	// Subscribe to config watch notifications: daemon.config.watch.>
	configWatchSub, err := b.nc.QueueSubscribe(configWatchSubject+".>", daemonBridgeQueue, func(msg *nats.Msg) {
		parts := strings.SplitN(msg.Subject, ".", 4)
		if len(parts) < 4 {
			logging.Warn("[NATSToolBridge] Invalid config watch subject", "subject", msg.Subject)
			return
		}
		userID := parts[3]

		var req struct {
			ProjectPath    string `json:"project_path"`
			IncludeInitial bool   `json:"include_initial"`
		}
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			logging.Error("[NATSToolBridge] Failed to unmarshal config watch request", "error", err)
			return
		}

		if err := b.mgr.SendWatchProjectConfigs(userID, req.ProjectPath, req.IncludeInitial); err != nil {
			logging.Warn("[NATSToolBridge] Failed to forward config watch", "error", err, "userID", userID)
		}
	})
	if err != nil {
		return err
	}
	b.subs = append(b.subs, configWatchSub)

	// --- Request-reply subjects: use regular Subscribe (all instances receive) ---
	// Only the instance that holds the daemon's gRPC connection responds.
	// Instances without the daemon return early without calling msg.Respond(),
	// letting the requester's NATS request timeout handle the "not found" case.

	// Subscribe to online checks: tools.online.> (request-reply)
	onlineSub, err := b.nc.Subscribe(toolOnlineSubject+".>", func(msg *nats.Msg) {
		parts := strings.SplitN(msg.Subject, ".", 3)
		if len(parts) < 3 {
			return
		}
		userID := parts[2]

		if !b.mgr.IsDaemonOnline(userID) {
			// Daemon not connected locally — don't respond; let the
			// instance that holds the connection answer instead.
			return
		}
		_ = msg.Respond([]byte("true"))
	})
	if err != nil {
		return err
	}
	b.subs = append(b.subs, onlineSub)

	// Subscribe to kill process requests: daemon.process.kill.> (request-reply)
	killSub, err := b.nc.Subscribe(daemonKillSubject+".>", func(msg *nats.Msg) {
		parts := strings.SplitN(msg.Subject, ".", 4)
		if len(parts) < 4 {
			logging.Warn("[NATSToolBridge] Invalid kill subject", "subject", msg.Subject)
			return
		}
		userID := parts[3]

		// Only handle if the daemon is connected locally.
		if !b.mgr.IsDaemonOnline(userID) {
			return
		}

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
	})
	if err != nil {
		return err
	}
	b.subs = append(b.subs, killSub)

	// Subscribe to daemon command requests: daemon.command.> (request-reply)
	cmdSub, err := b.nc.Subscribe(daemonCommandSubject+".>", func(msg *nats.Msg) {
		parts := strings.SplitN(msg.Subject, ".", 3)
		if len(parts) < 3 {
			logging.Warn("[NATSToolBridge] Invalid daemon command subject", "subject", msg.Subject)
			return
		}
		userID := parts[2]

		// Only handle if the daemon is connected locally.
		if !b.mgr.IsDaemonOnline(userID) {
			return
		}

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

		resp, err := b.mgr.SendDaemonCommand(context.Background(), userID, protoReq)
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
	})
	if err != nil {
		return err
	}
	b.subs = append(b.subs, cmdSub)

	// Subscribe to synchronous tool execution requests: tools.request.sync.> (request-reply)
	toolReqSyncSub, err := b.nc.Subscribe(toolRequestSyncSubject+".>", func(msg *nats.Msg) {
		parts := strings.SplitN(msg.Subject, ".", 4)
		if len(parts) < 4 {
			logging.Warn("[NATSToolBridge] Invalid sync tool request subject", "subject", msg.Subject)
			return
		}
		userID := parts[3]

		// Only handle if the daemon is connected locally.
		if !b.mgr.IsDaemonOnline(userID) {
			return
		}

		var request ToolExecutionRequest
		if err := json.Unmarshal(msg.Data, &request); err != nil {
			_ = msg.Respond([]byte(`{"success":false,"is_error":true,"error_message":"invalid payload","error_code":"INVALID_REQUEST"}`))
			return
		}

		resp, err := b.mgr.SendToolRequestSync(context.Background(), userID, &request)
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
	})
	if err != nil {
		return err
	}
	b.subs = append(b.subs, toolReqSyncSub)

	logging.Info("[NATSToolBridge] Started — listening for tool and daemon request notifications")
	return nil
}

// Close unsubscribes from all NATS subjects.
func (b *NATSToolBridge) Close() error {
	for _, sub := range b.subs {
		_ = sub.Unsubscribe()
	}
	return nil
}
