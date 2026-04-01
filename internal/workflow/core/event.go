// Copyright (c) 2025 Reliant Labs
package core

// Event represents a pure input into the core state machine.
type Event struct {
	Type string
	Data map[string]any
}
