package tools_daemon

import (
	"fmt"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// dispatchTerminalEvent sends a terminal output event to all subscribers for the session.
func (c *daemonConnection) dispatchTerminalEvent(evt *toolexec.TerminalOutputEvent) {
	c.terminalSubsMu.Lock()
	subs := c.terminalSubs[evt.SessionID]
	// Copy slice to release lock quickly.
	subsCopy := make([]chan *toolexec.TerminalOutputEvent, len(subs))
	copy(subsCopy, subs)
	c.terminalSubsMu.Unlock()

	for _, ch := range subsCopy {
		select {
		case ch <- evt:
		default:
			// Subscriber is slow; drop to avoid blocking the receive loop.
		}
	}
}

// dispatchProcessOutputEvent sends a process output event to all subscribers for the process.
func (c *daemonConnection) dispatchProcessOutputEvent(evt *toolexec.ProcessOutputEvent) {
	c.processOutputSubsMu.Lock()
	subs := c.processOutputSubs[evt.ProcessID]
	subsCopy := make([]chan *toolexec.ProcessOutputEvent, len(subs))
	copy(subsCopy, subs)
	c.processOutputSubsMu.Unlock()

	for _, ch := range subsCopy {
		select {
		case ch <- evt:
		default:
		}
	}
}

// closeAllSubscribers closes all terminal and process output subscriber channels.
// Called when a daemon disconnects.
func (c *daemonConnection) closeAllSubscribers() {
	c.terminalSubsMu.Lock()
	for sessionID, subs := range c.terminalSubs {
		for _, ch := range subs {
			close(ch)
		}
		delete(c.terminalSubs, sessionID)
	}
	c.terminalSubsMu.Unlock()

	c.processOutputSubsMu.Lock()
	for processID, subs := range c.processOutputSubs {
		for _, ch := range subs {
			close(ch)
		}
		delete(c.processOutputSubs, processID)
	}
	c.processOutputSubsMu.Unlock()
}

// SendTerminalInput sends raw PTY input bytes to the daemon for a terminal session.
func (s *Service) SendTerminalInput(userID, daemonID string, sessionID string, data []byte) error {
	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_TerminalInput{
			TerminalInput: &reliantv1.TerminalInputMessage{
				SessionId: sessionID,
				Data:      data,
			},
		},
	}
	return s.sendToDaemon(userID, daemonID, msg)
}

// SendTerminalResize sends a terminal resize request to the daemon.
func (s *Service) SendTerminalResize(userID, daemonID string, sessionID string, cols, rows uint32) error {
	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_TerminalResize{
			TerminalResize: &reliantv1.TerminalResizeMessage{
				SessionId: sessionID,
				Cols:      cols,
				Rows:      rows,
			},
		},
	}
	return s.sendToDaemon(userID, daemonID, msg)
}

// SubscribeTerminalOutput registers a subscriber for terminal output for a session.
// Returns a channel that receives events, an unsubscribe function, and an error.
func (s *Service) SubscribeTerminalOutput(userID, daemonID string, sessionID string) (<-chan *toolexec.TerminalOutputEvent, func(), error) {
	conn, err := s.connForUser(userID, daemonID)
	if err != nil {
		return nil, nil, err
	}

	ch := make(chan *toolexec.TerminalOutputEvent, 64)

	// Register the subscriber channel BEFORE sending the subscribe message so no
	// dispatched output can be dropped between the daemon starting its pump and
	// the channel being registered.
	conn.terminalSubsMu.Lock()
	conn.terminalSubs[sessionID] = append(conn.terminalSubs[sessionID], ch)
	conn.terminalSubsMu.Unlock()

	// Send the terminal-output-subscribe message to the daemon. This is what
	// starts the PTY output pump on the daemon side — mirroring the
	// process-output subscribe flow — so the initial shell prompt is buffered
	// until the full subscriber chain is established.
	subMsg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_TerminalOutputSubscribe{
			TerminalOutputSubscribe: &reliantv1.TerminalOutputSubscribeMessage{
				SessionId: sessionID,
			},
		},
	}
	if err := s.sendToDaemon(userID, daemonID, subMsg); err != nil {
		// Roll back the registration on failure.
		conn.terminalSubsMu.Lock()
		subs := conn.terminalSubs[sessionID]
		for i, sub := range subs {
			if sub == ch {
				conn.terminalSubs[sessionID] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		if len(conn.terminalSubs[sessionID]) == 0 {
			delete(conn.terminalSubs, sessionID)
		}
		conn.terminalSubsMu.Unlock()
		return nil, nil, fmt.Errorf("send terminal output subscribe: %w", err)
	}

	unsub := func() {
		conn.terminalSubsMu.Lock()
		defer conn.terminalSubsMu.Unlock()
		subs := conn.terminalSubs[sessionID]
		for i, sub := range subs {
			if sub == ch {
				conn.terminalSubs[sessionID] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		if len(conn.terminalSubs[sessionID]) == 0 {
			delete(conn.terminalSubs, sessionID)
		}
	}

	return ch, unsub, nil
}

// SubscribeProcessOutput registers a subscriber for process output.
// It sends a ProcessOutputSubscribe message to the daemon and returns a channel
// that receives output events. The unsubscribe function sends an unsubscribe message
// and removes the subscriber.
func (s *Service) SubscribeProcessOutput(userID, daemonID string, processID string, newOnly bool) (<-chan *toolexec.ProcessOutputEvent, func(), error) {
	conn, err := s.connForUser(userID, daemonID)
	if err != nil {
		return nil, nil, err
	}

	// Send subscribe message to daemon.
	subMsg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_ProcessOutputSubscribe{
			ProcessOutputSubscribe: &reliantv1.ProcessOutputSubscribeMessage{
				ProcessId: processID,
				NewOnly:   newOnly,
			},
		},
	}
	if err := s.sendToDaemon(userID, daemonID, subMsg); err != nil {
		return nil, nil, fmt.Errorf("send process output subscribe: %w", err)
	}

	ch := make(chan *toolexec.ProcessOutputEvent, 128)

	conn.processOutputSubsMu.Lock()
	conn.processOutputSubs[processID] = append(conn.processOutputSubs[processID], ch)
	conn.processOutputSubsMu.Unlock()

	unsub := func() {
		// Remove subscriber.
		conn.processOutputSubsMu.Lock()
		subs := conn.processOutputSubs[processID]
		for i, sub := range subs {
			if sub == ch {
				conn.processOutputSubs[processID] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		remaining := len(conn.processOutputSubs[processID])
		if remaining == 0 {
			delete(conn.processOutputSubs, processID)
		}
		conn.processOutputSubsMu.Unlock()

		// Send unsubscribe message to daemon if no more subscribers.
		if remaining == 0 {
			unsubMsg := &reliantv1.ServerMessage{
				Message: &reliantv1.ServerMessage_ProcessOutputUnsubscribe{
					ProcessOutputUnsubscribe: &reliantv1.ProcessOutputUnsubscribeMessage{
						ProcessId: processID,
					},
				},
			}
			_ = s.sendToDaemon(userID, daemonID, unsubMsg) // best-effort
		}
	}

	return ch, unsub, nil
}
