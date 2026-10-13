// Copyright (c) 2025 Reliant Labs

// Package signaltest records the Temporal signals a handler sends, for the
// handler services' tests that assert on them (ChatService's sends and
// ToolCallService's spawn cancels), so each service's tests can move to its
// own package (research/forge-migration/05-p3a-handler-map.md). Only _test.go
// files import it.
//
//forge:exclude-contract: a test double of the Temporal SDK client, imported only by tests; nothing to fake behind it
package signaltest

import (
	"context"

	"go.temporal.io/sdk/client"
)

// Recorder is a Temporal client that records the signals it is sent. It
// embeds client.Client so any other SDK call panics rather than silently
// succeeding — the previous version of the spawn-cancel fix called
// TerminateWorkflow, which can never work for a spawn, and tests that passed a
// nil client could not catch it.
type Recorder struct {
	client.Client

	Signals   []Signal
	SignalErr error
}

// Signal is one SignalWorkflow call.
type Signal struct {
	WorkflowID string
	Name       string
	Arg        interface{}
}

// SignalWorkflow records the signal and returns SignalErr.
func (c *Recorder) SignalWorkflow(
	_ context.Context, workflowID, _ string, signalName string, arg interface{},
) error {
	c.Signals = append(c.Signals, Signal{WorkflowID: workflowID, Name: signalName, Arg: arg})
	return c.SignalErr
}
