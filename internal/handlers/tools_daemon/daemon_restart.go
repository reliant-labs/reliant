package tools_daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// RequestDaemonRestart is the manual/operator hook, with no policy selection.
// Repeated requests cannot postpone a running drain. Like handOver, the bridge
// unsubscribes and flushes before the daemon sees the notice. Unlike handOver,
// it does not mark the entire gateway replica as draining.
func (s *Service) RequestDaemonRestart(ctx context.Context, userID, daemonID string, req *pb.RestartRequested) (*pb.DaemonRestartStatus, error) {
	conn, err := s.connForUser(userID, daemonID)
	if err != nil {
		return nil, err
	}
	if req == nil || strings.TrimSpace(req.Reason) == "" {
		return nil, svcerr.InvalidArgument("restart reason is required")
	}
	conn.admissionMu.Lock()
	defer conn.admissionMu.Unlock()
	if _, err := uuid.Parse(conn.incarnationID); err != nil {
		return nil, svcerr.FailedPrecondition("daemon process identity is required for planned restart")
	}
	if status := conn.restart.Load(); status != nil {
		if status.DeadlineUnixMs == 0 {
			return nil, svcerr.FailedPrecondition("daemon registration is not ready")
		}
		if err := s.persistRestart(ctx, conn, status); err != nil {
			return nil, err
		}
		if !status.Drained {
			err := s.sendControlToConn(conn, &pb.ServerMessage{Message: &pb.ServerMessage_RestartRequested{RestartRequested: &pb.RestartRequested{DeadlineUnixMs: status.DeadlineUnixMs, Reason: status.Reason}}})
			return status, err
		}
		return status, nil
	}
	deadline := time.Now().Add(toolexec.DaemonDrainTimeout)
	if req.DeadlineUnixMs > 0 {
		deadline = minTime(deadline, time.UnixMilli(req.DeadlineUnixMs))
	}
	status := &pb.DaemonRestartStatus{DeadlineUnixMs: deadline.UnixMilli(), Reason: req.Reason, IncarnationId: conn.incarnationID}
	if err := s.persistRestart(ctx, conn, status); err != nil {
		return nil, err
	}
	conn.restart.Store(status)
	s.notifyUpdating(userID, daemonID)
	notice := &pb.ServerMessage{Message: &pb.ServerMessage_RestartRequested{RestartRequested: &pb.RestartRequested{DeadlineUnixMs: status.DeadlineUnixMs, Reason: status.Reason}}}
	// New work is excluded under admissionMu; the notice itself is control traffic.
	if err := s.sendControlToConn(conn, notice); err != nil {
		return status, err
	}
	s.publishDaemonListChanged(userID, daemonID)
	return status, nil
}

