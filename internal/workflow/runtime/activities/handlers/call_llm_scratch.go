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
// hand one a briefing file by path; each also gets a subdirectory of its own
// (subAgentScratchNote).
//
// The daemon's idle reclaim (internal/idlereclaim) empties a scratch
// directory nobody uses, on these default thresholds, and keeps one with a
// .keep file at its top (idlereclaim.KeepFile); the guidance says so, or an
// agent coming back to a week-old briefing finds it gone without warning.
//
// It also says the agent-id subdirectories are not the reader's to delete.
// On 2026-11-25 a root agent freeing disk space ran `for d in */; do rm -rf
// "$d"; done` in its chat's scratch directory and took its running
// sub-agents' briefings and a running gate's log with it (agent reports
// 25075365, e60bb521); nothing in the prompt said whose those were.
func scratchDirGuidance(chat *db.Chat) string {
	dir := chatScratchDir(chat)
	return "Put scratch you may still need later (briefings, notes, clones, background-process output) in this chat's scratch directory, " +
		dir + " (create it with `mkdir -p`; `~` is this machine's home directory, so run `echo ~` once and use the absolute path with file tools). " +
		"It survives a restart of the machine. /tmp does not: it is emptied whenever the machine restarts (a daemon upgrade, suspend and resume, a move to another host), " +
		"so keep only files you can lose there. " +
		"The machine cleans up scratch nobody uses: after 2 days unused it deletes the node_modules, build output and clean, pushed git worktrees in a scratch directory, " +
		"and after 7 days unused (counted from this chat's last tool call too) the whole directory. " +
		"To keep a directory and everything in it, create a file named `.keep` at its top. " +
		"Each sub-agent of this chat keeps its own files in a subdirectory named by its agent id (briefings it was handed, its logs, a running command's output): " +
		"never delete a subdirectory you did not create, even to free disk space, because a sub-agent may still be using it; remove only your own files, by name."
}

// chatScratchDir is the chat's scratch directory, as the prompt writes it.
func chatScratchDir(chat *db.Chat) string {
	dir := "~/.reliant/scratch/"
	if chat != nil && chat.ID != "" {
		dir += chat.ID + "/"
	}
	return dir
}

// subAgentScratchNote gives a spawned sub-agent (spawnDepth > 0) its own
// subdirectory of the chat's scratch directory, named by its agent id (its
// thread). Every agent of a chat was told the same directory, so two of them
// writing a conventional name (pr-body.md) overwrote each other's files. The
// chat's directory stays the place for handoffs: a parent names a briefing
// there by path, and the sub-agent reads it.
//
// A system block of its own, after the shared prompt, rather than a change to
// scratchDirGuidance: that one sits in the first system block, which sibling
// agents share in the provider's prompt cache. Empty for the root agent, and
// when there is no chat, thread or working directory to speak of.
func subAgentScratchNote(chat *db.Chat, thread string, spawnDepth int, hasWorkingDir bool) string {
	if spawnDepth <= 0 || thread == "" || chat == nil || chat.ID == "" || !hasWorkingDir {
		return ""
	}
	chatDir := chatScratchDir(chat)
	return "You are a sub-agent (agent_id " + thread + "), and other agents of this chat share its scratch directory, " + chatDir +
		". Put your own scratch files in your subdirectory of it, " + chatDir + thread + "/ (create it with `mkdir -p`), so you never overwrite another agent's file of the same name. " +
		"Read files your parent hands you from wherever it names them, and when you hand a file back, give its absolute path."
}
