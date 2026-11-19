// Copyright (c) 2026 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"google.golang.org/protobuf/encoding/protojson"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/logging"
)

// The drain protocol, from the senders' side (RestartRequested; the daemon's
// half is daemonruntime/upgrade_drain.go).
//
// A daemon draining for a restart takes no new tool calls. Its gateway stops
// routing them (the daemon reports UPDATING or DRAINED on its heartbeat) and
// the daemon itself refuses any that still reach it; both answer
// ErrorCodeDaemonUpdating. Nothing ran, so the router treats the call like one
// that found no daemon at all — undelivered, safe to send again — and waits
// for the daemon to come back (rideOutDisconnect). Because a drain can last
// ten minutes, longer than DaemonReconnectGrace, that wait runs to the drain's
// deadline plus DaemonRestartBudget, which the gateway reports on the
// daemon's tools.online subject.

// ErrDaemonUpdating means the machine's daemon is restarting for an update the
// platform asked for, and did not take the request: it is draining, or has
// drained and is waiting to restart. Nothing ran, so asking again once the
// daemon is back is always safe.
//
// It is the not-connected state as far as new tool calls go, and says so the
// same way errDaemonNotConnected does: a USER-class Unavailable, with the
// daemonoffline marker in its text and reason errreason.DaemonNotConnected. So
// a call that outwaits even the drain and the restart fails into the
// workflow's daemon-offline pause, which resumes the run when the machine
// connects, exactly as for a machine that went away.
var ErrDaemonUpdating = svcerr.WithReason(svcerr.WithClass(svcerr.Unavailable(
	"no daemon connected for new tool calls: your machine is restarting for an update; they run once it is back"),
	svcerr.ClassUser), errreason.DaemonNotConnected)

// IsDaemonUpdating reports whether err is ErrDaemonUpdating.
func IsDaemonUpdating(err error) bool {
	return errors.Is(err, ErrDaemonUpdating)
}

// DaemonRestartBudget is how long past its drain deadline a restarting daemon
// is still waited for: a drained managed daemon waits up to 5 minutes for its
// pod to be rolled before it restarts in place, and then has to come back up
// and redial (DaemonReconnectGrace covers that last part).
const DaemonRestartBudget = 5*time.Minute + DaemonReconnectGrace

// onlineReplyUpdating is the tools.online answer for a daemon that is
// connected but draining for a restart. A router that predates it compares
// the answer to "true", so it reads the daemon as not answering, which is
// what it is for a tool call.
type onlineReplyUpdating struct {
	UpdatingUntilUnixMs int64 `json:"updating_until_unix_ms"`
}

// EncodeOnlineReplyUpdating is the tools.online answer for a daemon draining
// until deadline.
func EncodeOnlineReplyUpdating(deadline time.Time) []byte {
	data, _ := json.Marshal(onlineReplyUpdating{UpdatingUntilUnixMs: deadline.UnixMilli()})
	return data
}

// parseOnlineReply reads a tools.online answer: online for "true"; not online
// and draining until updatingUntil for an updating answer; neither for
// anything else.
func parseOnlineReply(data []byte) (online bool, updatingUntil time.Time) {
	if string(data) == "true" {
		return true, time.Time{}
	}
	var u onlineReplyUpdating
	if json.Unmarshal(data, &u) == nil && u.UpdatingUntilUnixMs > 0 {
		return false, time.UnixMilli(u.UpdatingUntilUnixMs)
	}
	return false, time.Time{}
}

// RequestRestartCommand is the daemon.command type that asks a gateway to send
// RestartRequested to the daemon the command's subject names. The gateway
// answers it itself; it never reaches the daemon. Its payload is the
// RestartRequested, protojson-encoded.
const RequestRestartCommand = "gateway.request_restart"

// ErrRestartNotSupported is the gateway's answer to RequestRestartCommand for
// a daemon that did not declare drains_on_restart_request: it predates the
// drain protocol, and would ignore the request.
var ErrRestartNotSupported = svcerr.FailedPrecondition(
	"this machine's daemon predates the drain protocol and cannot be asked to restart; it updates when it next restarts")

