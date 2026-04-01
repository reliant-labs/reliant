// Copyright (c) 2025 Reliant Labs
package shell

import (
	"sync"
)

// BackgroundSignal tracks tool calls that should be converted to background mode.
// This is a simple in-memory registry that allows the gRPC service to signal
// tool executors that a tool should be backgrounded.
type BackgroundSignal struct {
	signals map[string]bool // toolCallID -> backgrounded
	mu      sync.RWMutex
}

var (
	bgSignal     *BackgroundSignal
	bgSignalOnce sync.Once
)

// GetBackgroundSignal returns the singleton background signal registry
func GetBackgroundSignal() *BackgroundSignal {
	bgSignalOnce.Do(func() {
		bgSignal = &BackgroundSignal{
			signals: make(map[string]bool),
		}
	})
	return bgSignal
}

// SetBackgrounded marks a tool call as needing to be converted to background
func (s *BackgroundSignal) SetBackgrounded(toolCallID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.signals[toolCallID] = true
}

// IsBackgrounded checks if a tool call should be converted to background
func (s *BackgroundSignal) IsBackgrounded(toolCallID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.signals[toolCallID]
}

// ClearBackgrounded removes the background signal for a tool call
// This should be called after the tool has been converted to background
func (s *BackgroundSignal) ClearBackgrounded(toolCallID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.signals, toolCallID)
}

// ClearAll removes all background signals (for testing or cleanup)
func (s *BackgroundSignal) ClearAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.signals = make(map[string]bool)
}
