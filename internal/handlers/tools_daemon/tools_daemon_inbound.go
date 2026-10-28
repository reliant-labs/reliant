package tools_daemon

import (
	"context"
	"fmt"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// receivedMessage carries one Receive() outcome from the reader goroutine.
type receivedMessage struct {
	msg *reliantv1.DaemonMessage
	err error
}

// inboundStateQueueSize bounds how many state-persisting messages from one
// daemon may wait behind a slow write before the receive loop stops reading
// (backpressure onto the daemon's stream).
const inboundStateQueueSize = 128

// handleIncoming reads one daemon's stream for the life of the connection.
//
// Receive runs in its own goroutine so the loop can also select on conn.done.
// Checking done only between messages meant a connection closed by the
// replace path or the stale sweeper kept its handler parked in Receive until
// the daemon's next heartbeat — up to a full heartbeat interval during which
// the stream was closed on the gateway's books but still open on the wire, and
// the daemon learned nothing about WHY it ended (it saw a bare EOF).
//
// It splits what the daemon sends into two classes. REPLIES — command and
// tool responses, terminal/process/local-model output — are routed to their
// waiters right here, and routing is a map lookup and a channel send. STATE —
// heartbeat lease renewals, project discovery, config snapshots, filesystem
// and failure announcements — is written to the database, and that runs on a
// per-connection worker, in arrival order, so a slow write never sits in front
// of a reply.
//
// Before the split, every message was handled inline in arrival order, so a
// reply waited for any DB write the daemon had sent ahead of it: a 15s
// heartbeat, an agent's FileSystemChanged, or a project-config snapshot that
// reaches 10MB for a multi-repo project (on reconnect the gateway persisted 16
// of them back to back, ~1s). A millisecond daemon command could take as long
// as the slowest unrelated write queued in front of its reply.
func (s *Service) handleIncoming(ctx context.Context, conn *daemonConnection) error {
	stateQueue := make(chan *reliantv1.DaemonMessage, inboundStateQueueSize)
	streamEnded := make(chan struct{})
	// Detached from the stream's context so state the gateway already
	// received is still applied after the stream ends — a DaemonCommandFailed
	// is the only trace a fire-and-forget clone failure ever leaves.
	go s.runInboundStateWorker(context.WithoutCancel(ctx), conn, stateQueue, streamEnded)
	// The lease renewer has its own goroutine and a one-slot, coalescing
	// signal, so the lease reflects THIS loop reading heartbeats — not how
	// far the state worker has got through its queue. Renewal used to ride
	// the state FIFO, so a backlog of slow state writes aged a live stream's
	// lease out from under it.
	leaseRenew := make(chan struct{}, 1)
	go s.runLeaseRenewer(context.WithoutCancel(ctx), conn, leaseRenew, streamEnded)
	defer func() {
		close(streamEnded)
		close(stateQueue)
		close(leaseRenew)
	}()
	var lastQueueFullLog time.Time

	// The reader is joined before this returns, however it returns: the
	// stream is released (aborted, so a Receive parked on a daemon that is
	// still connected returns; the sender stopped) and then the reader is
	// waited for. Returning with the reader still inside Receive let it use
	// the stream after ConnectDaemon's handler had returned — a data race
	// with otelconnect finishing the stream's span, which Connect forbids.
	// Deferred after the queue closes above, so it runs before them.
	received := make(chan receivedMessage)
	stopReader := make(chan struct{})
	readerDone := make(chan struct{})
	defer func() {
		close(stopReader)
		conn.releaseStream()
		<-readerDone
	}()
	go func() {
		defer close(readerDone)
		for {
			msg, err := conn.stream.Receive()
			select {
			case received <- receivedMessage{msg: msg, err: err}:
			case <-stopReader:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		var msg *reliantv1.DaemonMessage
		select {
		case <-ctx.Done():
			return nil
		case <-conn.done:
			// closedReason is nil for an ordinary teardown and
			// errDaemonSuperseded when another process claimed this daemon id.
			// Returning it hands the daemon a status code it can act on.
			return conn.closedReason()
		case r := <-received:
			if r.err != nil {
				if conn.drainNoticeSent.Load() {
					// The daemon moved off this draining replica and
					// closed the old stream: the drain's expected end.
					return errGatewayDraining
				}
				return r.err
			}
			msg = r.msg
		}
		if msg == nil {
			continue
		}

		now := time.Now().UTC()
		s.mu.Lock()
		conn.lastActivity = now
		conn.lastReceived = now
		s.mu.Unlock()
		// conn.lastReceived (above) feeds the dead-connection sweeper and MUST
		// bump on every message, heartbeats included. It is NOT the activity
		// that keeps a cloud machine from idle-suspending: nothing the daemon
		// sends is. Its replies, output and notices arrive whether or not
		// anyone is there — a reply to the reconciler's 30s bg_status poll
		// kept an unused machine up for hours — so that activity is published
		// on the way OUT, for work someone asked for (runSender).
		if hb, isHeartbeat := msg.Message.(*reliantv1.DaemonMessage_Heartbeat); isHeartbeat {
			// Read off the stream just now: acknowledge it to the daemon
			// (runHeartbeat echoes this) and renew the lease. Both happen
			// here, at the read, because what they vouch for is that this
			// loop is consuming the stream.
			conn.recordDaemonHeartbeat(hb.Heartbeat.GetTimestamp())
			conn.daemonToolCallsInFlight.Store(hb.Heartbeat.GetToolCallsInFlight())
			select {
			case leaseRenew <- struct{}{}:
			default: // a renewal is already pending; it covers this one
			}
		}

		if status := msg.GetRestartStatus(); status != nil {
			if err := s.recordRestart(ctx, conn, status); err != nil {
				return err
			}
			continue
		}
		if tr := msg.GetToolResponse(); tr != nil {
			s.routeToolResponse(conn, tr)
			continue
		}
		if conn.routeInboundReply(msg) {
			continue
		}
		select {
		case stateQueue <- msg:
			continue
		default:
		}
		// The state worker is behind by a full queue, so this loop is about
		// to stop reading the stream: every reply from this daemon now waits
		// for a database write. Said out loud, at most once a minute, because
		// a silent stall here is indistinguishable from a dead daemon.
		if time.Since(lastQueueFullLog) >= time.Minute {
			lastQueueFullLog = time.Now()
			logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Inbound state queue full; stream reads are blocked until the state worker catches up",
				"daemonID", conn.daemonID, "userID", conn.userID, "queued", len(stateQueue))
		}
		select {
		case stateQueue <- msg:
		case <-conn.done:
			return conn.closedReason()
		case <-ctx.Done():
			return nil
		}
	}
}

// recordDaemonHeartbeat notes the newest daemon heartbeat the receive loop
// has read. Called only from the receive loop, so a plain compare-and-store is
// race-free against itself; runHeartbeat only loads.
func (c *daemonConnection) recordDaemonHeartbeat(timestamp int64) {
	if timestamp > c.ackedDaemonHeartbeat.Load() {
		c.ackedDaemonHeartbeat.Store(timestamp)
	}
}

// runLeaseRenewer renews this connection's daemon_attachment lease each time
// the receive loop signals that it read a heartbeat. It exits when the
// receive loop closes renew.
func (s *Service) runLeaseRenewer(ctx context.Context, conn *daemonConnection, renew <-chan struct{}, streamEnded <-chan struct{}) {
	for range renew {
		s.renewLease(ctx, conn, streamEnded)
	}
}

// renewLease writes "this stream is alive as of now" to daemon_attachment,
// re-creating the row if it went missing while the stream lived
// (RenewDaemonAttachment): the TTL reaper expired it during an inbound stall,
// or the connect-time upsert never landed.
//
// A renewal pending when the stream ends is dropped: it would tell the UI the
// daemon is up after it has gone. The check and the write happen under
// leaseMu, which teardownConnection also takes around its release, so a
// renewal can never land after the row was deliberately removed.
func (s *Service) renewLease(ctx context.Context, conn *daemonConnection, streamEnded <-chan struct{}) {
	if s.deps.Repo == nil {
		return
	}
	// Its own goroutine, like the state worker: an unrecovered panic here
	// would take down the whole gateway rather than one renewal.
	defer func() {
		if r := recover(); r != nil {
			logging.ErrorContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Panic renewing daemon reachability lease",
				"panic", r, "daemonID", conn.daemonID)
		}
	}()
	created, err := func() (bool, error) {
		conn.leaseMu.Lock()
		defer conn.leaseMu.Unlock()
		select {
		case <-streamEnded:
			return false, nil
		case <-conn.done:
			return false, nil
		default:
		}
		renewCtx, cancel := context.WithTimeout(ctx, leaseRenewTimeout)
		defer cancel()
		return s.deps.Repo.RenewDaemonAttachment(renewCtx, &db.DaemonAttachment{
			DaemonID:           conn.daemonID,
			UserID:             conn.userID,
			Source:             conn.attachSource,
			PodIP:              conn.podIP,
			PodPort:            conn.podPort,
			AttachedAt:         conn.attachedAt,
			LastStreamActivity: leaseTime(time.Now()),
			Build:              conn.build,
		})
	}()
	if err != nil {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Failed to renew daemon reachability lease", "error", err, "daemonID", conn.daemonID)
		return
	}
	if !created {
		return
	}
	// Until now the registry reported this live daemon as detached, and its
	// owner's clients are showing that. Repairing the row is not enough:
	// nothing else would tell them to re-read before their slow fallback
	// poll. The fresh row also lost the detected ports the state worker
	// believes it already wrote.
	logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Re-created a missing reachability lease for a live daemon stream",
		"daemonID", conn.daemonID, "userID", conn.userID, "attachedAt", conn.attachedAt)
	conn.leaseRecreated.Store(true)
	s.publishDaemonListChanged(conn.userID, conn.daemonID)
}

