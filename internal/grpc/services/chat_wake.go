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

// attendedWakeTimeout bounds how long a send waits on the control plane.
const attendedWakeTimeout = 30 * time.Second

// wakeDaemonForAttendedTurn wakes the daemon a signed-in user's turn will run
// on: the chat's pinned daemon, else default resolution. Tool-time traffic
// never resumes a daemon, so a daemon that idle-suspended mid-conversation can
// only be woken here (or by run preflight).
//
// Best-effort: a failed wake is logged and the message is still accepted; the
// tool call then reports ErrDaemonPending.
func (s *ChatService) wakeDaemonForAttendedTurn(ctx context.Context, userID string, chat *db.Chat) {
	waker, ok := s.daemonRouter.(toolexec.DaemonWaker)
	if !ok {
		return
	}
	// The control plane derives the owner from the caller's Bearer.
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
		logging.Warn("[ChatService] best-effort daemon wake failed; accepting the message anyway",
			"error", err, "user_id", userID)
	}
}
