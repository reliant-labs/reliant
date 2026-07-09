// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/daemonliveness"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/grpc/interceptors"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/observability"
)

// NATS subject patterns for tool execution and daemon routing.
// Subjects now include {userID}.{daemonID} for multi-daemon routing.
const (
	toolRequestSubject = "tools.request" // tools.request.{userID}.{daemonID}
	toolCancelSubject  = "tools.cancel"  // tools.cancel.{userID}.{daemonID}
	toolOnlineSubject  = "tools.online"  // tools.online.{userID}.{daemonID}

	daemonKillSubject      = "daemon.process.kill" // daemon.process.kill.{userID}.{daemonID}
	daemonCommandSubject   = "daemon.command"      // daemon.command.{userID}.{daemonID}
	toolRequestSyncSubject = "tools.request.sync"  // tools.request.sync.{userID}.{daemonID}

	configLoadSubject  = "daemon.config.load"  // daemon.config.load.{userID}.{daemonID}
	configWatchSubject = "daemon.config.watch" // daemon.config.watch.{userID}.{daemonID}

	// Terminal streaming subjects
	terminalInputSubject     = "daemon.terminal.input"     // daemon.terminal.input.{userID}.{daemonID}.{sessionID}
	terminalResizeSubject    = "daemon.terminal.resize"    // daemon.terminal.resize.{userID}.{daemonID}.{sessionID}
	terminalOutputSubject    = "daemon.terminal.output"    // daemon.terminal.output.{userID}.{daemonID}.{sessionID}
	terminalSubscribeSubject = "daemon.terminal.subscribe" // daemon.terminal.subscribe.{userID}.{daemonID}.{sessionID}

	// Process output streaming subjects
	processOutputSubscribeSubject = "daemon.process.subscribe" // daemon.process.subscribe.{userID}.{daemonID}
	processOutputSubject          = "daemon.process.output"    // daemon.process.output.{userID}.{processID}
)

// daemonSubject builds a NATS subject with the pattern base.{userID}.{daemonID}.
func daemonSubject(base, userID, daemonID string) string {
	return base + "." + userID + "." + daemonID
}

// NATSDaemonRouter implements DaemonRouter using NATS pub/sub.
// Used by workers and api-server replicas in distributed mode to route
// daemon operations to the api-server that holds the daemon's gRPC connection.
type NATSDaemonRouter struct {
	nc                 *nats.Conn
	db                 db.Repository
	resolver           DaemonResolver                               // optional: used to resolve daemonID for a user
	controlPlaneClient reliantv1connect.DaemonRegistryServiceClient // optional: gRPC client for control plane resolution

	// jsOnce lazily initializes the JetStream context the first time
	// EnqueueDaemonCommand is called. JetStream is only used for the
	// pending-commands stream — the rest of the router is core NATS only,
	// and we don't want to pay the JetStream startup cost on every router
	// that never enqueues.
	jsOnce sync.Once
	js     jetstream.JetStream
	jsErr  error
}

// NewNATSDaemonRouter creates a new NATS-based daemon router.
func NewNATSDaemonRouter(nc *nats.Conn, opts ...NATSRouterOption) *NATSDaemonRouter {
	r := &NATSDaemonRouter{nc: nc}
	for _, o := range opts {
		o(r)
	}
	return r
}

// NATSRouterOption configures a NATSDaemonRouter.
type NATSRouterOption func(*NATSDaemonRouter)

// WithDatabase adds a DB repository for fast daemon-online checks.
func WithDatabase(repo db.Repository) NATSRouterOption {
	return func(r *NATSDaemonRouter) {
		r.db = repo
	}
}

// WithResolver sets the DaemonResolver for multi-daemon routing.
func WithResolver(resolver DaemonResolver) NATSRouterOption {
	return func(r *NATSDaemonRouter) {
		r.resolver = resolver
	}
}

// WithControlPlaneClient sets the gRPC client for control plane daemon resolution.
// When set, the router calls ResolveDaemon/ResumeDaemon RPCs on the control plane
// when local/connected-only resolution fails. When nil, the router operates in
// OSS-only mode (connected daemons only).
func WithControlPlaneClient(client reliantv1connect.DaemonRegistryServiceClient) NATSRouterOption {
	return func(r *NATSDaemonRouter) {
		r.controlPlaneClient = client
	}
}

