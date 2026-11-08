package cli

import (
	"context"

	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/devstack"
	"github.com/reliant-labs/forge/pkg/release"
)

var ownedComposeRunning = func(ctx context.Context, projectDir, env string) (bool, error) {
	return (deploytarget.ComposeProvider{ProjectDir: projectDir}).OwnedRunning(ctx, env)
}

// reconcileLocalPresence uses the same live markers as down, rather than
// treating a best-effort session heartbeat as the authority on local liveness.
// The project ID hashes the exact checkout path, so neither a sibling worktree
// nor a same-named project can supply this stack's presence.
func reconcileLocalPresence(out *envStatusRecords, projectDir, env string, pids []int, facts procFacts) {
	owned := discoverMarkedStacks(pids, facts)[stackKey{projectID: projectIDForDir(projectDir), env: env}]
	if len(owned) == 0 {
		return
	}
	markLocalStackRunning(out, projectDir)
}

func markLocalStackRunning(out *envStatusRecords, projectDir string) {
	key := devstack.Worktree(projectDir)
	host := release.HostID()
	for i := range out.Sessions {
		s := &out.Sessions[i]
		if s.Worktree == key && s.Host == host {
			s.State = sessionStateRunning
			return
		}
	}
	out.SessionsRead = true
	out.Sessions = append(out.Sessions, envStatusSession{Worktree: key, Host: host, State: sessionStateRunning})
}
