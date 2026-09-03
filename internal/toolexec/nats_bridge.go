// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/observability"
)

// NATSToolBridge subscribes to per-daemon NATS subjects when a daemon connects
// and unsubscribes when it disconnects. It forwards messages to the local
// DaemonConnectionManager (ToolsDaemonService) that holds daemon gRPC streams.
// This runs on the daemon-gateway in distributed mode.
type NATSToolBridge struct {
	nc  *nats.Conn
	js  jetstream.JetStream
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

	// inFlight bounds the number of concurrently-handled request/reply
	// messages. See handleAsync.
	inFlight chan struct{}
}

// maxInFlightRequests bounds concurrent request/reply handlers across all
// daemons on this pod. Each slot costs one goroutine parked on a channel
// receive, so this is generous — it exists to stop a runaway agent loop from
// spawning unbounded goroutines, not to pace normal traffic.
const maxInFlightRequests = 512

// NewNATSToolBridge creates a new bridge.
func NewNATSToolBridge(nc *nats.Conn, js jetstream.JetStream, mgr DaemonConnectionManager) *NATSToolBridge {
	ctx, cancel := context.WithCancel(context.Background())
	return &NATSToolBridge{
		nc:            nc,
		js:            js,
		mgr:           mgr,
		daemonSubs:    make(map[string][]*nats.Subscription),
		daemonCancels: make(map[string]context.CancelFunc),
		ctx:           ctx,
		cancel:        cancel,
		inFlight:      make(chan struct{}, maxInFlightRequests),
	}
}

// Start logs startup. Subscriptions are created lazily via OnDaemonConnected.
func (b *NATSToolBridge) Start() error {
	logging.Info("[NATSToolBridge] Started — subscriptions will be created per-daemon on connect")
	return nil
}

