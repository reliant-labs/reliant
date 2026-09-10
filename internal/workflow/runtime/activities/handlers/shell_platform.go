// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// daemonPlatformReader is the narrow slice of the repository this resolver
// needs. Declared here, at the consumer, rather than reaching for the full
// db.Repository interface: the question "what OS is this daemon" needs two
// lookups, and naming them makes the dependency legible and trivially fakeable
// in a test that must assert a Windows record from a Linux test process.
type daemonPlatformReader interface {
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
	ListDaemonsByUserID(ctx context.Context, userID string) ([]*db.Daemon, error)
}

// resolveShellPlatform determines the shell family of the machine that will
// EXECUTE this chat's shell commands, so the shell tool can describe itself for
// that shell rather than for whatever OS this server was compiled on.
//
// Resolution mirrors the daemon routing precedence in ExecuteTools, because a
// description that describes a different daemon than the one that runs the
// command is the same bug in a new place:
//
//  1. The worktree's owning daemon. A worktree-bound (branch) chat MUST run on
//     the daemon holding its checkout, so that daemon's OS is the answer.
//  2. The chat's pinned ActiveDaemonID.
//  3. The user's daemons, but only if they UNANIMOUSLY agree on one platform.
//
// Step 3 is deliberately unanimous-or-nothing. Picking the first of a mixed
// fleet would be a coin flip presented to the model as fact, and a confident
// wrong dialect is worse than an admitted unknown: the model commits to bash
// syntax and discovers the mismatch halfway through a run. When daemons
// disagree — or when there is no daemon, or the lookup fails — this returns
// ShellPlatformUnknown, and the description tells the model to probe for its
// shell before relying on either dialect.
//
// Every failure path degrades to unknown rather than erroring. A description is
// advisory; being unable to name the platform must not fail an LLM call.
func resolveShellPlatform(ctx context.Context, repo daemonPlatformReader, chat *db.Chat, worktreeDaemonID string) tools.ShellPlatform {
	if repo == nil || chat == nil {
		return tools.ShellPlatformUnknown
	}

	if worktreeDaemonID != "" {
		if p := platformOfDaemon(ctx, repo, worktreeDaemonID); p != tools.ShellPlatformUnknown {
			return p
		}
	}

	if chat.ActiveDaemonID != nil && *chat.ActiveDaemonID != "" {
		if p := platformOfDaemon(ctx, repo, *chat.ActiveDaemonID); p != tools.ShellPlatformUnknown {
			return p
		}
	}

	daemons, err := repo.ListDaemonsByUserID(ctx, chat.UserID)
	if err != nil || len(daemons) == 0 {
		return tools.ShellPlatformUnknown
	}

	agreed := tools.ShellPlatformUnknown
	for _, d := range daemons {
		if d == nil || d.Platform == nil {
			continue
		}
		p := tools.ShellPlatformFromGOOS(*d.Platform)
		if p == tools.ShellPlatformUnknown {
			continue
		}
		if agreed == tools.ShellPlatformUnknown {
			agreed = p
			continue
		}
		if agreed != p {
			// A mixed fleet: any single answer would be a guess.
			return tools.ShellPlatformUnknown
		}
	}
	return agreed
}

// platformOfDaemon reads one daemon's persisted platform. A missing daemon, a
// null platform, or an unrecognized GOOS all yield unknown so the caller can
// fall through to the next resolution step.
func platformOfDaemon(ctx context.Context, repo daemonPlatformReader, daemonID string) tools.ShellPlatform {
	daemon, err := repo.GetDaemon(ctx, daemonID)
	if err != nil || daemon == nil || daemon.Platform == nil {
		return tools.ShellPlatformUnknown
	}
	return tools.ShellPlatformFromGOOS(*daemon.Platform)
}
