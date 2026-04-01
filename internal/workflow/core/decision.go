// Copyright (c) 2025 Reliant Labs
package core

// Decision is a pure side-effect command produced by the core state machine.
type Decision struct {
	Type string
	Data map[string]any
}
