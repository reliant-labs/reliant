package toolexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// CancelRunCommands drains this chat on every owned daemon, including node
// overrides. A publish is not an acknowledgment: every contacted daemon must
// reply after its execution handlers have returned. Unreachable means unknown.
func (r *NATSDaemonRouter) CancelRunCommands(ctx context.Context, userID, chatID string) error {
	if r.db == nil {
		return fmt.Errorf("cannot confirm command cancellation: daemon registry unavailable")
	}
	daemons, err := r.db.ListDaemonsByUserID(ctx, userID)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"chat_id": chatID})
	ids := make([]string, 0, len(daemons))
	for _, d := range daemons {
		ids = append(ids, d.ID)
	}
	return cancelOnMachines(ctx, ids, func(ctx context.Context, id string) ([]byte, error) {
		return r.SendDaemonCommandToDaemon(ctx, userID, id, "run.cancel", payload, 15000)
	})
}

// Each candidate is attempted independently. An offline machine must not
// prevent an executing one receiving cancel; uncertainty is aggregated.
func cancelOnMachines(ctx context.Context, ids []string, send func(context.Context, string) ([]byte, error)) error {
	results := make([]error, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			reply, err := send(ctx, id)
			if err != nil {
				results[i] = fmt.Errorf("command cancellation unconfirmed on machine %s: %w", id, err)
				return
			}
			var ack struct {
				Stopped bool `json:"stopped"`
			}
			if json.Unmarshal(reply, &ack) != nil || !ack.Stopped {
				results[i] = fmt.Errorf("command cancellation unconfirmed on machine %s: missing stopped acknowledgment", id)
			}
		}(i, id)
	}
	wg.Wait()
	return errors.Join(results...)
}
