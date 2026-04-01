// Copyright (c) 2025 Reliant Labs
package core

// Engine is the pure core state machine runner.
type Engine struct {
	program *Program
}

// NewEngine constructs a pure core engine.
func NewEngine(program *Program) *Engine {
	return &Engine{program: program}
}

// Step applies one event and returns the next state and decisions.
// Stream A scaffolding: no execution semantics yet.
func (e *Engine) Step(state State, event Event) (State, []Decision, error) {
	_ = event
	if state.Program == nil {
		state.Program = e.program
	}
	return state, nil, nil
}