// resolveDefaultDaemonID resolves the default daemon ID for a user.
// Falls back to the first active daemon in the DB if no resolver is set.
func (r *NATSDaemonRouter) resolveDefaultDaemonID(ctx context.Context, userID string) (string, error) {
	return r.resolveDaemonID(ctx, userID, nil)
}

// resolveDaemonID resolves a daemon ID for a user, optionally using a selector.
// Resolution order:
//  1. Local resolver (connected daemons on this gateway)
//  2. Control plane gRPC (ResolveDaemon RPC) if controlPlaneClient is set
//  3. DB fallback
//
// If the resolved daemon is suspended and controlPlaneClient is set,
// ResumeDaemon is called to wake it up.
func (r *NATSDaemonRouter) resolveDaemonID(ctx context.Context, userID string, selector *DaemonSelector) (string, error) {
	// Step 1: Try local resolver (connected daemons).
	if r.resolver != nil {
		daemons, err := r.resolver.ResolveDaemons(ctx, userID, selector)
		if err != nil {
			return "", err
		}
		if len(daemons) > 0 {
			// When no selector, prefer local daemons.
			if selector == nil {
				for _, d := range daemons {
					if d.Type == "local" {
						return d.DaemonID, nil
					}
				}
			}
			return daemons[0].DaemonID, nil
		}
		// No connected daemons matched — fall through to control plane.
	}

	// Step 2: Try control plane gRPC resolution.
	if r.controlPlaneClient != nil {
		daemonID, err := r.resolveViaControlPlane(ctx, selector)
		if err == nil {
			return daemonID, nil
		}
		// If control plane doesn't find a daemon, fall through to DB.
	}

	// Step 3: Fallback — look up from DB. Routability is now derived from the
	// daemon_attachment table: a daemon is reachable iff it has a recent attachment
	// row, regardless of any stale state recorded on the daemons row itself.
	if r.db != nil {
		daemons, err := r.db.ListDaemonsByUserID(ctx, userID)
		if err != nil {
			return "", fmt.Errorf("resolving daemon ID from DB: %w", err)
		}
		attachedIDs, err := r.db.ListAttachedDaemonIDsForUser(ctx, userID, daemonStaleThreshold)
		if err != nil {
			return "", fmt.Errorf("resolving attached daemon IDs from DB: %w", err)
		}
		attached := make(map[string]bool, len(attachedIDs))
		for _, id := range attachedIDs {
			attached[id] = true
		}
		// Attachment freshness is a decaying hint, not ground truth: routing is
		// authoritative via NATS (a request to a daemon with no live subscription
		// returns ErrNoResponders → CodeUnavailable). So PREFER a daemon with a
		// fresh attachment, but fall back to any matching daemon rather than
		// erroring — this keeps a connected-but-idle daemon (whose attachment
		// timestamp has gone stale) routable, and lets the NATS request decide
		// actual reachability.
		var fallbackID string
		for _, d := range daemons {
			if selector != nil && selector.Type != "" && selector.Type != "any" {
				continue
			}
			if attached[d.ID] {
				return d.ID, nil
			}
			if fallbackID == "" {
				fallbackID = d.ID
			}
		}
		if fallbackID != "" {
			return fallbackID, nil
		}
	}

	if selector != nil {
		return "", fmt.Errorf("no daemon matching selector (type=%q, name=%q, id=%q) for user %s", selector.Type, selector.Name, selector.ID, userID)
	}
	return "", fmt.Errorf("no daemon available for user %s", userID)
}

// resolveViaControlPlane calls the control plane's ResolveDaemon RPC.
// If the resolved daemon is suspended, it calls ResumeDaemon to wake it.
func (r *NATSDaemonRouter) resolveViaControlPlane(ctx context.Context, selector *DaemonSelector) (string, error) {
	req := &reliantv1.ResolveDaemonRequest{}
	if selector != nil {
		req.DaemonId = selector.ID
		req.DaemonName = selector.Name
		req.DaemonType = selector.Type
		req.Labels = selector.Labels
	}

	resp, err := r.controlPlaneClient.ResolveDaemon(ctx, connect.NewRequest(req))
	if err != nil {
		return "", fmt.Errorf("control plane ResolveDaemon: %w", err)
	}
	if !resp.Msg.Found || resp.Msg.Daemon == nil {
		return "", fmt.Errorf("control plane found no matching daemon")
	}

	daemon := resp.Msg.Daemon

	// If daemon is suspended or idle, try to wake it up.
	if daemon.Status == reliantv1.DaemonStatus_DAEMON_STATUS_IDLE ||
		daemon.Status == reliantv1.DaemonStatus_DAEMON_STATUS_DISCONNECTED {
		resumeResp, err := r.controlPlaneClient.ResumeDaemon(ctx, connect.NewRequest(&reliantv1.ResumeDaemonRequest{
			DaemonId: daemon.DaemonId,
		}))
		if err != nil {
			return "", fmt.Errorf("control plane ResumeDaemon(%s): %w", daemon.DaemonId, err)
		}
		if !resumeResp.Msg.Resumed {
			return "", fmt.Errorf("daemon %s could not be resumed: %s", daemon.DaemonId, resumeResp.Msg.ErrorMessage)
		}
	}

	return daemon.DaemonId, nil
}