// RequestDaemonRestart asks the gateway holding daemonID's stream to send it
// RestartRequested. It fails when the daemon is not connected, or predates the
// drain protocol (the error then names ErrRestartNotSupported's text). It
// does not wait for the drain: the daemon reports its progress on its
// heartbeat.
func (r *NATSDaemonRouter) RequestDaemonRestart(ctx context.Context, userID, daemonID string, req *reliantv1.RestartRequested) error {
	if strings.TrimSpace(daemonID) == "" {
		return svcerr.InvalidArgument("a restart names one daemon")
	}
	payload, err := protojson.Marshal(req)
	if err != nil {
		return fmt.Errorf("encoding the restart request: %w", err)
	}
	data, requestID, err := r.buildDaemonCommand(ctx, RequestRestartCommand, payload, 0)
	if err != nil {
		return err
	}
	if _, err := r.sendDaemonCommandData(ctx, userID, daemonID, RequestRestartCommand, data, requestID, 0); err != nil {
		return fmt.Errorf("asking daemon %s to restart: %w", daemonID, err)
	}
	return nil
}

// reconnectDeadline is how long a call waits for a daemon last seen at seen:
// DaemonReconnectGrace from then, or, for a daemon draining for a restart
// until updatingUntil, DaemonRestartBudget past its drain, whichever is later.
func reconnectDeadline(seen, updatingUntil time.Time) time.Time {
	deadline := seen.Add(DaemonReconnectGrace)
	if !updatingUntil.IsZero() {
		if restartBy := updatingUntil.Add(DaemonRestartBudget); restartBy.After(deadline) {
			deadline = restartBy
		}
	}
	return deadline
}

// daemonUpdatingUntil is when daemonID's drain ends, as its gateway reports
// it on tools.online; zero when it is not draining, or nothing answered.
func (r *NATSDaemonRouter) daemonUpdatingUntil(ctx context.Context, userID, daemonID string) time.Time {
	if r.nc == nil {
		return time.Time{}
	}
	probeCtx, cancel := context.WithTimeout(ctx, reconnectProbeTimeout)
	defer cancel()
	msg, err := r.nc.RequestWithContext(probeCtx, daemonSubject(toolOnlineSubject, userID, daemonID), nil)
	if err != nil {
		return time.Time{}
	}
	_, until := parseOnlineReply(msg.Data)
	return until
}

// DaemonUpgradeReporter is optional on a DaemonConnectionManager: it says
// whether a held daemon is draining for a restart, and until when.
// *tools_daemon.Service implements it.
type DaemonUpgradeReporter interface {
	DaemonUpdatingUntil(userID, daemonID string) (until time.Time, updating bool)
}

// DaemonRestartRequester is optional on a DaemonConnectionManager: it sends
// RestartRequested to a held daemon. *tools_daemon.Service implements it.
type DaemonRestartRequester interface {
	RequestDaemonRestart(userID, daemonID string, req *reliantv1.RestartRequested) error
}

// respondRequestRestart answers RequestRestartCommand on the gateway, without
// involving the daemon beyond sending it the request.
func (b *NATSToolBridge) respondRequestRestart(ctx context.Context, msg *nats.Msg, userID, daemonID string, req *daemonCommandWire) {
	respond := func(err error) {
		body := map[string]any{"success": err == nil}
		if err != nil {
			body["error_message"] = err.Error()
		}
		data, _ := json.Marshal(body)
		_ = msg.Respond(data)
	}
	requester, ok := b.mgr.(DaemonRestartRequester)
	if !ok {
		respond(errors.New("this gateway cannot ask a daemon to restart"))
		return
	}
	var restart reliantv1.RestartRequested
	if err := protojson.Unmarshal(req.Payload, &restart); err != nil {
		respond(fmt.Errorf("invalid restart request: %w", err))
		return
	}
	err := requester.RequestDaemonRestart(userID, daemonID, &restart)
	if err != nil {
		logging.WarnContext(ctx, "[NATSToolBridge] Could not ask a daemon to restart",
			"userID", userID, "daemonID", daemonID, "rollout_id", restart.GetRolloutId(), "error", err)
	} else {
		logging.InfoContext(ctx, "[NATSToolBridge] Asked a daemon to restart",
			"userID", userID, "daemonID", daemonID, "rollout_id", restart.GetRolloutId(),
			"target_version", restart.GetTargetVersion(), "urgency", restart.GetUrgency().String())
	}
	respond(err)
}
