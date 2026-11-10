// Copyright (c) 2025 Reliant Labs

package daemonwire

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// SubjectDisconnectCommand carries the control plane's order to end a
// daemon's ESTABLISHED gateway stream, because the authority it connected
// with is gone: its credential was revoked, or its operator was removed from
// (or left) the org the credential belonged to.
//
// Revocation alone already stops the daemon's next authentication and every
// reconnect: the token authority filters revoked rows and nothing caches the
// answer. What it cannot reach is a stream that authenticated before it, which
// the gateway holds in memory and would otherwise serve until the daemon next
// reconnects. This command closes that gap.
//
// Every gateway replica receives it (a plain, fan-out subscription, never a
// queue group): a daemon's stream lives on exactly one replica, and only the
// control plane's NATS user may publish here, so no replica can tell in
// advance whether it is the holder. The replica holding the stream closes it;
// every other replica, and every replica when the daemon is not connected at
// all, does nothing.
//
// Delivery is best effort (plain NATS, no replay) and the command is
// idempotent. A lost command leaves the behaviour from before it existed: the
// stream ends at the daemon's next reconnect, which fails authentication. The
// revocation itself is durable either way.
//
// A fixed subject with the daemon in the payload, like
// SubjectRegistrySnapshot, so the subject grant is one literal and the daemon
// id never has to be a valid subject token.
const SubjectDisconnectCommand = "daemon.v1.commands.disconnect"

// DisconnectCommand is the payload on SubjectDisconnectCommand.
//
// A consumer ignores fields it does not know, so a later field (the id of the
// revoked credential, say) is an additive change.
type DisconnectCommand struct {
	// DaemonID is the daemon whose stream to end. Required.
	DaemonID string `json:"daemon_id"`
	// Reason is why, in words an operator can read ("credential revoked",
	// "member removed from org"). It is logged by the gateway and told to
	// the daemon in the status that ends its stream.
	Reason string `json:"reason,omitempty"`
	// RequestID identifies one command, so the publisher's log line and every
	// replica's can be joined. Not a dedupe key: the command is idempotent,
	// so a repeat needs none.
	RequestID string `json:"request_id,omitempty"`
}

// UnmarshalDisconnectCommand parses a disconnect command and validates the one
// field it must carry, daemon_id. Every error is permanent, so a consumer logs
// and drops the message.
func UnmarshalDisconnectCommand(data []byte) (DisconnectCommand, error) {
	var c DisconnectCommand
	if err := json.Unmarshal(data, &c); err != nil {
		return DisconnectCommand{}, fmt.Errorf("unmarshal daemon disconnect command: %w", err)
	}
	if strings.TrimSpace(c.DaemonID) == "" {
		return DisconnectCommand{}, errors.New("daemon disconnect command missing daemon_id")
	}
	return c, nil
}
