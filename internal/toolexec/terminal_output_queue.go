package toolexec

import (
	"errors"
	"sync"
	"time"
)

// A terminal's output is a byte stream: a hole in it corrupts escape
// sequences, so full-screen programs render garbage and bursty output arrives
// with pieces missing. Each hop that hands a session's output from one
// goroutine to another (the gateway's dispatch to its NATS forwarder, the
// api-server's NATS subscription to the websocket pump) used to drop a chunk
// whenever its 64-slot channel was full, with no log and no metric
// (research/2026-11-04-terminal-incident.md). TerminalOutputQueue replaces
// that drop: the producer waits for the consumer, and when the consumer stays
// full past a bound the session ends with an error the browser shows.

// TerminalOutputOverflowMessage is the error a terminal session ends with when
// a hop's consumer fell behind. The browser prints it and reconnects.
const TerminalOutputOverflowMessage = "terminal output fell behind; reconnecting"

// TerminalOutputQueueCapacity is the events a TerminalOutputQueue holds before
// its producer waits: up to 1 MiB of 4 KiB PTY chunks.
const TerminalOutputQueueCapacity = 256

var (
	// ErrTerminalOutputOverflow is TerminalOutputQueue.Send's answer when the
	// consumer stayed full past the queue's timeout. The queue has failed: the
	// consumer receives what was queued, then the overflow event, then the
	// channel closes.
	ErrTerminalOutputOverflow = errors.New(TerminalOutputOverflowMessage)
	// ErrTerminalOutputClosed is TerminalOutputQueue.Send's answer once the
	// queue is closed.
	ErrTerminalOutputClosed = errors.New("terminal output queue closed")
)

// TerminalOutputQueue carries one terminal session's output from ONE producer
// goroutine to one consumer, in order and without loss. Send waits up to the
// queue's timeout for room; past it the queue fails, which ends the session
// loudly instead of losing bytes silently. Close may be called from any
// goroutine, any number of times, and the consumer must call it when it stops
// reading.
type TerminalOutputQueue struct {
	sessionID string
	timeout   time.Duration
	ch        chan *TerminalOutputEvent

	stop     chan struct{} // closed by Close; ends a waiting Send or fail
	stopOnce sync.Once

	mu       sync.Mutex // serialises sends against close(ch)
	failed   bool
	chClosed bool
}

// NewTerminalOutputQueue returns a queue for sessionID holding capacity events,
// whose producer waits at most timeout for the consumer.
func NewTerminalOutputQueue(sessionID string, capacity int, timeout time.Duration) *TerminalOutputQueue {
	return &TerminalOutputQueue{
		sessionID: sessionID,
		timeout:   timeout,
		ch:        make(chan *TerminalOutputEvent, capacity),
		stop:      make(chan struct{}),
	}
}

// C is the consumer's channel. It closes after Close, or after a failure has
// delivered its overflow event.
func (q *TerminalOutputQueue) C() <-chan *TerminalOutputEvent { return q.ch }

// Send delivers evt, waiting up to the queue's timeout while the consumer is
// full. It returns ErrTerminalOutputOverflow when that wait ran out (the queue
// has now failed; the caller counts it and stops sending), and
// ErrTerminalOutputClosed once the queue is closed or failed.
func (q *TerminalOutputQueue) Send(evt *TerminalOutputEvent) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.chClosed || q.failed {
		return ErrTerminalOutputClosed
	}
	select {
	case <-q.stop:
		return ErrTerminalOutputClosed
	case q.ch <- evt:
		return nil
	default:
	}
	timer := time.NewTimer(q.timeout)
	defer timer.Stop()
	select {
	case q.ch <- evt:
		return nil
	case <-q.stop:
		return ErrTerminalOutputClosed
	case <-timer.C:
	}
	q.failLocked()
	return ErrTerminalOutputOverflow
}

// Fail ends the queue as an overflow, for a loss the queue did not see itself
// (a NATS subscription that dropped messages). It is a no-op on a queue
// already closed or failed.
func (q *TerminalOutputQueue) Fail() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.chClosed || q.failed {
		return
	}
	q.failLocked()
}

// failLocked marks the queue failed and, without holding up the producer,
// delivers the overflow event after whatever is queued and closes the
// channel. The delivery waits for the consumer however long it takes, so a
// consumer that resumes always learns why its stream ended; every consumer
// closes the queue when it stops reading, which ends the wait.
func (q *TerminalOutputQueue) failLocked() {
	q.failed = true
	go func() {
		evt := &TerminalOutputEvent{SessionID: q.sessionID, Error: TerminalOutputOverflowMessage, Overflow: true}
		q.mu.Lock()
		defer q.mu.Unlock()
		if q.chClosed {
			return
		}
		select {
		case q.ch <- evt:
		case <-q.stop:
		}
		q.closeChLocked()
	}()
}

// Close ends the queue: a waiting Send returns, and the consumer's channel
// closes after what is already queued.
func (q *TerminalOutputQueue) Close() {
	q.stopOnce.Do(func() { close(q.stop) })
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closeChLocked()
}

func (q *TerminalOutputQueue) closeChLocked() {
	if !q.chClosed {
		q.chClosed = true
		close(q.ch)
	}
}