// daemonStaleThreshold is 2× the daemon heartbeat interval. If the frontend
// reports a heartbeat within this window we skip the DB query.
const daemonStaleThreshold = 30 * time.Second

// LocalConnectionChecker is implemented by resolvers that can report whether
// any daemon stream is currently held in-process for a given user. It lets the
// router skip DB-based liveness checks for single-replica deployments.
type LocalConnectionChecker interface {
	HasConnectedDaemonsForUser(userID string) bool
}

// isDaemonReachable checks whether the daemon is online. If the resolver also
// satisfies LocalConnectionChecker and reports a live stream on this gateway,
// the DB lookup is skipped — the in-memory map is authoritative for the
// connections this process owns. Otherwise it falls back to the auth header
// hint and finally to the DB attachment query.
func (r *NATSDaemonRouter) isDaemonReachable(ctx context.Context, userID string) (bool, error) {
	if checker, ok := r.resolver.(LocalConnectionChecker); ok && checker.HasConnectedDaemonsForUser(userID) {
		return true, nil
	}
	if interceptors.DaemonLastSeenFresh(ctx, daemonStaleThreshold) {
		return true, nil
	}
	return r.IsDaemonOnline(ctx, userID)
}

// IsDaemonOnline is a thin wrapper that delegates to
// daemonliveness.ReachableByUser. The signature is preserved so call sites
// don't change during the Step 1 migration.
//
// When no DB is configured (OSS single-replica) the router falls back to the
// legacy NATS request-reply path, which enumerates and asks; otherwise the
// daemonliveness package owns the answer.
func (r *NATSDaemonRouter) IsDaemonOnline(ctx context.Context, userID string) (bool, error) {
	if r.db == nil {
		return r.isDaemonOnlineViaNATS(ctx, userID)
	}
	s, err := daemonliveness.ReachableByUser(ctx, r.nc, dbAdapter{r.db}, userID)
	if err != nil {
		return false, fmt.Errorf("checking daemon liveness: %w", err)
	}
	return s.Live, nil
}

// dbAdapter implements daemonliveness.Repository by delegating to the
// existing db.Repository. We deliberately do NOT add a new repo method:
// IsDaemonAttached already answers the boolean we need, and LastSeen=zero is
// acceptable for the current callers (none read it). The per-daemon variant
// is a TODO — see below.
type dbAdapter struct {
	repo db.Repository
}

func (a dbAdapter) GetUserLiveness(ctx context.Context, userID string, staleThreshold time.Duration) (daemonliveness.Status, error) {
	live, err := a.repo.IsDaemonAttached(ctx, userID, staleThreshold)
	if err != nil {
		return daemonliveness.Status{}, err
	}
	return daemonliveness.Status{Live: live}, nil
}

func (a dbAdapter) GetDaemonLiveness(ctx context.Context, daemonID string, staleThreshold time.Duration) (daemonliveness.Status, error) {
	// TODO: add a per-daemon attachment-freshness query to db.Repository when
	// the first caller for Reachable(daemonID) lands. For now, callers route
	// through ReachableByUser via IsDaemonOnline, so this path is unused.
	_ = ctx
	_ = daemonID
	_ = staleThreshold
	return daemonliveness.Status{}, fmt.Errorf("dbAdapter.GetDaemonLiveness: not implemented; no caller yet")
}