// notifyUpdating leaves a gateway hand-over's administrative subscriptions
// distinct from a daemon drain's: the former must move all request routing.
func (s *Service) notifyUpdating(userID, daemonID string) {
	s.listenersMu.RLock()
	defer s.listenersMu.RUnlock()
	for _, listener := range s.listeners {
		if l, ok := listener.(interface{ OnDaemonUpdating(string, string) }); ok {
			l.OnDaemonUpdating(userID, daemonID)
		} else if l, ok := listener.(DaemonDrainListener); ok {
			l.OnDaemonDraining(userID, daemonID)
		}
	}
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func (s *Service) persistRestart(ctx context.Context, conn *daemonConnection, status *pb.DaemonRestartStatus) error {
	if s.deps.Repo == nil {
		return nil
	}
	data := ""
	if status != nil {
		b, err := json.Marshal(status)
		if err != nil {
			return err
		}
		data = string(b)
	}
	return s.deps.Repo.SetDaemonRestartStatus(ctx, conn.userID, conn.daemonID, conn.attachedAt, data)
}

// restoreRestart runs before announcing new-work availability. A nil local
// observation never proves replacement: durable intent targets a process UUID,
// not the stream/lease, and is resent when its original notice was lost.
func (s *Service) restoreRestart(ctx context.Context, conn *daemonConnection, reg *pb.DaemonRegister) error {
	conn.admissionMu.Lock()
	defer conn.admissionMu.Unlock()
	conn.incarnationID = reg.GetIncarnationId()
	if _, err := uuid.Parse(conn.incarnationID); err != nil {
		return svcerr.FailedPrecondition("daemon process identity is required")
	}
	var previous string
	var status *pb.DaemonRestartStatus
	if s.deps.Repo != nil {
		row, err := s.deps.Repo.GetDaemon(ctx, conn.daemonID)
		if err != nil {
			return err
		}
		if row == nil || row.UserID != conn.userID {
			return svcerr.NotFound("daemon not found")
		}
		previous = row.RestartStatus
		if previous != "" {
			status = &pb.DaemonRestartStatus{}
			if err := json.Unmarshal([]byte(previous), status); err != nil {
				return fmt.Errorf("decode restart intent: %w", err)
			}
			if _, err := uuid.Parse(status.IncarnationId); err != nil {
				return svcerr.FailedPrecondition("restart intent has no verified process identity")
			}
			if status.IncarnationId != conn.incarnationID {
				status = nil
			}
		}
	}
	if local := reg.GetRestartStatus(); local != nil {
		if local.IncarnationId != conn.incarnationID {
			return svcerr.FailedPrecondition("restart observation belongs to another process")
		}
		if status != nil && local.DeadlineUnixMs == status.DeadlineUnixMs {
			status = proto.Clone(status).(*pb.DaemonRestartStatus)
			status.Drained = status.Drained || local.Drained
			status.TimedOut = status.TimedOut || local.TimedOut
		} else if previous == "" {
			status = proto.Clone(local).(*pb.DaemonRestartStatus)
		}
	}
	data := ""
	if status != nil {
		b, err := json.Marshal(status)
		if err != nil {
			return err
		}
		data = string(b)
	}
	if s.deps.Repo != nil {
		if err := s.deps.Repo.CompareDaemonRestartStatus(ctx, conn.userID, conn.daemonID, conn.attachedAt, previous, data); err != nil {
			return err
		}
	}
	conn.restart.Store(status)
	if status != nil {
		return s.sendControlToConn(conn, &pb.ServerMessage{Message: &pb.ServerMessage_RestartRequested{RestartRequested: &pb.RestartRequested{DeadlineUnixMs: status.DeadlineUnixMs, Reason: status.Reason}}})
	}
	return nil
}

// recordRestart runs on the receive loop so a drained report is not lost to
// disconnect overtaking a queued state write. It is a rare bounded write.
func (s *Service) recordRestart(ctx context.Context, conn *daemonConnection, status *pb.DaemonRestartStatus) error {
	conn.admissionMu.Lock()
	defer conn.admissionMu.Unlock()
	prev := conn.restart.Load()
	if prev == nil || status == nil || status.DeadlineUnixMs != prev.DeadlineUnixMs || status.IncarnationId != conn.incarnationID || prev.IncarnationId != conn.incarnationID {
		return nil
	}
	if prev.Drained && !status.Drained {
		status = prev // never regress, but re-persist after lease recreation
	}
	ctx, cancel := context.WithTimeout(ctx, inboundStateApplyTimeout)
	defer cancel()
	if err := s.persistRestart(ctx, conn, status); err != nil {
		return err
	}
	conn.restart.Store(status)
	s.publishDaemonListChanged(conn.userID, conn.daemonID)
	return nil
}

// requestNeedsAdmission excludes control traffic: results/acks, heartbeat,
// cancellation and tool.status must still cross a draining stream.
func requestNeedsAdmission(msg *pb.ServerMessage) bool {
	switch m := msg.GetMessage().(type) {
	case *pb.ServerMessage_ToolRequest, *pb.ServerMessage_LocalModelHttpRequest, *pb.ServerMessage_TerminalInput:
		return true
	case *pb.ServerMessage_DaemonCommand:
		return m.DaemonCommand.GetCommandType() != toolexec.ToolStatusCommand
	default:
		return false
	}
}
