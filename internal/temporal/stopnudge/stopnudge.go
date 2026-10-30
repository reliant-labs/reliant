// Copyright (c) 2025 Reliant Labs

// Package stopnudge tells the worker running a workflow's activities that a
// stop was just requested for that workflow, so they can ask Temporal for the
// verdict at once instead of waiting for their next heartbeat.
//
// # Why this exists
//
// Temporal delivers a cancellation to a running activity ONLY in a heartbeat
// RPC's response, and the SDK sends at most one heartbeat per throttle window
// (worker.Options.MaxHeartbeatThrottleInterval). So the throttle used to be the
// whole cancel latency of pause, interrupt and stop, which is why it was 2s.
// That made heartbeats the largest single source of writes to Temporal's
// database: every heartbeat is one UpdateWorkflowExecution, and on 2026-10-30
// they were 13.6 of prod's 25.5 UpdateWorkflowExecution/s, all serialized on
// the workflow's lock ("Workflow is busy" for an orchestrator with 30 running
// sub-agent activities).
//
// With a nudge the throttle can be long (liveness only): a stop request made
// through Wrap's client publishes the workflow id on Subject; every worker
// hands it to its Hub; each activity of that workflow running there sends a
// few direct heartbeats over the next seconds and stops on a real verdict.
// The cost moves from every activity, all the time, to the activities of one
// workflow, when a user asks it to stop.
//
// A nudge is a hint, never the instruction: the stop itself still goes
// through Temporal (the signal, the workflow's RequestCancelActivityTask), and
// the activity stops only on the server's answer. A lost nudge costs latency
// (up to one throttle window), not correctness.
//
//forge:exclude-contract: a client.Client decorator and an in-memory fan-in; it publishes through the Publisher it is given and the worker owns the subscription, so it does no I/O of its own
package stopnudge

import (
	"context"
	"encoding/json"
	"sync"

	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/logging"
)

// Subject is the core-NATS subject nudges travel on. Fire-and-forget: a
// worker that is not subscribed at that instant has no running activity a
// nudge could help.
const Subject = "reliant.temporal.stop_nudge"

type message struct {
	WorkflowID string `json:"workflow_id"`
}

// Publisher is the one NATS method a nudging client needs; *nats.Conn has it.
type Publisher interface {
	Publish(subject string, data []byte) error
}

// nudgingClient is a client.Client whose stop requests also publish a nudge.
type nudgingClient struct {
	client.Client
	pub         Publisher
	stopSignals map[string]bool
}

// Wrap returns c, nudging after every successful stop request: CancelWorkflow,
// TerminateWorkflow, ResetWorkflowExecution, and SignalWorkflow for a signal
// named in stopSignals. Other signals (a wake, a state update) do not stop an
// activity, so they nudge nothing. A nil pub returns c unchanged.
func Wrap(c client.Client, pub Publisher, stopSignals ...string) client.Client {
	if c == nil || pub == nil {
		return c
	}
	set := make(map[string]bool, len(stopSignals))
	for _, s := range stopSignals {
		set[s] = true
	}
	return &nudgingClient{Client: c, pub: pub, stopSignals: set}
}

func (c *nudgingClient) SignalWorkflow(ctx context.Context, workflowID, runID, signalName string, arg interface{}) error {
	err := c.Client.SignalWorkflow(ctx, workflowID, runID, signalName, arg)
	if err == nil && c.stopSignals[signalName] {
		c.nudge(ctx, workflowID)
	}
	return err
}

func (c *nudgingClient) CancelWorkflow(ctx context.Context, workflowID, runID string) error {
	err := c.Client.CancelWorkflow(ctx, workflowID, runID)
	if err == nil {
		c.nudge(ctx, workflowID)
	}
	return err
}

func (c *nudgingClient) TerminateWorkflow(ctx context.Context, workflowID, runID, reason string, details ...interface{}) error {
	err := c.Client.TerminateWorkflow(ctx, workflowID, runID, reason, details...)
	if err == nil {
		c.nudge(ctx, workflowID)
	}
	return err
}

func (c *nudgingClient) ResetWorkflowExecution(ctx context.Context, req *workflowservice.ResetWorkflowExecutionRequest) (*workflowservice.ResetWorkflowExecutionResponse, error) {
	resp, err := c.Client.ResetWorkflowExecution(ctx, req)
	if err == nil {
		// The reset run is a new execution: the old run's activities learn
		// they are gone (NotFound) from their next heartbeat.
		c.nudge(ctx, req.GetWorkflowExecution().GetWorkflowId())
	}
	return resp, err
}

// nudge publishes best-effort: the stop already succeeded, and a lost nudge
// only means the activity hears of it on its next regular heartbeat.
func (c *nudgingClient) nudge(ctx context.Context, workflowID string) {
	if workflowID == "" {
		return
	}
	data, err := json.Marshal(message{WorkflowID: workflowID})
	if err == nil {
		err = c.pub.Publish(Subject, data)
	}
	if err != nil {
		logging.WarnContext(ctx, "[stopnudge] Failed to publish stop nudge; running activities hear of the stop on their next heartbeat",
			"workflowID", workflowID, "error", err)
	}
}

// Hub fans nudges in from NATS out to the activities running on this worker,
// by workflow id. The zero value is not usable; call NewHub. The worker owns
// the NATS subscription on Subject and hands each message to HandleMessage.
type Hub struct {
	mu      sync.Mutex
	watches map[string]map[chan struct{}]struct{}
}

// NewHub returns an empty hub.
func NewHub() *Hub {
	return &Hub{watches: make(map[string]map[chan struct{}]struct{})}
}

// HandleMessage delivers one message received on Subject. A malformed one is
// dropped: a nudge is only a hint.
func (h *Hub) HandleMessage(data []byte) {
	var msg message
	if err := json.Unmarshal(data, &msg); err != nil || msg.WorkflowID == "" {
		return
	}
	h.Deliver(msg.WorkflowID)
}

// Watch returns a channel that receives a value whenever a stop is requested
// for workflowID, and the func that stops watching. The channel holds one
// pending nudge: nudges arriving while one is pending coalesce, which is what
// a watcher wants (it probes the server either way).
func (h *Hub) Watch(workflowID string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	set := h.watches[workflowID]
	if set == nil {
		set = make(map[chan struct{}]struct{})
		h.watches[workflowID] = set
	}
	set[ch] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			delete(set, ch)
			if len(h.watches[workflowID]) == 0 {
				delete(h.watches, workflowID)
			}
		})
	}
}

// Deliver nudges every watcher of workflowID, never blocking.
func (h *Hub) Deliver(workflowID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.watches[workflowID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// watching reports how many watchers workflowID has (tests).
func (h *Hub) watching(workflowID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.watches[workflowID])
}