// isDaemonOnlineViaNATS is the legacy NATS request-reply check, used as fallback
// when no DB is configured.
func (r *NATSDaemonRouter) isDaemonOnlineViaNATS(ctx context.Context, userID string) (bool, error) {
	// Try resolving a specific daemon first; fall back to wildcard.
	daemonID, err := r.resolveDefaultDaemonID(ctx, userID)
	if err != nil {
		return false, nil // No daemon found → offline.
	}
	subject := daemonSubject(toolOnlineSubject, userID, daemonID)
	reqMsg := observability.NATSPublishMsg(ctx, subject, nil)
	start := time.Now()
	msg, err := r.nc.RequestMsg(reqMsg, 2*time.Second)
	observability.NATSRequestDuration.WithLabelValues("tools.online").Observe(time.Since(start).Seconds())
	if err != nil {
		// No subscribers means no api-server holds this daemon's connection.
		// Timeout means the request went out but nobody answered (daemon genuinely offline).
		// Both are definitive "offline" — not infrastructure failures.
		if err == nats.ErrNoResponders || err == nats.ErrTimeout {
			return false, nil
		}
		// Other errors (connection closed, etc.) are infrastructure failures.
		observability.NATSErrorsTotal.WithLabelValues("tools.online", "request").Inc()
		return false, fmt.Errorf("NATS IsDaemonOnline request failed: %w", err)
	}
	return string(msg.Data) == "true", nil
}

func (r *NATSDaemonRouter) SendToolRequest(ctx context.Context, userID string, request *ToolExecutionRequest) error {
	daemonID, err := r.resolveDefaultDaemonID(ctx, userID)
	if err != nil {
		return fmt.Errorf("resolving daemon for tool request: %w", err)
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	subject := daemonSubject(toolRequestSubject, userID, daemonID)
	msg := observability.NATSPublishMsg(ctx, subject, payload)
	if err := r.nc.PublishMsg(msg); err != nil {
		observability.NATSErrorsTotal.WithLabelValues("tools.request", "publish").Inc()
		return err
	}
	observability.NATSPublishTotal.WithLabelValues("tools.request").Inc()
	return nil
}

func (r *NATSDaemonRouter) SendToolExecutionCancel(ctx context.Context, userID, requestID, reason string) error {
	daemonID, err := r.resolveDefaultDaemonID(ctx, userID)
	if err != nil {
		return fmt.Errorf("resolving daemon for cancel: %w", err)
	}
	payload, err := json.Marshal(map[string]string{
		"request_id": requestID,
		"reason":     reason,
	})
	if err != nil {
		return err
	}
	subject := daemonSubject(toolCancelSubject, userID, daemonID)
	msg := observability.NATSPublishMsg(ctx, subject, payload)
	if err := r.nc.PublishMsg(msg); err != nil {
		observability.NATSErrorsTotal.WithLabelValues("tools.cancel", "publish").Inc()
		return err
	}
	observability.NATSPublishTotal.WithLabelValues("tools.cancel").Inc()
	return nil
}

// daemonRequestError maps a NATS request/reply error to a caller-facing error.
// nats.ErrNoResponders is authoritative: the NATS server reports that no
// subscription is currently live for the daemon's subject (the gateway
// subscribes on connect and unsubscribes on disconnect), i.e. the daemon is not
// connected. That is the ground-truth reachability signal — we deliberately do
// NOT pre-check a decaying DB freshness timestamp, which produced false
// "offline" for connected-but-idle daemons. Any other error (timeouts, wedged
// daemon, transport failure) is surfaced as-is rather than mislabeled as
// "no daemon connected".
func daemonRequestError(op string, err error) error {
	if err == nats.ErrNoResponders {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("no daemon connected for user"))
	}
	return fmt.Errorf("%s via NATS failed: %w", op, err)
}