// handleAsync runs fn on its own goroutine so the NATS subscription callback
// can return immediately.
//
// nats.go delivers messages for a subscription to its callback ONE AT A TIME,
// in order. Any request/reply handler that waits on the daemon round-trip
// inline therefore blocks every other message on the same subject for the full
// duration of that tool call — and the subject is per-{user,daemon}, so a
// single `go test` stalled every other chat sharing the daemon behind it. A
// 3ms `sed` was observed taking 209s, completing 12ms after the slow neighbour
// ahead of it released the callback.
//
// The reply is published from the goroutine; msg.Respond is just a publish to
// msg.Reply, so it is safe off-callback.
//
// If the in-flight budget is exhausted, fail the request immediately rather
// than blocking the callback — blocking here would reintroduce exactly the
// head-of-line stall this exists to prevent. onOverload should send whatever
// error shape the caller's subject expects.
func (b *NATSToolBridge) handleAsync(subject string, msg *nats.Msg, onOverload func(), fn func()) {
	select {
	case b.inFlight <- struct{}{}:
	default:
		logging.Error("[NATSToolBridge] In-flight request budget exhausted, rejecting",
			"subject", subject, "limit", maxInFlightRequests)
		onOverload()
		return
	}

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer func() { <-b.inFlight }()
		defer func() {
			// A panic here would otherwise take down the whole gateway
			// process, since this no longer runs on nats.go's callback
			// goroutine. The caller is left to time out.
			if r := recover(); r != nil {
				logging.Error("[NATSToolBridge] Panic in async handler",
					"subject", subject, "panic", r)
			}
		}()
		fn()
	}()
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

	// 1. tools.request.{userID}.{daemonID}
	addSub(b.nc.Subscribe(daemonSubject(toolRequestSubject, userID, daemonID), func(msg *nats.Msg) {
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

	// 2. tools.cancel.{userID}.{daemonID}
	addSub(b.nc.Subscribe(daemonSubject(toolCancelSubject, userID, daemonID), func(msg *nats.Msg) {
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

	// 2b. tools.background.{userID}.{daemonID}
	addSub(b.nc.Subscribe(daemonSubject(toolBackgroundSubj, userID, daemonID), func(msg *nats.Msg) {
		ctx, span := observability.StartNATSSpan(context.Background(), msg, "nats.handle.tools.background")
		defer span.End()
		observability.NATSReceiveTotal.WithLabelValues("tools.background").Inc()

		var bg struct {
			RequestID  string `json:"request_id"`
			ToolCallID string `json:"tool_call_id"`
		}
		if err := json.Unmarshal(msg.Data, &bg); err != nil {
			logging.Error("[NATSToolBridge] Failed to unmarshal background request", "error", err)
			return
		}

		if err := b.mgr.SendToolExecutionBackground(ctx, userID, bg.RequestID, bg.ToolCallID); err != nil {
			observability.ToolExecutionErrorsTotal.WithLabelValues("forward_background").Inc()
			logging.Warn("[NATSToolBridge] Failed to forward background", "error", err, "userID", userID, "requestID", bg.RequestID)
		}
	}))

	// 3. daemon.config.load.{userID}.{daemonID}
	addSub(b.nc.Subscribe(daemonSubject(configLoadSubject, userID, daemonID), func(msg *nats.Msg) {
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

	// 4. daemon.config.watch.{userID}.{daemonID}
	addSub(b.nc.Subscribe(daemonSubject(configWatchSubject, userID, daemonID), func(msg *nats.Msg) {
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

	// 5. daemon.terminal.input.{userID}.{daemonID}.> (wildcard on sessionID)
	addSub(b.nc.Subscribe(daemonSubject(terminalInputSubject, userID, daemonID)+".>", func(msg *nats.Msg) {
		_, span := observability.StartNATSSpan(context.Background(), msg, "nats.handle.daemon.terminal.input")
		defer span.End()
		observability.NATSReceiveTotal.WithLabelValues("daemon.terminal.input").Inc()

		// Subject: daemon.terminal.input.{userID}.{daemonID}.{sessionID}
		parts := strings.SplitN(msg.Subject, ".", 6)
		if len(parts) < 6 {
			logging.Warn("[NATSToolBridge] Invalid terminal input subject", "subject", msg.Subject)
			return
		}
		sessionID := parts[5]

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

	// 6. daemon.terminal.resize.{userID}.{daemonID}.> (wildcard on sessionID)
	addSub(b.nc.Subscribe(daemonSubject(terminalResizeSubject, userID, daemonID)+".>", func(msg *nats.Msg) {
		_, span := observability.StartNATSSpan(context.Background(), msg, "nats.handle.daemon.terminal.resize")
		defer span.End()
		observability.NATSReceiveTotal.WithLabelValues("daemon.terminal.resize").Inc()

		// Subject: daemon.terminal.resize.{userID}.{daemonID}.{sessionID}
		parts := strings.SplitN(msg.Subject, ".", 6)
		if len(parts) < 6 {
			logging.Warn("[NATSToolBridge] Invalid terminal resize subject", "subject", msg.Subject)
			return
		}
		sessionID := parts[5]

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

	// 7. daemon.process.subscribe.{userID}.{daemonID}
	addSub(b.nc.Subscribe(daemonSubject(processOutputSubscribeSubject, userID, daemonID), func(msg *nats.Msg) {
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

	// 7b. daemon.terminal.subscribe.{userID}.{daemonID}.{sessionID}
	// A terminal-output-subscribe request from a remote subscriber
	// (NATSDaemonRouter). This starts the terminal output forwarder, which in
	// turn sends the terminal-output-subscribe message down to the daemon and
	// starts its PTY pump. Mirrors the process-output subscribe handler above.
	addSub(b.nc.Subscribe(daemonSubject(terminalSubscribeSubject, userID, daemonID)+".*", func(msg *nats.Msg) {
		_, span := observability.StartNATSSpan(context.Background(), msg, "nats.handle.daemon.terminal.subscribe")
		defer span.End()
		observability.NATSReceiveTotal.WithLabelValues("daemon.terminal.subscribe").Inc()

		// Subject: daemon.terminal.subscribe.{userID}.{daemonID}.{sessionID}
		parts := strings.SplitN(msg.Subject, ".", 6)
		if len(parts) < 6 {
			logging.Warn("[NATSToolBridge] Invalid terminal subscribe subject", "subject", msg.Subject)
			return
		}
		sessionID := parts[5]

		b.startTerminalOutputForwarder(daemonCtx, userID, daemonID, sessionID)
	}))

	// -----------------------------------------------------------------------
	// Request-reply subjects (plain Subscribe — only this pod responds)
	// -----------------------------------------------------------------------

	// 8. tools.online.{userID}.{daemonID}
	addSub(b.nc.Subscribe(daemonSubject(toolOnlineSubject, userID, daemonID), func(msg *nats.Msg) {
		_, span := observability.StartNATSSpan(context.Background(), msg, "nats.handle.tools.online")
		defer span.End()
		observability.NATSReceiveTotal.WithLabelValues("tools.online").Inc()

		_ = msg.Respond([]byte("true"))
	}))

	// 9. daemon.process.kill.{userID}.{daemonID}
	addSub(b.nc.Subscribe(daemonSubject(daemonKillSubject, userID, daemonID), func(msg *nats.Msg) {
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

	// 10. daemon.command.{userID}.{daemonID}
	addSub(b.nc.Subscribe(daemonSubject(daemonCommandSubject, userID, daemonID), func(msg *nats.Msg) {
		ctx, span := observability.StartNATSSpan(context.Background(), msg, "nats.handle.daemon.command")
		observability.NATSReceiveTotal.WithLabelValues("daemon.command").Inc()

		var req daemonCommandWire
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			span.End()
			_ = msg.Respond([]byte(`{"success":false,"error_message":"invalid payload"}`))
			return
		}

		// Off the callback goroutine: SendDaemonCommand blocks on the daemon
		// round-trip. See handleAsync.
		b.handleAsync("daemon.command", msg, func() {
			span.End()
			_ = msg.Respond([]byte(`{"success":false,"error_message":"daemon gateway overloaded: in-flight request budget exhausted"}`))
		}, func() {
			defer span.End()
			b.respondDaemonCommand(ctx, msg, userID, &req)
		})
	}))

	// 11. tools.request.sync.{userID}.{daemonID}
	addSub(b.nc.Subscribe(daemonSubject(toolRequestSyncSubject, userID, daemonID), func(msg *nats.Msg) {
		ctx, span := observability.StartNATSSpan(context.Background(), msg, "nats.handle.tools.request.sync")
		observability.NATSReceiveTotal.WithLabelValues("tools.request.sync").Inc()

		var request ToolExecutionRequest
		if err := json.Unmarshal(msg.Data, &request); err != nil {
			span.End()
			_ = msg.Respond([]byte(`{"success":false,"is_error":true,"error_message":"invalid payload","error_code":"INVALID_REQUEST"}`))
			return
		}

		// Off the callback goroutine: SendToolRequestSync blocks for the whole
		// tool execution (up to 600s). See handleAsync.
		b.handleAsync("tools.request.sync", msg, func() {
			span.End()
			errResp, _ := json.Marshal(&ToolExecutionResponse{
				RequestID:    request.RequestID,
				Success:      false,
				IsError:      true,
				Content:      "Daemon gateway is overloaded; too many tool requests in flight.",
				ErrorMessage: "in-flight request budget exhausted",
				ErrorCode:    ErrorCodeDaemonRoundTrip,
			})
			_ = msg.Respond(errResp)
		}, func() {
			defer span.End()
			b.respondToolRequestSync(ctx, msg, userID, &request)
		})
	}))

	b.finishDaemonConnected(daemonCtx, userID, daemonID, subs)
}

// daemonCommandWire is the JSON envelope for a daemon.command NATS request.
type daemonCommandWire struct {
	RequestID   string                   `json:"request_id"`
	CommandType string                   `json:"command_type"`
	Payload     json.RawMessage          `json:"payload"`
	TimeoutMs   int32                    `json:"timeout_ms"`
	Policy      *daemonpolicy.WirePolicy `json:"policy,omitempty"`
}

// respondDaemonCommand performs the daemon round-trip for a daemon.command
// message and publishes the reply. Runs off the NATS callback goroutine.
func (b *NATSToolBridge) respondDaemonCommand(ctx context.Context, msg *nats.Msg, userID string, req *daemonCommandWire) {
	protoReq := &reliantv1.DaemonCommandRequest{
		RequestId:   req.RequestID,
		CommandType: req.CommandType,
		Payload:     req.Payload,
		TimeoutMs:   req.TimeoutMs,
		// Forwarded so the daemon can enforce connector confinement. Nil
		// for first-party traffic, which the daemon treats as unrestricted.
		Policy: daemonpolicy.WireToProto(req.Policy),
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
	// A reply exceeding the NATS server's max_payload can't transit as a
	// single message — and the old `_ = msg.Respond(...)` DISCARDED the
	// publish error, so the caller waited out its full timeout with zero
	// diagnostics (2026-07-09: worktree.git_changes / fs.search replies of
	// 1.7-5.4MB against the 1MB default surfaced as bare "nats: timeout").
	// publishReply keeps the single-message fast path for small replies
	// (byte-identical to Respond) and transparently chunks oversize ones;
	// the router reassembles, so user RPCs get the full payload. Only
	// beyond the absolute cap do we substitute a structured, actionable
	// error reply in the same envelope format.
	chunks, err := publishReply(b.nc, msg.Reply, respData)
	switch {
	case errors.Is(err, errReplyExceedsAbsoluteCap):
		logging.Error("[NATSToolBridge] Daemon command reply exceeds absolute reply cap",
			"commandType", req.CommandType, "requestID", req.RequestID,
			"replyBytes", len(respData), "cap", int64(maxChunkedReplyBytes))
		errResp, _ := json.Marshal(map[string]interface{}{
			"success":       false,
			"error_message": oversizeNATSPayloadError("response", len(respData), maxChunkedReplyBytes, oversizeReplyHint),
		})
		if rerr := msg.Respond(errResp); rerr != nil {
			logging.Error("[NATSToolBridge] Failed to publish oversize-reply error for daemon command",
				"commandType", req.CommandType, "requestID", req.RequestID, "error", rerr)
		}
		return
	case err != nil:
		logging.Error("[NATSToolBridge] Failed to publish daemon command reply",
			"commandType", req.CommandType, "requestID", req.RequestID,
			"replyBytes", len(respData), "error", err)
	case chunks > 1:
		logging.Info("[NATSToolBridge] Chunked oversize daemon command reply",
			"commandType", req.CommandType, "requestID", req.RequestID,
			"replyBytes", len(respData), "chunks", chunks, "maxPayload", b.nc.MaxPayload())
	}
}

// respondToolRequestSync performs the daemon round-trip for a tools.request.sync
// message and publishes the reply. Runs off the NATS callback goroutine.
func (b *NATSToolBridge) respondToolRequestSync(ctx context.Context, msg *nats.Msg, userID string, request *ToolExecutionRequest) {
	resp, err := b.mgr.SendToolRequestSync(ctx, userID, request)
	if err != nil {
		errResp, _ := json.Marshal(&ToolExecutionResponse{
			Success:      false,
			IsError:      true,
			ErrorMessage: err.Error(),
			ErrorCode:    ErrorCodeDaemonRoundTrip,
		})
		_ = msg.Respond(errResp)
		return
	}

	respData, _ := json.Marshal(resp)
	// Same transparent chunking as daemon.command — the transport doesn't
	// decide policy, it just moves the bytes; how much of an oversize tool
	// result an LLM actually sees is capped at the tool-result consumption
	// layer (RemoteExecutor). Only beyond the absolute cap do we substitute a
	// structured error, with Content set (not just ErrorMessage) because the
	// LLM sees the tool result's Content — that's what lets the model react by
	// narrowing its search.
	chunks, err := publishReply(b.nc, msg.Reply, respData)
	switch {
	case errors.Is(err, errReplyExceedsAbsoluteCap):
		logging.Error("[NATSToolBridge] Tool sync reply exceeds absolute reply cap",
			"toolName", request.ToolName, "requestID", request.RequestID,
			"replyBytes", len(respData), "cap", int64(maxChunkedReplyBytes))
		oversizeMsg := oversizeNATSPayloadError("tool result", len(respData), maxChunkedReplyBytes, oversizeReplyHint)
		errResp, _ := json.Marshal(&ToolExecutionResponse{
			RequestID:    request.RequestID,
			Success:      false,
			IsError:      true,
			Content:      oversizeMsg,
			ErrorMessage: oversizeMsg,
			ErrorCode:    "RESPONSE_TOO_LARGE",
		})
		if rerr := msg.Respond(errResp); rerr != nil {
			logging.Error("[NATSToolBridge] Failed to publish oversize-reply error for tool sync request",
				"toolName", request.ToolName, "requestID", request.RequestID, "error", rerr)
		}
		return
	case err != nil:
		logging.Error("[NATSToolBridge] Failed to publish tool sync reply",
			"toolName", request.ToolName, "requestID", request.RequestID,
			"replyBytes", len(respData), "error", err)
	case chunks > 1:
		logging.Info("[NATSToolBridge] Chunked oversize tool sync reply",
			"toolName", request.ToolName, "requestID", request.RequestID,
			"replyBytes", len(respData), "chunks", chunks, "maxPayload", b.nc.MaxPayload())
	}
}

// finishDaemonConnected stores the subscription set for a daemon and kicks off
// the pending-command drain.
func (b *NATSToolBridge) finishDaemonConnected(daemonCtx context.Context, userID, daemonID string, subs []*nats.Subscription) {
	// Store all subscriptions for this daemon.
	b.subsMu.Lock()
	b.daemonSubs[daemonID] = subs
	b.subsMu.Unlock()

	logging.Info("[NATSToolBridge] Subscribing to daemon subjects",
		"userID", userID, "daemonID", daemonID, "count", len(subs))

	// Drain any pending commands queued before this daemon was online, then
	// keep polling for as long as the daemon stays connected. The
	// control-plane publishes to daemon.pending.{daemonID} via JetStream —
	// for a genuinely fire-and-forget command (e.g. git.clone) that publish
	// happens whether or not the daemon is online AT THAT MOMENT, so a
	// one-shot drain at connect would miss anything enqueued after this
	// daemon was already connected and stranded it until the daemon's next
	// reconnect. See pollPendingCommands.
	go b.pollPendingCommands(daemonCtx, userID, daemonID)
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

// startTerminalOutputForwarder subscribes to local terminal output for the
// given session and publishes events to NATS on
// daemon.terminal.output.{userID}.{daemonID}.{sessionID}.
//
// This is driven by a terminal-output-subscribe request (not the terminal.create
// hook): b.mgr.SubscribeTerminalOutput sends the terminal-output-subscribe
// message down to the daemon, which is what starts the daemon's PTY output pump.
// Deferring the pump until a subscriber's interest chain is fully established is
// the whole point of the terminal-output race fix — it mirrors the
// process-output subscribe flow.
func (b *NATSToolBridge) startTerminalOutputForwarder(userCtx context.Context, userID, daemonID, sessionID string) {
	if sessionID == "" {
		logging.Warn("[NATSToolBridge] Empty sessionID for terminal output forwarder", "userID", userID)
		return
	}

	outputCh, unsub, err := b.mgr.SubscribeTerminalOutput(userID, sessionID)
	if err != nil {
		logging.Warn("[NATSToolBridge] Failed to subscribe to terminal output for forwarding",
			"error", err, "userID", userID, "sessionID", sessionID)
		return
	}

	subject := daemonSubject(terminalOutputSubject, userID, daemonID) + "." + sessionID
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
// Note: process output subjects are NOT per-daemon — a processID is already unique.
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

// sanitizePendingSubjectToken mirrors control-plane's
// natsio.SanitizeSubject (control-plane/internal/natsio/publisher.go). It
// MUST stay byte-for-byte identical: the daemon.pending.{daemonID} subject
// is the invariant that the enqueue side (control-plane's
// PublishPendingDaemonCommand AND our own NATSDaemonRouter.EnqueueDaemonCommand)
// and the drain side (drainPendingCommands' FilterSubject) have to agree on.
// If the two diverge for any daemonID containing '.', '>', '*' or ' ', the
// published subject never matches the consumer filter and the queued command
// (e.g. a git.clone) is silently never delivered.
func sanitizePendingSubjectToken(s string) string {
	return strings.NewReplacer(".", "_", ">", "_", "*", "_", " ", "_").Replace(s)
}

const (
	// maxPendingCommandDeliveries bounds redelivery of a pending command that
	// keeps failing to dispatch, so a poison message can't loop forever.
	// NumDelivered is 1 on the first delivery.
	maxPendingCommandDeliveries = 5
	// pendingCommandRedeliveryDelay spaces out redeliveries of a command whose
	// dispatch failed transiently (e.g. daemon momentarily busy right after
	// reconnect). It also guarantees a Nak'd message is not immediately
	// re-fetched within the same drain's fetch window.
	pendingCommandRedeliveryDelay = 5 * time.Second

	// pendingCommandDispatchDefaultTimeout bounds a dispatched command whose
	// envelope carries no TimeoutMs (zero/unset). This is a real case, not a
	// hypothetical: EnqueueDaemonCommand honors whatever the caller passes,
	// and the wire envelope's TimeoutMs field is only ever set by callers
	// that explicitly populate it.
	pendingCommandDispatchDefaultTimeout = 60 * time.Second
	// pendingCommandDispatchMaxTimeout caps a declared TimeoutMs so a
	// misconfigured enqueue can't pin a drain goroutine (and the daemon
	// connection's context) open indefinitely.
	pendingCommandDispatchMaxTimeout = 5 * time.Minute
)

// pendingDrainFetchBudget bounds how long drainPendingCommands spends
// pulling ALREADY-QUEUED messages off DAEMON_PENDING_COMMANDS on daemon
// connect — it must NOT bound how long a dispatched command is allowed to
// run. Those two were previously the same context, which cut off a
// mid-flight git.clone (a 10-60s NATS round trip, see
// filesystemMutatingCommands in daemonruntime/runtime.go) after exactly 5s
// even though the daemon went on to finish the clone successfully — the
// caller had already given up and NAK'd it for redelivery. Var (not const)
// so tests can shrink it instead of running real-time against a 5s budget.
var pendingDrainFetchBudget = 5 * time.Second

// pendingCommandPollInterval controls how often pollPendingCommands re-runs
// drainPendingCommands for a daemon that stays connected. A genuinely
// fire-and-forget enqueue (control-plane's Clone, which never sends a live
// NATS request at all — see gitcredential.svc.Clone) can land on
// DAEMON_PENDING_COMMANDS at any point during a long-lived connection, not
// just before it. A connect-time-only drain would strand that command until
// the daemon's next reconnect, which may be hours away. Var so tests don't
// pay real wall-clock time for it.
var pendingCommandPollInterval = 10 * time.Second

// pollPendingCommands runs drainPendingCommands once immediately (covering
// anything queued while the daemon was offline) and then repeats on
// pendingCommandPollInterval for as long as ctx (the per-daemon connection
// context) is alive, so commands enqueued at any point during a long-lived
// connection still get picked up without waiting for a reconnect.
func (b *NATSToolBridge) pollPendingCommands(ctx context.Context, userID, daemonID string) {
	b.drainPendingCommands(ctx, userID, daemonID)

	ticker := time.NewTicker(pendingCommandPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.drainPendingCommands(ctx, userID, daemonID)
		}
	}
}

// drainPendingCommands creates a pull consumer on the
// DAEMON_PENDING_COMMANDS stream, drains any messages currently queued for
// this daemon, dispatches them, and returns. Called both once at connect
// (draining what queued while offline) and periodically by
// pollPendingCommands while the daemon stays connected.
func (b *NATSToolBridge) drainPendingCommands(ctx context.Context, userID, daemonID string) {
	if b.js == nil {
		return
	}

	// Sanitize to match the enqueue side (see sanitizePendingSubjectToken).
	subject := pendingSubjectPrefix + sanitizePendingSubjectToken(daemonID)

	// Look up the stream — if it doesn't exist yet, skip gracefully.
	stream, err := b.js.Stream(ctx, "DAEMON_PENDING_COMMANDS")
	if err != nil {
		if errors.Is(err, jetstream.ErrStreamNotFound) {
			logging.Debug("[NATSToolBridge] DAEMON_PENDING_COMMANDS stream not found, skipping drain",
				"daemonID", daemonID)
			return
		}
		logging.Warn("[NATSToolBridge] Failed to look up DAEMON_PENDING_COMMANDS stream",
			"daemonID", daemonID, "error", err)
		return
	}

	// Create a *named* pull consumer filtered to this daemon's subject, via
	// CreateOrUpdateConsumer (idempotent) rather than an anonymous
	// CreateConsumer. This matters because DAEMON_PENDING_COMMANDS uses
	// WorkQueue retention, which permits only ONE consumer per filter subject.
	// With an anonymous consumer, a fast disconnect→reconnect would collide
	// with the previous, not-yet-reaped consumer (InactiveThreshold below is
	// 1 minute) — CreateConsumer would fail with an overlapping-filter error
	// and the drain would be skipped, stranding the queued command. A
	// deterministic name is simply re-attached on reconnect, and it preserves
	// per-message NumDelivered so the bounded-retry guard below actually
	// bounds across reconnects. Explicit ack policy is required (ordered
	// consumers force AckNone and fail with "consumer in pull mode requires
	// ack policy").
	consumerName := "pending-drain-" + sanitizePendingSubjectToken(daemonID)
	consumer, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Name:              consumerName,
		Durable:           consumerName,
		FilterSubject:     subject,
		AckPolicy:         jetstream.AckExplicitPolicy,
		DeliverPolicy:     jetstream.DeliverAllPolicy,
		AckWait:           30 * time.Second,
		InactiveThreshold: 1 * time.Minute,
	})
	if err != nil {
		logging.Warn("[NATSToolBridge] Failed to create pending commands consumer",
			"daemonID", daemonID, "consumer", consumerName, "error", err)
		return
	}
	// drainClean stays true only if we ack'd everything we saw. If any message
	// was Nak'd for redelivery, we intentionally leave the named consumer in
	// place so its NumDelivered accounting survives — InactiveThreshold reaps
	// it later. Deleting it here would reset the redelivery count and defeat
	// the poison-message guard.
	drainClean := true
	defer func() {
		if !drainClean {
			return
		}
		// Best-effort cleanup — InactiveThreshold above is the fallback.
		if delErr := stream.DeleteConsumer(ctx, consumerName); delErr != nil &&
			!errors.Is(delErr, jetstream.ErrConsumerNotFound) {
			logging.Debug("[NATSToolBridge] Pending commands consumer cleanup failed (InactiveThreshold will reap)",
				"daemonID", daemonID, "consumer", consumerName, "error", delErr)
		}
	}()

	// fetchCtx bounds ONLY the loop below that pulls already-queued messages
	// off the stream — we want to drain what's queued, not block waiting for
	// new arrivals. It must never be handed to SendDaemonCommand: dispatching
	// a command is a separate, longer-lived operation with its own timeout
	// derived from the envelope below (see pendingCommandDispatchTimeout).
	fetchCtx, fetchCancel := context.WithTimeout(ctx, pendingDrainFetchBudget)
	defer fetchCancel()

	var dispatched int
	for {
		// Bound total time spent looping over fetch batches to the drain
		// budget, independent of how long any individual dispatch below
		// takes (dispatch now runs on its own context — see
		// pendingCommandDispatchTimeout).
		if fetchCtx.Err() != nil {
			break
		}
		// Each individual Fetch call is capped at 2s so a slow/empty stream
		// doesn't stall past the fetchCtx budget by more than one poll.
		msgs, err := consumer.Fetch(100, jetstream.FetchMaxWait(2*time.Second))
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				break
			}
			logging.Warn("[NATSToolBridge] Error fetching pending commands",
				"daemonID", daemonID, "error", err)
			break
		}

		gotMessages := false
		for msg := range msgs.Messages() {
			gotMessages = true

			var envelope struct {
				RequestID   string                   `json:"request_id"`
				CommandType string                   `json:"command_type"`
				Payload     json.RawMessage          `json:"payload"`
				TimeoutMs   int32                    `json:"timeout_ms"`
				Policy      *daemonpolicy.WirePolicy `json:"policy,omitempty"`
			}
			if err := json.Unmarshal(msg.Data(), &envelope); err != nil {
				logging.Warn("[NATSToolBridge] Failed to unmarshal pending command",
					"daemonID", daemonID, "error", err)
				_ = msg.Ack()
				continue
			}

			protoReq := &reliantv1.DaemonCommandRequest{
				RequestId:   envelope.RequestID,
				CommandType: envelope.CommandType,
				Payload:     envelope.Payload,
				TimeoutMs:   envelope.TimeoutMs,
				// A replayed command must carry the same confinement it was
				// issued under; a queued request that woke up unrestricted
				// would be a policy bypass with a delay fuse.
				Policy: daemonpolicy.WireToProto(envelope.Policy),
			}

			// Dispatch runs on ITS OWN context, bounded by the command's
			// declared TimeoutMs (default/cap below), never by fetchCtx.
			// fetchCtx expires after pendingDrainFetchBudget purely to stop
			// the drain LOOP from polling for new batches — reusing it here
			// cancelled a still-running command (e.g. a 10-60s git.clone)
			// out from under the daemon after 5s even though the daemon
			// went on to finish it. Root cause of the prod clone failures;
			// see the drainPendingCommands doc comment.
			dispatchTimeout := pendingCommandDispatchDefaultTimeout
			if envelope.TimeoutMs > 0 {
				dispatchTimeout = time.Duration(envelope.TimeoutMs) * time.Millisecond
				if dispatchTimeout > pendingCommandDispatchMaxTimeout {
					dispatchTimeout = pendingCommandDispatchMaxTimeout
				}
			}
			dispatchCtx, dispatchCancel := context.WithTimeout(ctx, dispatchTimeout)
			_, err = b.mgr.SendDaemonCommand(dispatchCtx, userID, protoReq)
			dispatchCancel()
			if err != nil {
				// Dispatch failed. This is often transient — the daemon may be
				// momentarily busy or the stream just healed on reconnect.
				// DAEMON_PENDING_COMMANDS is WorkQueue retention, so the ONLY
				// way to retry is to NOT ack and let JetStream redeliver; the
				// daemon's own command idempotency absorbs any double-delivery.
				// Acking here (the previous behavior) permanently dropped the
				// command — the exact bug where an enqueued git.clone never ran
				// after reconnect. Bound the retries so a genuinely poison
				// command can't loop forever.
				numDelivered := uint64(0)
				if md, mdErr := msg.Metadata(); mdErr == nil && md != nil {
					numDelivered = md.NumDelivered
				}
				if numDelivered >= maxPendingCommandDeliveries {
					logging.Error("[NATSToolBridge] Dropping pending command after max delivery attempts",
						"daemonID", daemonID, "commandType", envelope.CommandType,
						"requestID", envelope.RequestID, "numDelivered", numDelivered, "error", err)
					_ = msg.Ack()
					continue
				}
				logging.Warn("[NATSToolBridge] Failed to dispatch pending command; leaving un-acked for redelivery",
					"daemonID", daemonID, "commandType", envelope.CommandType,
					"requestID", envelope.RequestID, "numDelivered", numDelivered, "error", err)
				_ = msg.NakWithDelay(pendingCommandRedeliveryDelay)
				drainClean = false
				continue
			}
			_ = msg.Ack()
			dispatched++
		}

		if msgs.Error() != nil {
			// ErrMsgIteratorClosed or timeout — we're done draining.
			break
		}
		if !gotMessages {
			break
		}
	}

	if dispatched > 0 {
		logging.Info("[NATSToolBridge] Drained pending commands",
			"daemonID", daemonID, "count", dispatched)
	}
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