// runInboundStateWorker applies one connection's state messages in the order
// the daemon sent them. It drains whatever the receive loop queued, including
// after the stream ends, and exits when the queue is closed.
func (s *Service) runInboundStateWorker(ctx context.Context, conn *daemonConnection, queue <-chan *reliantv1.DaemonMessage, streamEnded <-chan struct{}) {
	for msg := range queue {
		s.applyInboundState(ctx, conn, msg, streamEnded)
	}
}

// applyInboundState handles one state message. A panic is contained to the
// message: this runs on its own goroutine, where an unrecovered panic would
// take down the whole gateway rather than one stream.
func (s *Service) applyInboundState(ctx context.Context, conn *daemonConnection, msg *reliantv1.DaemonMessage, streamEnded <-chan struct{}) {
	defer func() {
		if r := recover(); r != nil {
			logging.ErrorContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Panic applying daemon state message",
				"panic", r, "daemonID", conn.daemonID, "messageType", fmt.Sprintf("%T", msg.GetMessage()))
		}
	}()
	// One message's writes may not hold the worker indefinitely: everything
	// queued behind it, and once the queue is full every reply on the stream,
	// waits for them (inboundStateApplyTimeout).
	ctx, cancel := context.WithTimeout(ctx, inboundStateApplyTimeout)
	defer cancel()

	switch m := msg.Message.(type) {
	case *reliantv1.DaemonMessage_Heartbeat:
		// A heartbeat says the daemon is alive NOW. One that was still
		// queued when the stream ended would renew the lease and tell the UI
		// the daemon is up after it has gone, so it is dropped.
		select {
		case <-streamEnded:
			return
		default:
		}
		s.publishDaemonHeartbeat(ctx, conn.userID, conn.daemonID, time.Now().UTC(), m.Heartbeat)
		// The reachability lease (last_stream_activity) is NOT renewed here:
		// the receive loop renews it the moment it reads the heartbeat
		// (renewLease), so a backlog in this FIFO cannot age out a live
		// stream. The gateway writes daemon_attachment directly rather than
		// publishing a daemonstate event: a dedicated EventHeartbeat would land
		// on the shared daemon.v1.state.* stream, whose authoritative consumer
		// lives in the control-plane repo and STRICTLY rejects unknown event
		// types, and it must never feed the workspace idle-suspend timer
		// (which keys off EventActivity).
		//
		// The heartbeat announced above may reach a client before the
		// renewer has re-created a missing lease; the renewer announces the
		// re-creation itself, so the client's re-read is not lost to that
		// race.
		//
		// Persist heartbeat-carried workspace memory telemetry on the
		// attachment record so the daemon registry (and the UI behind it)
		// can surface memory pressure. limit==0 means the daemon has no
		// cgroup accounting (local/mac) — nothing to record.
		if hb := m.Heartbeat; hb != nil && hb.MemoryLimitBytes > 0 {
			if err := s.deps.Repo.UpdateDaemonAttachmentMemory(ctx, conn.daemonID,
				int64(hb.MemoryUsedBytes), int64(hb.MemoryLimitBytes), hb.MemoryPressure); err != nil {
				logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Failed to record daemon memory telemetry", "error", err, "daemonID", conn.daemonID)
			}
		}
		// Persist the heartbeat's disk report on the daemon row, where the
		// registry and the storage inbox item read it.
		if hb := m.Heartbeat; hb != nil && hb.Disk != nil && len(hb.Disk.Volumes) > 0 {
			s.recordDiskReport(ctx, conn, hb.Disk)
		}
		// Persist heartbeat-carried detected listener ports on the
		// attachment record (same flow as the memory telemetry above) so
		// the daemon registry can surface preview affordances. Written
		// only when the set changed — heartbeats are 15s apart but port
		// churn is rare. UpsertDaemonAttachment resets the column on
		// re-attach, matching the empty initial cache key here.
		if hb := m.Heartbeat; hb != nil {
			if conn.leaseRecreated.Swap(false) {
				// The renewer re-created the row with the column default:
				// whatever this cache says was written is gone.
				conn.lastDetectedPortsKey = ""
			}
			portsKey := fmt.Sprint(hb.DetectedPorts)
			if portsKey != conn.lastDetectedPortsKey {
				if err := s.deps.Repo.UpdateDaemonAttachmentPorts(ctx, conn.daemonID, hb.DetectedPorts); err != nil {
					logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Failed to record daemon detected ports", "error", err, "daemonID", conn.daemonID)
				} else {
					conn.lastDetectedPortsKey = portsKey
				}
			}
		}

	case *reliantv1.DaemonMessage_ProjectDiscovery:
		if err := s.handleProjectDiscovery(ctx, conn, m.ProjectDiscovery); err != nil {
			logging.ErrorContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Failed to handle project discovery", "error", err, "daemonID", conn.daemonID)
		}

	case *reliantv1.DaemonMessage_LoadProjectConfigsResponse:
		if err := s.handleLoadProjectConfigsResponse(ctx, conn, m.LoadProjectConfigsResponse); err != nil {
			logging.ErrorContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Failed to handle load project configs response", "error", err, "daemonID", conn.daemonID)
		}

	case *reliantv1.DaemonMessage_ProjectConfigDelta:
		if err := s.handleProjectConfigDelta(ctx, conn, m.ProjectConfigDelta); err != nil {
			logging.ErrorContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Failed to handle project config delta", "error", err, "daemonID", conn.daemonID)
		}

	case *reliantv1.DaemonMessage_LocalModelInventory:
		s.handleLocalModelInventory(ctx, conn, m.LocalModelInventory)

	case *reliantv1.DaemonMessage_FileSystemChanged:
		if err := s.handleFileSystemChanged(ctx, conn, m.FileSystemChanged); err != nil {
			logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Failed to handle filesystem changed", "error", err)
		}

	case *reliantv1.DaemonMessage_DaemonCommandFailed:
		if err := s.handleDaemonCommandFailed(ctx, conn, m.DaemonCommandFailed); err != nil {
			logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Failed to handle daemon command failure", "error", err)
		}

	default:
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Unknown message type", "userID", conn.userID)
	}
}