func (r *NATSDaemonRouter) SendKillProcess(ctx context.Context, userID, processID string) error {
	payload, err := json.Marshal(map[string]string{
		"process_id": processID,
	})
	if err != nil {
		return err
	}

	daemonID, err := r.resolveDefaultDaemonID(ctx, userID)
	if err != nil {
		return fmt.Errorf("resolving daemon for kill: %w", err)
	}
	subject := daemonSubject(daemonKillSubject, userID, daemonID)
	reqMsg := observability.NATSPublishMsg(ctx, subject, payload)
	start := time.Now()
	msg, err := r.nc.RequestMsg(reqMsg, 10*time.Second)
	observability.NATSRequestDuration.WithLabelValues("daemon.process.kill").Observe(time.Since(start).Seconds())
	if err != nil {
		observability.NATSErrorsTotal.WithLabelValues("daemon.process.kill", "request").Inc()
		return daemonRequestError("kill process", err)
	}

	// Check response for error.
	var resp struct {
		Error string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(msg.Data, &resp); err == nil && resp.Error != "" {
		return fmt.Errorf("remote kill failed: %s", resp.Error)
	}
	return nil
}

func (r *NATSDaemonRouter) SendDaemonCommand(ctx context.Context, userID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	req := struct {
		RequestID   string          `json:"request_id"`
		CommandType string          `json:"command_type"`
		Payload     json.RawMessage `json:"payload"`
		TimeoutMs   int32           `json:"timeout_ms"`
	}{
		RequestID:   fmt.Sprintf("%d", time.Now().UnixNano()),
		CommandType: commandType,
		Payload:     json.RawMessage(payload),
		TimeoutMs:   timeoutMs,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal daemon command: %w", err)
	}

	timeout := 30 * time.Second
	if timeoutMs > 0 {
		timeout = time.Duration(timeoutMs) * time.Millisecond
	}

	// Respect the caller's context deadline if it's sooner than the explicit timeout.
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}

	// Run NATS request in a goroutine so we can also select on ctx.Done().
	type natsResult struct {
		msg *nats.Msg
		err error
	}
	daemonID, err := r.resolveDefaultDaemonID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("resolving daemon for command: %w", err)
	}
	subject := daemonSubject(daemonCommandSubject, userID, daemonID)
	reqMsg := observability.NATSPublishMsg(ctx, subject, data)
	resultCh := make(chan natsResult, 1)
	start := time.Now()
	go func() {
		msg, err := r.nc.RequestMsg(reqMsg, timeout)
		resultCh <- natsResult{msg, err}
	}()

	var msg *nats.Msg
	select {
	case res := <-resultCh:
		observability.NATSRequestDuration.WithLabelValues("daemon.command").Observe(time.Since(start).Seconds())
		if res.err != nil {
			observability.NATSErrorsTotal.WithLabelValues("daemon.command", "request").Inc()
			return nil, daemonRequestError("daemon command", res.err)
		}
		msg = res.msg
	case <-ctx.Done():
		_ = r.SendToolExecutionCancel(context.Background(), userID, req.RequestID, "daemon command caller cancelled")
		observability.NATSErrorsTotal.WithLabelValues("daemon.command", "timeout").Inc()
		return nil, fmt.Errorf("daemon command via NATS failed: %w", ctx.Err())
	}

	var resp struct {
		Success      bool   `json:"success"`
		Payload      []byte `json:"payload"`
		ErrorMessage string `json:"error_message,omitempty"`
	}
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal daemon command response: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("daemon command %q failed: %s", commandType, resp.ErrorMessage)
	}
	return resp.Payload, nil
}

func (r *NATSDaemonRouter) SendToolRequestSync(ctx context.Context, userID string, request *ToolExecutionRequest) (*ToolExecutionResponse, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal tool request: %w", err)
	}

	timeout := 10 * time.Minute
	if request.TimeoutMs > 0 {
		timeout = time.Duration(request.TimeoutMs)*time.Millisecond + 30*time.Second // buffer for daemon overhead
	}

	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}

	type natsResult struct {
		msg *nats.Msg
		err error
	}
	resolvedDaemonID, err := r.resolveDefaultDaemonID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("resolving daemon for sync request: %w", err)
	}
	subject := daemonSubject(toolRequestSyncSubject, userID, resolvedDaemonID)
	reqMsg := observability.NATSPublishMsg(ctx, subject, payload)
	resultCh := make(chan natsResult, 1)
	start := time.Now()
	go func() {
		msg, err := r.nc.RequestMsg(reqMsg, timeout)
		resultCh <- natsResult{msg, err}
	}()

	var msg *nats.Msg
	select {
	case res := <-resultCh:
		observability.NATSRequestDuration.WithLabelValues("tools.request.sync").Observe(time.Since(start).Seconds())
		if res.err != nil {
			observability.NATSErrorsTotal.WithLabelValues("tools.request.sync", "request").Inc()
			return nil, daemonRequestError("tool request", res.err)
		}
		msg = res.msg
	case <-ctx.Done():
		observability.NATSErrorsTotal.WithLabelValues("tools.request.sync", "timeout").Inc()
		return nil, fmt.Errorf("tool request via NATS failed: %w", ctx.Err())
	}

	var resp ToolExecutionResponse
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal tool response: %w", err)
	}
	return &resp, nil
}

