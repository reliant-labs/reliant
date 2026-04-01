// Copyright (c) 2025 Reliant Labs
package terminal

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/aymanbagabas/go-pty"
	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Manager manages terminal sessions
type Manager struct {
	sessions map[string]*Session
	mu       sync.RWMutex
}

// NewManager creates a new terminal manager
func NewManager() *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
	}
}

// CreateSession creates a new terminal session owned by the given user.
// If userID is empty the session is unscoped (for backward compatibility).
func (m *Manager) CreateSession(workingDir string, userID string) (*Session, error) {
	sessionID := uuid.New().String()

	// Get default shell for the OS
	shell, args := GetDefaultShell()

	// Validate working directory
	if workingDir == "" {
		var err error
		workingDir, err = os.Getwd()
		if err != nil {
			workingDir = GetHomeDir()
		}
	}

	// Verify working directory exists
	if _, err := os.Stat(workingDir); os.IsNotExist(err) {
		logging.Warn("[Terminal] Working directory does not exist, using HOME", "dir", workingDir)
		workingDir = GetHomeDir()
	}

	// Create PTY
	ptty, err := pty.New()
	if err != nil {
		return nil, fmt.Errorf("failed to create PTY: %w", err)
	}

	// DON'T configure termios - go-pty already sets it up correctly
	// The default PTY configuration works properly with shells

	// Set initial terminal size (default to 80x24)
	if err := ptty.Resize(80, 24); err != nil {
		logging.Warn("[Terminal] Failed to set initial terminal size", "error", err)
	}

	// Create command
	cmd := ptty.Command(shell, args...)
	cmd.Dir = workingDir
	cmd.Env = GetShellEnv(workingDir)

	// Start the command
	if err := cmd.Start(); err != nil {
		ptty.Close()
		return nil, fmt.Errorf("failed to start command: %w", err)
	}

	session := &Session{
		ID:         sessionID,
		UserID:     userID,
		PTY:        ptty,
		CMD:        cmd,
		WorkingDir: workingDir,
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		done:       make(chan struct{}),
	}

	// Store session
	m.mu.Lock()
	m.sessions[sessionID] = session
	m.mu.Unlock()

	// Monitor process exit
	go m.monitorProcess(session)

	return session, nil
}

// GetSession retrieves a session by ID
func (m *Manager) GetSession(sessionID string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	session, ok := m.sessions[sessionID]
	return session, ok
}

// CloseSession closes and cleans up a terminal session
func (m *Manager) CloseSession(sessionID string) error {
	m.mu.Lock()
	session, ok := m.sessions[sessionID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("session not found: %s", sessionID)
	}
	delete(m.sessions, sessionID)
	m.mu.Unlock()

	return m.cleanupSession(session)
}

// cleanupSession cleans up session resources
func (m *Manager) cleanupSession(session *Session) error {
	session.mu.Lock()
	defer session.mu.Unlock()

	// Signal done
	select {
	case <-session.done:
		// Already closed
		return nil
	default:
		close(session.done)
	}

	var errs []error

	// Close PTY
	if session.PTY != nil {
		if err := session.PTY.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close PTY: %w", err))
		}
	}

	// Kill process if still running
	if session.CMD != nil && session.CMD.Process != nil {
		if err := session.CMD.Process.Kill(); err != nil && err.Error() != "os: process already finished" {
			errs = append(errs, fmt.Errorf("kill process: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("cleanup errors: %v", errs)
	}
	return nil
}

// monitorProcess monitors the shell process and cleans up on exit
func (m *Manager) monitorProcess(session *Session) {
	if session.CMD == nil {
		return
	}

	_ = session.CMD.Wait() // Wait for process to exit

	// Clean up session
	m.mu.Lock()
	delete(m.sessions, session.ID)
	m.mu.Unlock()

	if err := m.cleanupSession(session); err != nil {
		logging.Warn("[Terminal] Cleanup errors", "sessionID", session.ID[:8], "error", err)
	}
}

// Resize resizes the terminal
func (m *Manager) Resize(sessionID string, cols, rows uint16) error {
	session, ok := m.GetSession(sessionID)
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	session.mu.Lock()
	defer session.mu.Unlock()

	if session.PTY == nil {
		return fmt.Errorf("PTY not available")
	}

	err := session.PTY.Resize(int(cols), int(rows))
	if err != nil {
		return fmt.Errorf("failed to resize: %w", err)
	}
	return nil
}

// Write writes data to the PTY
func (m *Manager) Write(sessionID string, data []byte) error {
	session, ok := m.GetSession(sessionID)
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	session.mu.Lock()
	defer session.mu.Unlock()

	if session.PTY == nil {
		return fmt.Errorf("PTY not available")
	}

	_, err := session.PTY.Write(data)
	if err != nil {
		return fmt.Errorf("failed to write to PTY: %w", err)
	}

	session.LastActive = time.Now()
	return nil
}

// Read reads data from the PTY
func (m *Manager) Read(sessionID string, buf []byte) (int, error) {
	session, ok := m.GetSession(sessionID)
	if !ok {
		return 0, fmt.Errorf("session not found: %s", sessionID)
	}

	session.mu.Lock()
	pty := session.PTY
	session.mu.Unlock()

	if pty == nil {
		return 0, fmt.Errorf("PTY not available")
	}

	n, err := pty.Read(buf)

	if err != nil && err != io.EOF {
		return n, err
	}

	if n > 0 {
		session.mu.Lock()
		session.LastActive = time.Now()
		session.mu.Unlock()
	}

	return n, err
}

// ListSessions returns all active sessions (internal use / backward compatibility).
func (m *Manager) ListSessions() []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessions := make([]*Session, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	return sessions
}

// ListSessionsForUser returns only sessions owned by the given user.
func (m *Manager) ListSessionsForUser(userID string) []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var sessions []*Session
	for _, session := range m.sessions {
		if session.UserID == userID {
			sessions = append(sessions, session)
		}
	}
	return sessions
}

// CloseSessionForUser closes a session only if it belongs to the given user.
func (m *Manager) CloseSessionForUser(sessionID, userID string) error {
	m.mu.Lock()
	session, ok := m.sessions[sessionID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("session not found: %s", sessionID)
	}
	if session.UserID != userID {
		m.mu.Unlock()
		return fmt.Errorf("session not found: %s", sessionID) // Don't reveal existence to other users
	}
	delete(m.sessions, sessionID)
	m.mu.Unlock()

	return m.cleanupSession(session)
}

// Cleanup closes all sessions
func (m *Manager) Cleanup() {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.sessions = make(map[string]*Session)
	m.mu.Unlock()

	for _, session := range sessions {
		if err := m.cleanupSession(session); err != nil {
			logging.Warn("[Terminal] Cleanup error", "sessionID", session.ID[:8], "error", err)
		}
	}

	logging.Info("[Terminal] Cleaned up all sessions", "count", len(sessions))
}
