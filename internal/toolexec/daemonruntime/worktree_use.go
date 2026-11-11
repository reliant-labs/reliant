package daemonruntime

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// The daemon's record of path use (worktreereclaim.UseLog): every tool call
// and every command that works on files notes the workspaces its input
// names, and the shared Reclaimer reports the last one with each answer
// about an active worktree, so the server never archives a worktree an agent
// is using by path from a chat bound elsewhere.

// worktreeUses holds one use log per worktree root, keyed like
// worktreeReclaimers: the root follows $HOME.
var (
	worktreeUsesMu sync.Mutex
	worktreeUses   = map[string]*worktreereclaim.UseLog{}
)

// sharedWorktreeUse is the use log of the daemon's worktree root, or nil when
// $HOME cannot be resolved (then there is no root to record use under).
func sharedWorktreeUse() *worktreereclaim.UseLog {
	root, err := worktreereclaim.DefaultRoot()
	if err != nil {
		return nil
	}
	worktreeUsesMu.Lock()
	defer worktreeUsesMu.Unlock()
	if u := worktreeUses[root]; u != nil {
		return u
	}
	statePath := ""
	if dir, err := worktreereclaim.DefaultStateDir(); err == nil {
		statePath = filepath.Join(dir, worktreereclaim.UseFile)
	}
	// How a tool input may spell the root besides its absolute path.
	aliases := []string{"~/.reliant/worktrees", "$HOME/.reliant/worktrees", "${HOME}/.reliant/worktrees"}
	if resolved, err := filepath.EvalSymlinks(root); err == nil && resolved != root {
		aliases = append(aliases, resolved)
	}
	u := worktreereclaim.NewUseLog(root, statePath, time.Now, aliases...)
	worktreeUses[root] = u
	return u
}

// noteWorktreeUse records use of every workspace the texts name: a tool
// call's working directory and input, a command's payload.
func noteWorktreeUse(texts ...string) {
	u := sharedWorktreeUse()
	for _, t := range texts {
		u.Note(t)
	}
}

// usesPathsCommand names the daemon commands whose payload is work on the
// paths it names: reading or writing files, running a command. Commands the
// UI polls (git status and changes, background-process listings) and the
// reclaim's own lifecycle commands, which carry the paths of every worktree
// they settle, are left out: counting them would keep every worktree open in
// a window, or every worktree on the machine, in use forever.
func usesPathsCommand(name string) bool {
	switch name {
	case "exec.run", "exec.bg_start", "terminal.create", "mcp.call_tool":
		return true
	}
	return strings.HasPrefix(name, "fs.")
}
