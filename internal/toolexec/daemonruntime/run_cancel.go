package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type ownedRunExecution struct {
	chat   string
	cancel context.CancelFunc
	done   chan struct{}
}

func (d *daemonClient) trackRunExecution(id, chat string, dispatchedAt int64, cancel context.CancelFunc) (func(), bool) {
	d.cancelMu.Lock()
	defer d.cancelMu.Unlock()
	if cutoff, ok := d.cancelledChats[chat]; ok && dispatchedAt <= cutoff {
		return func() {}, false
	}
	if d.runExecutions == nil {
		d.runExecutions = map[string]*ownedRunExecution{}
	}
	e := &ownedRunExecution{chat: chat, cancel: cancel, done: make(chan struct{})}
	d.runExecutions[id] = e
	return func() { d.cancelMu.Lock(); delete(d.runExecutions, id); close(e.done); d.cancelMu.Unlock() }, true
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
	if d.cancelledChats == nil {
		d.cancelledChats = map[string]int64{}
	}
	d.cancelledChats[req.ChatID] = time.Now().UnixNano() // reject late deliveries while cancellation drains
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
	return []byte(`{"stopped":true}`), nil
}
