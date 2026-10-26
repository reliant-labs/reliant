// Copyright (c) 2025 Reliant Labs
package daemonplacement

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// PinStore is the Pinner's repository: PinTarget's reads, and the one write.
type PinStore interface {
	Registry
	// PinChatDaemonIfUnset pins the chat to daemonID only when it has no pin
	// and is not a no-machine chat, and returns the pin the row holds
	// afterwards ("" when it holds none).
	PinChatDaemonIfUnset(ctx context.Context, chatID, daemonID string) (string, error)
}

// Pinner decides, once, which machine a chat runs on, and writes it to the
// chat row before a run starts.
//
// A chat started with the default machine stores no daemon, so every tool
// call of its re-ran default resolution, which prefers a connected local
// machine: a user with a cloud machine and several chats who connected their
// laptop saw every chat move to it (research/CHAT_MACHINE_PLACEMENT.md). A
// pinned chat carries session_daemon_id into the run and stays where it is
// until the user moves it or its machine is removed.
//
// Everything is best effort: a conversation always continues, so a failure is
// logged and the run starts unpinned, as it did before pinning existed.
type Pinner struct {
	repo     PinStore
	machines Machines
}

// NewPinner builds a Pinner. machines may be nil (no router wired): only the
// chat's own pin and its worktree's owner can then place it.
func NewPinner(repo PinStore, machines Machines) *Pinner {
	return &Pinner{repo: repo, machines: machines}
}

// Target is the machine chat should be pinned to, or "" when it should stay
// unpinned or the answer failed. It writes nothing: a chat about to be
// created carries it into the insert, and a wake for a chat not yet created
// targets it.
func (p *Pinner) Target(ctx context.Context, chat *db.Chat) string {
	id, err := PinTarget(ctx, p.repo, p.machines, chat)
	if err != nil {
		logging.WarnContext(ctx, "[daemonplacement] could not choose a machine for the chat; it stays unpinned",
			"chatID", chat.ID, "userID", chat.UserID, "error", err)
		return ""
	}
	return id
}

// Pin pins an unpinned chat to its Target and sets chat.ActiveDaemonID to the
// pin the row ends up holding, so the run being started carries it. A chat
// that already has a pin is left alone, and two sends racing to pin one chat
// agree on whichever was stored first.
func (p *Pinner) Pin(ctx context.Context, chat *db.Chat) {
	if chat == nil || chat.NoMachine || (chat.ActiveDaemonID != nil && *chat.ActiveDaemonID != "") {
		return
	}
	target := p.Target(ctx, chat)
	if target == "" {
		return
	}
	stored, err := p.repo.PinChatDaemonIfUnset(ctx, chat.ID, target)
	if err != nil {
		logging.WarnContext(ctx, "[daemonplacement] could not pin the chat to its machine; it stays unpinned",
			"chatID", chat.ID, "daemonID", target, "error", err)
		return
	}
	if stored == "" {
		return
	}
	chat.ActiveDaemonID = &stored
	logging.InfoContext(ctx, "[daemonplacement] pinned chat to its machine",
		"chatID", chat.ID, "daemonID", stored)
}
