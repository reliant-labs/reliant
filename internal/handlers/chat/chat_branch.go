// Copyright (c) 2025 Reliant Labs
package chat

import (
	"fmt"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// branchContextNote states what a branch is: of which chat, from which point,
// and — only when there are any — what became of the sub-agents the original
// had already spawned. Facts only, a few lines; the model decides what to do
// with them.
func branchContextNote(sourceChat *db.Chat, forkPoint *db.Message, inherited []*core.InheritedSpawnChild) string {
	source := "chat " + sourceChat.ID
	if sourceChat.Title != "" {
		source = fmt.Sprintf("%q (chat %s)", sourceChat.Title, sourceChat.ID)
	}
	note := fmt.Sprintf(
		"This chat is a branch of %s, forked at the message above (%s). The original chat may still be running.",
		source, forkPoint.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"))

	// One agent per child thread: a resumed spawn has a row per resumption.
	// A call whose child never landed still counts, keyed by its tool call.
	agents := map[string]bool{}
	ownedBySource := true
	for _, child := range inherited {
		key := child.ToolCallID
		if child.ChildThreadID != nil {
			key = *child.ChildThreadID
		}
		agents[key] = true
		if child.SourceChatID != sourceChat.ID {
			// A branch of a branch also inherits what older chats spawned.
			ownedBySource = false
		}
	}
	owner := "the original chat"
	if !ownedBySource {
		owner = "the chats that spawned them"
	}
	switch n := len(agents); {
	case n == 1:
		note += "\nThe sub-agent spawned before the branch belongs to " + owner + ". " +
			"Here it is visible read-only with spawn_status; its results and notifications are not delivered to this chat. " +
			"Re-spawn anything you need from this chat."
	case n > 1:
		note += fmt.Sprintf("\nThe %d sub-agents spawned before the branch belong to %s. ", n, owner) +
			"Here they are visible read-only with spawn_status; their results and notifications are not delivered to this chat. " +
			"Re-spawn anything you need from this chat."
	}
	return note
}