// routeInboundReply delivers a message that answers something already in
// flight — a command or tool response, a chunk of streamed output — and
// reports whether msg was one. It never touches the database, which is what
// lets the receive loop run it inline without making a reply wait on a write.
//
// Tool responses are not routed here: they may answer a request this stream
// never carried (see Service.routeToolResponse).
func (c *daemonConnection) routeInboundReply(msg *reliantv1.DaemonMessage) bool {
	switch m := msg.Message.(type) {
	case *reliantv1.DaemonMessage_DaemonCommandResponse:
		if resp := m.DaemonCommandResponse; resp != nil {
			c.pendingCommandsMu.Lock()
			ch, ok := c.pendingCommands[resp.RequestId]
			if ok {
				delete(c.pendingCommands, resp.RequestId)
			}
			c.pendingCommandsMu.Unlock()
			if ok {
				ch <- resp
			} else {
				logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Received daemon command response with no pending request",
					"requestID", resp.RequestId, "commandType", resp.CommandType)
			}
		}

	case *reliantv1.DaemonMessage_KillProcessResponse:
		if resp := m.KillProcessResponse; resp != nil {
			if resp.Success {
				logging.Debug(LOG_PREFIX_TOOLS_DAEMON+" Kill process succeeded", "processID", resp.ProcessId)
			} else {
				logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Kill process failed", "processID", resp.ProcessId, "error", resp.ErrorMessage)
			}
		}

	case *reliantv1.DaemonMessage_TerminalOutput:
		if out := m.TerminalOutput; out != nil {
			c.dispatchTerminalEvent(&toolexec.TerminalOutputEvent{
				SessionID: out.GetSessionId(),
				Data:      out.GetData(),
			})
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
			c.dispatchTerminalEvent(outEvt)
		}

	case *reliantv1.DaemonMessage_ProcessOutputChunk:
		if chunk := m.ProcessOutputChunk; chunk != nil {
			c.dispatchProcessOutputEvent(&toolexec.ProcessOutputEvent{
				ProcessID:  chunk.GetProcessId(),
				Data:       chunk.GetData(),
				Stream:     chunk.GetStream(),
				Sequence:   chunk.GetSequence(),
				IsComplete: chunk.GetIsComplete(),
				ExitCode:   chunk.GetExitCode(),
			})
		}

	case *reliantv1.DaemonMessage_LocalModelHttpChunk:
		c.dispatchLocalModelChunk(m.LocalModelHttpChunk)

	default:
		return false
	}
	return true
}
