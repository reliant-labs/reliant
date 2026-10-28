package daemonruntime

import (
	"context"
	"time"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
	"google.golang.org/protobuf/proto"
)

func (d *daemonClient) restartSnapshot() *pb.DaemonRestartStatus {
	d.restartMu.Lock()
	defer d.restartMu.Unlock()
	if d.restartStatus == nil {
		return nil
	}
	return proto.Clone(d.restartStatus).(*pb.DaemonRestartStatus)
}

// beginRestart is event-driven: completions and acks advance it, and exactly
// one timer bounds it. Background processes/PTYs never participate in the count.
// It reports drained and stays quiesced: replacing the process/pod belongs to
// its supervisor (a container restart alone cannot deliver a new image).
func (d *daemonClient) beginRestart(_ context.Context, notice *pb.RestartRequested) {
	if notice == nil {
		return
	}
	d.restartMu.Lock()
	if d.restartStatus != nil {
		d.publishRestartLocked()
		d.restartMu.Unlock()
		return
	}
	deadline := time.Now().Add(toolexec.DaemonDrainTimeout)
	if notice.DeadlineUnixMs > 0 && time.UnixMilli(notice.DeadlineUnixMs).Before(deadline) {
		deadline = time.UnixMilli(notice.DeadlineUnixMs)
	}
	d.restartStatus = &pb.DaemonRestartStatus{DeadlineUnixMs: deadline.UnixMilli(), Reason: notice.Reason, IncarnationId: d.incarnationID}
	d.updating.Store(true)
	d.recordStream(daemonstate.StreamUpdating, notice.Reason)
	d.publishRestartLocked()
	after := d.restartAfter
	if after == nil {
		after = func(wait time.Duration, fn func()) func() bool { return time.AfterFunc(wait, fn).Stop }
	}
	d.restartStop = after(time.Until(deadline), d.restartExpired)
	d.restartProgressLocked()
	d.restartMu.Unlock()
	// The timer must not outlive its daemon. A session context is too short:
	// moving between gateways must not cancel a process-wide drain.
}

func (d *daemonClient) publishRestartLocked() {
	if d.restartStatus == nil {
		return
	}
	status := proto.Clone(d.restartStatus).(*pb.DaemonRestartStatus)
	// Never block progress/acks behind a congested stream while holding the
	// admission lock. Registration and heartbeats repeat this observation.
	if s := d.session.Load(); s.open() {
		select {
		case s.sendCh <- &pb.DaemonMessage{Message: &pb.DaemonMessage_RestartStatus{RestartStatus: status}}:
		default:
		}
	}
}

func (d *daemonClient) restartProgressLocked() {
	if d.restartStatus == nil || d.restartStatus.Drained {
		return
	}
	d.results.mu.Lock()
	pending := len(d.results.running) + len(d.results.held)
	d.results.mu.Unlock()
	if d.restartCommands != 0 || (pending != 0 && !d.restartStatus.TimedOut) {
		return
	}
	d.restartStatus.Drained = true
	if d.restartStop != nil {
		d.restartStop()
	}
	d.publishRestartLocked()
}

func (d *daemonClient) restartExpired() {
	d.restartMu.Lock()
	defer d.restartMu.Unlock()
	if d.restartStatus == nil || d.restartStatus.Drained {
		return
	}
	d.restartStatus.TimedOut = true
	// Hold the same clear result B1 restores from a running marker, before
	// cancellation can produce CANCELLED or a late successful completion.
	d.results.mu.Lock()
	ids := make([]string, 0, len(d.results.running))
	for id := range d.results.running {
		ids = append(ids, id)
	}
	d.results.mu.Unlock()
	if d.restartAborted == nil {
		d.restartAborted = make(map[string]bool)
	}
	for _, id := range ids {
		d.restartAborted[id] = true
		d.results.hold(id, interruptedByRestartResponse(id), nil)
	}
	// Cancel registered daemon commands too; an uncooperative command must
	// not hold replacement indefinitely. Their stream reports interruption.
	d.cancelAllRequests()
	// Cancellation is not proof that an arbitrary command stopped. A command
	// that ignores its context must finish before replacement is safe; its
	// completion is fenced to an interrupted outcome, never late success.
	d.restartProgressLocked()
	d.publishRestartLocked()
	// Sending held results may block on I/O backpressure, so let ack processing
	// and the deadline lock proceed. The durable outbox also sends on reconnect.
	go d.sendHeldResults(d.session.Load(), func(*heldResult) bool { return true })
}

func (d *daemonClient) stopRestartTimer() {
	d.restartMu.Lock()
	defer d.restartMu.Unlock()
	if d.restartStop != nil {
		d.restartStop()
	}
}
