// Copyright (c) 2025 Reliant Labs
package terminal

import (
	"sync"
	"time"

	"github.com/aymanbagabas/go-pty"
	"github.com/gorilla/websocket"
)

// Session represents a terminal session with a PTY
type Session struct {
	ID         string
	UserID     string // Owner of this session for access control
	PTY        pty.Pty
	CMD        *pty.Cmd
	Conn       *websocket.Conn
	WorkingDir string
	CreatedAt  time.Time
	LastActive time.Time
	mu         sync.Mutex
	done       chan struct{}
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
