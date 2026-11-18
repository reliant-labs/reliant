package handlers

import "github.com/reliant-labs/reliant/internal/db"

// scratchDirGuidance tells an agent where scratch it will still need goes: a
// per-chat directory under the machine's home, which survives a restart of the
// machine. /tmp does not: a cloud machine empties it on every boot (a daemon
// upgrade, suspend and resume, a node move), and a laptop clears it on
// reboot. The prompt used to suggest /tmp, and agents lost sweep briefings,
// clones and background-process output there mid-session (control-plane#2845;
// owner decision 2026-11-18: /tmp stays ephemeral, scratch moves).
//
// The path is written with "~" because only the daemon knows its home
// directory; the file tools take absolute paths, so the agent is told to
// expand it once. Sub-agents of the chat share its directory, so a parent can
// hand one a briefing file by path.
func scratchDirGuidance(chat *db.Chat) string {
	dir := "~/.reliant/scratch/"
	if chat != nil && chat.ID != "" {
		dir += chat.ID + "/"
	}
	return "Put scratch you may still need later (briefings, notes, clones, background-process output) in this chat's scratch directory, " +
		dir + " (create it with `mkdir -p`; `~` is this machine's home directory, so run `echo ~` once and use the absolute path with file tools). " +
		"It survives a restart of the machine. /tmp does not: it is emptied whenever the machine restarts (a daemon upgrade, suspend and resume, a move to another host), " +
		"so keep only files you can lose there."
}
