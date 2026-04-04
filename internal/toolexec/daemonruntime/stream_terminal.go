package daemonruntime

import (
	"io"
	"sync"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
)

const terminalReadBufSize = 4096

// terminalPumpTracker tracks active terminal output pumps so they can be
// stopped when the daemon disconnects or a session is closed.
type terminalPumpTracker struct {
	mu    sync.Mutex
	pumps map[string]func() // sessionID -> stop function
}

func newTerminalPumpTracker() *terminalPumpTracker {
	return &terminalPumpTracker{
		pumps: make(map[string]func()),
	}
}

func (t *terminalPumpTracker) add(id string, stop func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pumps[id] = stop
}

func (t *terminalPumpTracker) remove(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.pumps, id)
}

func (t *terminalPumpTracker) stopAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, stop := range t.pumps {
		stop()
		delete(t.pumps, id)
	}
}

// handleTerminalInput writes raw bytes into the PTY for the given session.
func (d *daemonClient) handleTerminalInput(
	msg *reliantv1.TerminalInputMessage,
) {
	if terminalManager == nil {
		logging.Warn(logPrefix + " Terminal input received but terminal manager not initialized")
		return
	}
	sessionID := msg.GetSessionId()
	data := msg.GetData()
	if sessionID == "" || len(data) == 0 {
		return
	}

	if err := terminalManager.Write(sessionID, data); err != nil {
		logging.Warn(logPrefix+" Failed to write terminal input",
			"sessionID", sessionID, "error", err)
		// Notify the server that the session has an error.
		_ = d.send(&reliantv1.DaemonMessage{
			Message: &reliantv1.DaemonMessage_TerminalSessionEvent{
				TerminalSessionEvent: &reliantv1.TerminalSessionEvent{
					SessionId: sessionID,
					EventType: reliantv1.TerminalSessionEvent_EVENT_TYPE_ERROR,
					Message:   err.Error(),
				},
			},
		})
	}
}

// handleTerminalResize resizes the PTY for the given session.
func (d *daemonClient) handleTerminalResize(
	msg *reliantv1.TerminalResizeMessage,
) {
	if terminalManager == nil {
		logging.Warn(logPrefix + " Terminal resize received but terminal manager not initialized")
		return
	}
	sessionID := msg.GetSessionId()
	if sessionID == "" {
		return
	}
	cols := uint16(msg.GetCols())
	rows := uint16(msg.GetRows())

	if err := terminalManager.Resize(sessionID, cols, rows); err != nil {
		logging.Warn(logPrefix+" Failed to resize terminal",
			"sessionID", sessionID, "cols", cols, "rows", rows, "error", err)
		_ = d.send(&reliantv1.DaemonMessage{
			Message: &reliantv1.DaemonMessage_TerminalSessionEvent{
				TerminalSessionEvent: &reliantv1.TerminalSessionEvent{
					SessionId: sessionID,
					EventType: reliantv1.TerminalSessionEvent_EVENT_TYPE_ERROR,
					Message:   err.Error(),
				},
			},
		})
	}
}

// startTerminalOutputPump starts a goroutine that continuously reads from
// the PTY for the given session and sends TerminalOutputMessage chunks on
// the stream. It sends a TerminalSessionEvent when the session closes or
// encounters an error.
func (d *daemonClient) startTerminalOutputPump(
	sessionID string,
) {
	if terminalManager == nil {
		logging.Warn(logPrefix + " Cannot start terminal output pump: terminal manager not initialized")
		return
	}

	// Create a done channel to signal stop.
	done := make(chan struct{})
	stop := func() {
		select {
		case <-done:
		default:
			close(done)
		}
	}

	d.terminalPumps.add(sessionID, stop)

	go func() {
		defer d.terminalPumps.remove(sessionID)

		buf := make([]byte, terminalReadBufSize)
		for {
			// Check if we've been told to stop.
			select {
			case <-done:
				return
			default:
			}

			n, err := terminalManager.Read(sessionID, buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				sendErr := d.send(&reliantv1.DaemonMessage{
					Message: &reliantv1.DaemonMessage_TerminalOutput{
						TerminalOutput: &reliantv1.TerminalOutputMessage{
							SessionId: sessionID,
							Data:      chunk,
						},
					},
				})
				if sendErr != nil {
					logging.Warn(logPrefix+" Failed to send terminal output",
						"sessionID", sessionID, "error", sendErr)
					return
				}
			}

			if err != nil {
				eventType := reliantv1.TerminalSessionEvent_EVENT_TYPE_CLOSED
				msg := "session closed"
				if err != io.EOF {
					eventType = reliantv1.TerminalSessionEvent_EVENT_TYPE_ERROR
					msg = err.Error()
				}
				_ = d.send(&reliantv1.DaemonMessage{
					Message: &reliantv1.DaemonMessage_TerminalSessionEvent{
						TerminalSessionEvent: &reliantv1.TerminalSessionEvent{
							SessionId: sessionID,
							EventType: eventType,
							Message:   msg,
						},
					},
				})
				return
			}
		}
	}()
}
