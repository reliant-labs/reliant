package daemonruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

type ownedRunExecution struct {
	chat        string
	cancel      context.CancelFunc
	done        chan struct{}
	uncertainty error
}

func (d *daemonClient) trackRunExecution(id, chat string, cancel context.CancelFunc) func() {
	d.cancelMu.Lock()
	defer d.cancelMu.Unlock()
	if d.runExecutions == nil {
		d.runExecutions = map[string]*ownedRunExecution{}
	}
	e := &ownedRunExecution{chat: chat, cancel: cancel, done: make(chan struct{})}
	d.runExecutions[id] = e
	return func() { d.cancelMu.Lock(); delete(d.runExecutions, id); close(e.done); d.cancelMu.Unlock() }
}

// The reply acknowledges handler completion, not merely delivery of a signal.
// A timeout/error MUST NOT be translated into a successful terminal status.
func (d *daemonClient) cancelOwnedRun(ctx context.Context, payload []byte) ([]byte, error) {
	var req struct {
		ChatID string `json:"chat_id"`
	}
	if err := json.Unmarshal(payload, &req); err != nil || req.ChatID == "" {
		return nil, fmt.Errorf("run.cancel requires chat_id")
	}
	d.cancelMu.Lock()
	var owned []*ownedRunExecution
	for _, e := range d.runExecutions {
		if e.chat == req.ChatID {
			owned = append(owned, e)
		}
	}
	d.cancelMu.Unlock()
	for _, e := range owned {
		e.cancel()
	}
	for _, e := range owned {
		select {
		case <-e.done:
		case <-ctx.Done():
			return nil, fmt.Errorf("run cancellation unconfirmed: command still executing: %w", ctx.Err())
		}
	}
	// Without an execution-scoped dispatch fence, a terminated Temporal
	// activity can still create a fresh request after this snapshot. Never
	// claim stopped merely because the currently known handlers returned.
	d.cancelMu.Lock()
	defer d.cancelMu.Unlock()
	errs := []error{errors.New("run cancellation unconfirmed: known handlers drained, late activity dispatch is not fenced")}
	for _, e := range owned {
		errs = append(errs, e.uncertainty)
	}
	return nil, errors.Join(errs...)
}