func (r *NATSDaemonRouter) SendToolRequestSyncWithSelector(ctx context.Context, userID string, request *ToolExecutionRequest, selector *DaemonSelector) (*ToolExecutionResponse, error) {
	if selector == nil {
		return r.SendToolRequestSync(ctx, userID, request)
	}

	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal tool request: %w", err)
	}

	timeout := 10 * time.Minute
	if request.TimeoutMs > 0 {
		timeout = time.Duration(request.TimeoutMs)*time.Millisecond + 30*time.Second
	}
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}

	resolvedDaemonID, err := r.resolveDaemonID(ctx, userID, selector)
	if err != nil {
		return nil, fmt.Errorf("resolving daemon for selector: %w", err)
	}

	type natsResult struct {
		msg *nats.Msg
		err error
	}
	subject := daemonSubject(toolRequestSyncSubject, userID, resolvedDaemonID)
	reqMsg := observability.NATSPublishMsg(ctx, subject, payload)
	resultCh := make(chan natsResult, 1)
	start := time.Now()
	go func() {
		msg, err := r.nc.RequestMsg(reqMsg, timeout)
		resultCh <- natsResult{msg, err}
	}()

	var msg *nats.Msg
	select {
	case res := <-resultCh:
		observability.NATSRequestDuration.WithLabelValues("tools.request.sync.selector").Observe(time.Since(start).Seconds())
		if res.err != nil {
			observability.NATSErrorsTotal.WithLabelValues("tools.request.sync.selector", "request").Inc()
			return nil, daemonRequestError("tool request", res.err)
		}
		msg = res.msg
	case <-ctx.Done():
		observability.NATSErrorsTotal.WithLabelValues("tools.request.sync.selector", "timeout").Inc()
		return nil, fmt.Errorf("tool request via NATS failed: %w", ctx.Err())
	}

	var resp ToolExecutionResponse
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal tool response: %w", err)
	}
	return &resp, nil
}

func (r *NATSDaemonRouter) SendLoadProjectConfigs(ctx context.Context, userID string, projectPath string, requestID string) error {
	payload, err := json.Marshal(map[string]string{
		"project_path": projectPath,
		"request_id":   requestID,
	})
	if err != nil {
		return err
	}
	resolvedDaemonID, err := r.resolveDefaultDaemonID(ctx, userID)
	if err != nil {
		return fmt.Errorf("resolving daemon for config load: %w", err)
	}
	subject := daemonSubject(configLoadSubject, userID, resolvedDaemonID)
	msg := observability.NATSPublishMsg(ctx, subject, payload)
	if err := r.nc.PublishMsg(msg); err != nil {
		observability.NATSErrorsTotal.WithLabelValues("daemon.config.load", "publish").Inc()
		return err
	}
	observability.NATSPublishTotal.WithLabelValues("daemon.config.load").Inc()
	return nil
}

func (r *NATSDaemonRouter) SendWatchProjectConfigs(ctx context.Context, userID string, projectPath string, includeInitial bool) error {
	payload, err := json.Marshal(map[string]interface{}{
		"project_path":    projectPath,
		"include_initial": includeInitial,
	})
	if err != nil {
		return err
	}
	resolvedDaemonID, err := r.resolveDefaultDaemonID(ctx, userID)
	if err != nil {
		return fmt.Errorf("resolving daemon for config watch: %w", err)
	}
	subject := daemonSubject(configWatchSubject, userID, resolvedDaemonID)
	msg := observability.NATSPublishMsg(ctx, subject, payload)
	if err := r.nc.PublishMsg(msg); err != nil {
		observability.NATSErrorsTotal.WithLabelValues("daemon.config.watch", "publish").Inc()
		return err
	}
	observability.NATSPublishTotal.WithLabelValues("daemon.config.watch").Inc()
	return nil
}

func (r *NATSDaemonRouter) SendTerminalInput(ctx context.Context, userID string, sessionID string, data []byte) error {
	payload, err := json.Marshal(struct {
		SessionID string `json:"session_id"`
		Data      []byte `json:"data"`
	}{SessionID: sessionID, Data: data})
	if err != nil {
		return err
	}
	resolvedDaemonID, err := r.resolveDefaultDaemonID(ctx, userID)
	if err != nil {
		return fmt.Errorf("resolving daemon for terminal input: %w", err)
	}
	subject := daemonSubject(terminalInputSubject, userID, resolvedDaemonID) + "." + sessionID
	msg := observability.NATSPublishMsg(ctx, subject, payload)
	if err := r.nc.PublishMsg(msg); err != nil {
		observability.NATSErrorsTotal.WithLabelValues("daemon.terminal.input", "publish").Inc()
		return err
	}
	observability.NATSPublishTotal.WithLabelValues("daemon.terminal.input").Inc()
	return nil
}

