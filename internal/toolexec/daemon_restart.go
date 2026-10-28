package toolexec

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// DaemonDrainTimeout is the owner's approved upper bound for planned drains.
const DaemonDrainTimeout = 10 * time.Minute

// DaemonRestartBudget bounds waiting for replacement after a drain. It matches
// the ordinary reconnect grace, not the fleet rollout health-gate window.
const DaemonRestartBudget = DaemonReconnectGrace

const daemonRestartSubject = "daemon.restart"

// ErrDaemonUpdating is authoritative non-delivery: the gateway refused the
// request BEFORE queueing it on the daemon's stream. Safe to re-send, shell included.
var ErrDaemonUpdating = errDaemonNotConnected

// ErrorCodeDaemonUndelivered crosses the bridge for the admission race: a NATS
// callback selected before unsubscribe may only run after the drain began.
const ErrorCodeDaemonUndelivered = "DAEMON_UNDELIVERED"

// RequestDaemonRestart is the operator/admin transport, not rollout policy.
// Only the replica holding this daemon's stream subscribes to this subject.
func (r *NATSDaemonRouter) RequestDaemonRestart(ctx context.Context, userID, daemonID string, req *pb.RestartRequested) (*pb.DaemonRestartStatus, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	msg, err := r.nc.RequestWithContext(ctx, daemonSubject(daemonRestartSubject, userID, daemonID), data)
	if err != nil {
		return nil, daemonRequestError("request daemon restart", err)
	}
	var out struct {
		Status *pb.DaemonRestartStatus `json:"status"`
		Error  string                  `json:"error"`
	}
	if err := json.Unmarshal(msg.Data, &out); err != nil {
		return nil, err
	}
	if out.Error != "" || out.Status == nil {
		return nil, fmt.Errorf("request daemon restart: %s", out.Error)
	}
	return out.Status, nil
}

// restartDeadline reads the persisted observation, so a worker that started
// after the notice (or missed a plain NATS event) still gets the full grace.
func (r *NATSDaemonRouter) restartDeadline(ctx context.Context, userID, daemonID string) time.Time {
	if r.db == nil {
		return time.Time{}
	}
	rows, err := r.db.ListDaemonsByUserID(ctx, userID)
	if err != nil {
		return time.Time{}
	}
	for _, row := range rows {
		if row.ID != daemonID || row.UserID != userID {
			continue
		}
		var status pb.DaemonRestartStatus
		if json.Unmarshal([]byte(row.RestartStatus), &status) == nil && status.DeadlineUnixMs > 0 {
			return time.UnixMilli(status.DeadlineUnixMs).Add(DaemonRestartBudget)
		}
	}
	return time.Time{}
}

func (r *NATSDaemonRouter) reconnectDeadline(ctx context.Context, userID, daemonID string, ordinary time.Time) time.Time {
	return later(ordinary, r.restartDeadline(ctx, userID, daemonID))
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
