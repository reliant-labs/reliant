// Copyright (c) 2025 Reliant Labs
package shell

import (
	"sync"
)

// CancelSignal tracks tool calls that have been cancelled by the user.
// This is a simple in-memory registry that allows the gRPC service to signal
// tool executors that a tool was cancelled, preventing "completed" status
// from being emitted after the user clicked cancel.
type CancelSignal struct {
	signals map[string]bool // toolCallID -> cancelled
	mu      sync.RWMutex
}

var (
	cancelSignal     *CancelSignal
	cancelSignalOnce sync.Once
)

// GetCancelSignal returns the singleton cancel signal registry
func GetCancelSignal() *CancelSignal {
	cancelSignalOnce.Do(func() {
		cancelSignal = &CancelSignal{
			signals: make(map[string]bool),
		}
	})
	return cancelSignal
}

// SetCancelled marks a tool call as cancelled by the user
func (s *CancelSignal) SetCancelled(toolCallID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.signals[toolCallID] = true
}

// IsCancelled checks if a tool call was cancelled by the user
func (s *CancelSignal) IsCancelled(toolCallID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.signals[toolCallID]
}

// ClearCancelled removes the cancel signal for a tool call
// This should be called after the tool has been marked as cancelled
func (s *CancelSignal) ClearCancelled(toolCallID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.signals, toolCallID)
}

// ClearAll removes all cancel signals (for testing or cleanup)
func (s *CancelSignal) ClearAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.signals = make(map[string]bool)
}