func (r *NATSDaemonRouter) SendTerminalResize(ctx context.Context, userID string, sessionID string, cols, rows uint32) error {
	payload, err := json.Marshal(struct {
		SessionID string `json:"session_id"`
		Cols      uint32 `json:"cols"`
		Rows      uint32 `json:"rows"`
	}{SessionID: sessionID, Cols: cols, Rows: rows})
	if err != nil {
		return err
	}
	resolvedDaemonID, err := r.resolveDefaultDaemonID(ctx, userID)
	if err != nil {
		return fmt.Errorf("resolving daemon for terminal resize: %w", err)
	}
	subject := daemonSubject(terminalResizeSubject, userID, resolvedDaemonID) + "." + sessionID
	msg := observability.NATSPublishMsg(ctx, subject, payload)
	if err := r.nc.PublishMsg(msg); err != nil {
		observability.NATSErrorsTotal.WithLabelValues("daemon.terminal.resize", "publish").Inc()
		return err
	}
	observability.NATSPublishTotal.WithLabelValues("daemon.terminal.resize").Inc()
	return nil
}

func (r *NATSDaemonRouter) SubscribeTerminalOutput(ctx context.Context, userID string, sessionID string) (<-chan *TerminalOutputEvent, func(), error) {
	resolvedDaemonID, err := r.resolveDefaultDaemonID(ctx, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving daemon for terminal output: %w", err)
	}
	ch := make(chan *TerminalOutputEvent, 64)
	subject := daemonSubject(terminalOutputSubject, userID, resolvedDaemonID) + "." + sessionID

	sub, err := r.nc.Subscribe(subject, func(msg *nats.Msg) {
		var evt TerminalOutputEvent
		if err := json.Unmarshal(msg.Data, &evt); err != nil {
			return
		}
		select {
		case ch <- &evt:
		default:
		}
	})
	if err != nil {
		return nil, nil, fmt.Errorf("subscribe to terminal output via NATS: %w", err)
	}

	// Only AFTER the local NATS subscription is live do we publish the subscribe
	// request down to the bridge. The bridge starts the terminal output
	// forwarder, which starts the daemon's PTY pump — so the daemon does not read
	// the PTY until the full WS->NATS->bridge->daemon interest chain is
	// established and the initial shell prompt can no longer be dropped. Mirrors
	// SubscribeProcessOutput, but with the publish deliberately ordered after the
	// subscribe (the whole point of the terminal fix).
	reqPayload, err := json.Marshal(struct {
		SessionID string `json:"session_id"`
	}{SessionID: sessionID})
	if err != nil {
		_ = sub.Unsubscribe()
		return nil, nil, err
	}
	subscribeSubject := daemonSubject(terminalSubscribeSubject, userID, resolvedDaemonID) + "." + sessionID
	subMsg := observability.NATSPublishMsg(ctx, subscribeSubject, reqPayload)
	if err := r.nc.PublishMsg(subMsg); err != nil {
		observability.NATSErrorsTotal.WithLabelValues("daemon.terminal.subscribe", "publish").Inc()
		_ = sub.Unsubscribe()
		return nil, nil, fmt.Errorf("publish terminal output subscribe request via NATS: %w", err)
	}
	observability.NATSPublishTotal.WithLabelValues("daemon.terminal.subscribe").Inc()

	unsub := func() {
		_ = sub.Unsubscribe()
	}
	return ch, unsub, nil
}

