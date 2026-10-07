// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// attendedWakeTimeout bounds how long a send waits on the control plane's
// ResumeDaemon. Only a suspended daemon costs that call; any other is answered
// from this process's own daemon records.
const attendedWakeTimeout = 30 * time.Second

// wakeDaemonForAttendedTurn wakes the daemon a signed-in user's turn will run
// on: the chat's pinned daemon, else default resolution. Tool-time traffic
// never resumes a daemon, so a daemon that idle-suspended mid-conversation can
// only be woken here (or by run preflight).
//
// The router picks the daemon from the registry's records (suspended is the
// lifecycle phase the control plane mirrors in) and resumes it through
// controlplane.v1.DaemonService/ResumeDaemon. It returns once the wake is under
// way, not once the daemon is attached.
//
// Best-effort: a failed wake is logged and the message is still accepted; the
// tool call then reports ErrDaemonPending.
func (s *ChatService) wakeDaemonForAttendedTurn(ctx context.Context, userID string, chat *db.Chat) {
	// A chat with no machine by design has nothing to wake, and waking the
	// user's default machine is exactly what it must never do.
	if chat != nil && chat.NoMachine {
		return
	}
	waker, ok := s.daemonRouter.(toolexec.DaemonWaker)
	if !ok {
		return
	}
	// A resume acts as the user: the control plane derives the owner from the
	// forwarded JWT, so without one there is nothing to wake with.
	if jwt, ok := auth.GetUserJWT(userID); !ok || jwt == "" {
		return
	}
	var selector *toolexec.DaemonSelector
	if chat != nil && chat.ActiveDaemonID != nil && *chat.ActiveDaemonID != "" {
		selector = &toolexec.DaemonSelector{ID: *chat.ActiveDaemonID}
	}
	wakeCtx, cancel := context.WithTimeout(ctx, attendedWakeTimeout)
	defer cancel()
	if _, err := waker.EnsureAwake(wakeCtx, userID, selector); err != nil {
		// Pending is not a failed wake: the machine is already on its way up
		// (provisioning), and the turn's tool calls will wait for it.
		if toolexec.IsDaemonPending(err) {
			logging.Info("[ChatService] daemon is still coming up; accepting the message",
				"error", err, "user_id", userID)
			return
		}
		logging.Warn("[ChatService] best-effort daemon wake failed; accepting the message anyway",
			"error", err, "user_id", userID)
	}
}
