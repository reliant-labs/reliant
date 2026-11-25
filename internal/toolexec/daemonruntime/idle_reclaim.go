// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/idlereclaim"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// The idle reclaim (internal/idlereclaim): the scratch directories' lifecycle
// and the node_modules strip of idle kept worktrees, on its own schedule.
// This file gives it what only the daemon has: the admission lease, the
// record of work naming a path in a scratch directory, and each chat's last
// tool call; and gives a failed command the note saying what it removed.

var (
	scratchUsesOnce sync.Once
	scratchUse      *worktreereclaim.UseLog // work naming a path in a scratch dir
	scratchChat     *worktreereclaim.UseLog // each chat's last tool call, keyed by its scratch dir
	scratchRoot     string

	idleRecordsOnce sync.Once
	idleRecords     *idlereclaim.Records
)

// sharedScratchUses returns the scratch use logs, or nils when $HOME cannot
// be resolved.
func sharedScratchUses() (use, chat *worktreereclaim.UseLog, root string) {
	scratchUsesOnce.Do(func() {
		r, err := idlereclaim.DefaultScratchRoot()
		if err != nil {
			return
		}
		state, err := worktreereclaim.DefaultStateDir()
		if err != nil {
			return
		}
		aliases := []string{"~/.reliant/scratch", "$HOME/.reliant/scratch", "${HOME}/.reliant/scratch"}
		if resolved, err := filepath.EvalSymlinks(r); err == nil && resolved != r {
			aliases = append(aliases, resolved)
		}
		scratchRoot = r
		scratchUse = worktreereclaim.NewUseLog(r, filepath.Join(state, idlereclaim.ScratchUseFile), time.Now, aliases...)
		scratchChat = worktreereclaim.NewUseLog(r, filepath.Join(state, idlereclaim.ScratchChatFile), time.Now)
	})
	return scratchUse, scratchChat, scratchRoot
}

// noteScratchUse records use of every scratch directory the texts name.
func noteScratchUse(texts ...string) {
	use, _, _ := sharedScratchUses()
	for _, t := range texts {
		use.Note(t)
	}
}

// noteChatActivity records a tool call of chatID: the clock on removing its
// scratch directory whole runs from its last one.
func noteChatActivity(chatID string) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" || strings.ContainsAny(chatID, `/\`) || strings.HasPrefix(chatID, ".") {
		return
	}
	_, chat, root := sharedScratchUses()
	if chat == nil {
		return
	}
	chat.Note(filepath.Join(root, chatID))
}

// sharedIdleRecords is the removal record the command note reads.
func sharedIdleRecords() *idlereclaim.Records {
	idleRecordsOnce.Do(func() {
		state, err := worktreereclaim.DefaultStateDir()
		if err != nil {
			return
		}
		idleRecords = idlereclaim.NewRecords(filepath.Join(state, idlereclaim.RecordFile))
	})
	return idleRecords
}

// idleReclaimNote is the note a command that failed in workingDir gets when
// the idle reclaim removed regenerable output there (idlereclaim.Records.Note).
func idleReclaimNote(workingDir string) string {
	return sharedIdleRecords().Note(workingDir)
}

// recoveryLease adapts the admission lease to idlereclaim.Lease.
func recoveryLease(ctx context.Context, paths []string) (idlereclaim.Lease, error) {
	l, err := AcquireWorktreeRecovery(ctx, paths)
	if err != nil {
		return nil, err
	}
	return l, nil
}

// runIdleReclaim runs the idle reclaim until ctx is done, when its mode says
// it runs on this daemon (mode auto: where the storage reaper runs).
func runIdleReclaim(ctx context.Context, cfg idlereclaim.Config, reaperEnabled bool, sweepMode worktreereclaim.SweepMode) {
	r, err := idlereclaim.NewReclaimer(cfg)
	if err != nil {
		logging.WarnContext(ctx, "[idlereclaim] Idle reclaim not started: home directory unavailable", "error", err)
		return
	}
	use, chat, _ := sharedScratchUses()
	wtUse := sharedWorktreeUse()
	r.Records = sharedIdleRecords()
	r.Acquire = recoveryLease
	r.ScratchUse = use.LastUse
	r.ChatUse = chat.LastUse
	r.WorktreeUse = wtUse.LastUse
	r.WorktreeSweepOn = sweepMode == worktreereclaim.SweepOn
	r.Run(ctx, reaperEnabled)
}