func (r *NATSDaemonRouter) SubscribeProcessOutput(ctx context.Context, userID string, processID string, newOnly bool) (<-chan *ProcessOutputEvent, func(), error) {
	// Publish subscribe request so the bridge/gateway knows to start forwarding.
	reqPayload, err := json.Marshal(struct {
		ProcessID string `json:"process_id"`
		NewOnly   bool   `json:"new_only"`
	}{ProcessID: processID, NewOnly: newOnly})
	if err != nil {
		return nil, nil, err
	}
	resolvedDaemonID, err := r.resolveDefaultDaemonID(ctx, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving daemon for process subscribe: %w", err)
	}
	subject := daemonSubject(processOutputSubscribeSubject, userID, resolvedDaemonID)
	subMsg := observability.NATSPublishMsg(ctx, subject, reqPayload)
	if err := r.nc.PublishMsg(subMsg); err != nil {
		observability.NATSErrorsTotal.WithLabelValues("daemon.process.subscribe", "publish").Inc()
		return nil, nil, fmt.Errorf("publish process output subscribe request via NATS: %w", err)
	}
	observability.NATSPublishTotal.WithLabelValues("daemon.process.subscribe").Inc()

	ch := make(chan *ProcessOutputEvent, 128)
	subject = processOutputSubject + "." + userID + "." + processID

	sub, err := r.nc.Subscribe(subject, func(msg *nats.Msg) {
		var evt ProcessOutputEvent
		if err := json.Unmarshal(msg.Data, &evt); err != nil {
			return
		}
		select {
		case ch <- &evt:
		default:
		}
	})
	if err != nil {
		return nil, nil, fmt.Errorf("subscribe to process output via NATS: %w", err)
	}

	unsub := func() {
		_ = sub.Unsubscribe()
	}
	return ch, unsub, nil
}

func (r *NATSDaemonRouter) Close() error {
	return nil
}

// pendingCommandsStream and pendingSubjectPrefix mirror the values used by
// control-plane's natsio package (StreamDaemonPendingCommands /
// SubjectDaemonPendingAll) and by the gateway's NATSToolBridge drainer.
// Kept as constants here so we don't take a circular dep on control-plane.
const (
	pendingCommandsStream = "DAEMON_PENDING_COMMANDS"
	pendingSubjectPrefix  = "daemon.pending."
)

// EnqueueDaemonCommand persists a fire-and-forget command to
// DAEMON_PENDING_COMMANDS for every daemon the user owns. The gateway's
// NATSToolBridge.drainPendingCommands consumer picks each message up on
// the daemon's next connect and dispatches it via SendDaemonCommand,
// healing creation-time races where the daemon wasn't yet online.
func (r *NATSDaemonRouter) EnqueueDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) (int, error) {
	if userID == "" {
		return 0, fmt.Errorf("userID required")
	}
	if r.db == nil {
		return 0, fmt.Errorf("daemon router has no DB; cannot resolve user's daemons for enqueue")
	}

	daemons, err := r.db.ListDaemonsByUserID(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("listing daemons for enqueue: %w", err)
	}
	if len(daemons) == 0 {
		return 0, nil
	}

	js, err := r.jetstream()
	if err != nil {
		return 0, err
	}

	envelope, err := json.Marshal(struct {
		RequestID   string          `json:"request_id"`
		CommandType string          `json:"command_type"`
		Payload     json.RawMessage `json:"payload"`
		TimeoutMs   int32           `json:"timeout_ms"`
	}{
		RequestID:   fmt.Sprintf("enq-%d", time.Now().UnixNano()),
		CommandType: commandType,
		Payload:     json.RawMessage(payload),
		TimeoutMs:   timeoutMs,
	})
	if err != nil {
		return 0, fmt.Errorf("marshal pending command envelope: %w", err)
	}

	var enqueued int
	for _, d := range daemons {
		if d == nil || d.ID == "" {
			continue
		}
		// Sanitize the daemonID to keep this subject identical to the one the
		// gateway's drainer filters on (see sanitizePendingSubjectToken) and to
		// control-plane's natsio.SanitizeSubject. A raw daemonID with a '.',
		// '>', '*' or ' ' would publish to a subject the consumer never matches.
		subject := pendingSubjectPrefix + sanitizePendingSubjectToken(d.ID)
		if _, pubErr := js.Publish(ctx, subject, envelope); pubErr != nil {
			logging.Warn("EnqueueDaemonCommand: JetStream publish failed",
				"userID", userID, "daemonID", d.ID, "subject", subject,
				"commandType", commandType, "error", pubErr)
			continue
		}
		enqueued++
	}
	return enqueued, nil
}

// jetstream returns the lazily-initialized JetStream context. Safe to call
// from multiple goroutines; sync.Once guarantees a single init.
func (r *NATSDaemonRouter) jetstream() (jetstream.JetStream, error) {
	r.jsOnce.Do(func() {
		if r.nc == nil {
			r.jsErr = fmt.Errorf("nats connection is nil")
			return
		}
		r.js, r.jsErr = jetstream.New(r.nc)
	})
	return r.js, r.jsErr
}

// Compile-time interface check.
var _ DaemonRouter = (*NATSDaemonRouter)(nil)
