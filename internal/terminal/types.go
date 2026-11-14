// Copyright (c) 2025 Reliant Labs
package terminal

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/aymanbagabas/go-pty"
	"github.com/gorilla/websocket"
)

// Session represents a terminal session with a PTY
type Session struct {
	ID     string
	UserID string // Owner of this session for access control
	PTY    pty.Pty
	CMD    *pty.Cmd
	// sandboxed is the shell of a credentials-none session on Unix, started
	// on the PTY in the credential sandbox (startShell); CMD is nil then.
	sandboxed  *exec.Cmd
	Conn       *websocket.Conn
	WorkingDir string
	CreatedAt  time.Time
	LastActive time.Time
	mu         sync.Mutex
	done       chan struct{}
}

// Process is the session's shell process (the sandbox's init for a
// credentials-none session), or nil before it started.
func (s *Session) Process() *os.Process {
	switch {
	case s.CMD != nil:
		return s.CMD.Process
	case s.sandboxed != nil:
		return s.sandboxed.Process
	}
	return nil
}

// wait waits for the session's shell to exit.
func (s *Session) wait() error {
	switch {
	case s.CMD != nil:
		return s.CMD.Wait()
	case s.sandboxed != nil:
		return s.sandboxed.Wait()
	}
	return errors.New("terminal: session has no process")
}

// ResizeMessage represents terminal resize event
type ResizeMessage struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// InputMessage represents terminal input from client
type InputMessage struct {
	Type string `json:"type"` // "input" or "resize"
	Data string `json:"data"` // For input type
	Cols uint16 `json:"cols"` // For resize type
	Rows uint16 `json:"rows"` // For resize type
}

// OutputMessage represents terminal output to client
type OutputMessage struct {
	Type string `json:"type"` // "output", "error", "exit", "init"
	Data string `json:"data"`
	Code int    `json:"code,omitempty"` // Exit code for "exit" type
	PID  int    `json:"pid,omitempty"`  // Process ID for "init" type
}
