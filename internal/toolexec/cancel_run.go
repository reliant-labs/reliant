package toolexec

import (
	"context"
	"encoding/json"
	"fmt"
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
	for _, d := range daemons {
		reply, err := r.SendDaemonCommandToDaemon(ctx, userID, d.ID, "run.cancel", payload, 15000)
		if err != nil {
			return fmt.Errorf("command cancellation unconfirmed on machine %s: %w", d.ID, err)
		}
		var ack struct {
			Stopped bool `json:"stopped"`
		}
		if json.Unmarshal(reply, &ack) != nil || !ack.Stopped {
			return fmt.Errorf("command cancellation unconfirmed on machine %s: missing stopped acknowledgment", d.ID)
		}
	}
	return nil
}
