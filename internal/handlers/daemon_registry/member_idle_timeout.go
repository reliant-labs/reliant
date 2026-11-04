package daemon_registry

import (
	"context"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/machinepool"
)

// applyMemberIdleTimeouts sets every managed member of userID's pools to its
// pool's member_idle_timeout (dev-docs/design/machine-pools.md, "What a member
// costs asleep vs running"): 30 idle minutes of an awake 2xl cost what 14
// hours of its sleeping disk do, so a pool member sleeps sooner than an
// interactive machine. only, when non-nil, limits it to those machines (a
// relabel touches one); nil is every machine (a pool definition changed).
//
// A machine in several pools takes the shortest timeout among them. A
// machine leaving every pool keeps the timeout it has: nothing records what
// it was before, and the owner can set it on the machine.
//
// Best effort: the pool or label write has already succeeded, a failure is
// logged, and the next write that touches the member converges it again.
func (s *Service) applyMemberIdleTimeouts(ctx context.Context, userID string, only []string) {
	if s.deps.IdleTimeouts == nil {
		return
	}
	jwt, ok := auth.GetUserJWT(userID)
	if !ok || strings.TrimSpace(jwt) == "" {
		logging.WarnContext(ctx, "[DaemonRegistry] pool member idle timeouts not applied: no signed-in session", "userID", userID)
		return
	}
	pools, err := s.deps.Repo.ListMachinePoolsForUser(ctx, userID)
	if err != nil || len(pools) == 0 {
		if err != nil {
			logging.WarnContext(ctx, "[DaemonRegistry] pool member idle timeouts not applied: listing pools failed", "error", err, "userID", userID)
		}
		return
	}
	daemons, err := s.deps.Repo.ListDaemonsByUserID(ctx, userID)
	if err != nil {
		logging.WarnContext(ctx, "[DaemonRegistry] pool member idle timeouts not applied: listing machines failed", "error", err, "userID", userID)
		return
	}
	limit := map[string]bool{}
	for _, id := range only {
		limit[id] = true
	}
	for _, d := range daemons {
		if only != nil && !limit[d.ID] {
			continue
		}
		timeout := memberIdleTimeout(d, pools)
		if timeout == "" {
			continue
		}
		if err := s.deps.IdleTimeouts.SetDaemonIdleTimeout(ctx, jwt, d.ID, timeout); err != nil {
			logging.WarnContext(ctx, "[DaemonRegistry] could not set a pool member's idle timeout",
				"error", err, "userID", userID, "daemonID", d.ID, "idleTimeout", timeout)
		}
	}
}

// memberIdleTimeout is the idle timeout d takes as a pool member: the
// shortest member_idle_timeout of the pools its labels match, or "" when it
// is in none or is not a managed machine (only those sleep).
func memberIdleTimeout(d *db.Daemon, pools []*core.MachinePool) string {
	if d == nil || d.DaemonType == nil || *d.DaemonType != "managed" {
		return ""
	}
	best, bestDur := "", time.Duration(0)
	for _, p := range pools {
		if !machinepool.Matches(d.Labels, p.Selector) {
			continue
		}
		t := p.MemberIdleTimeout
		if t == "" {
			t = core.DefaultMemberIdleTimeout
		}
		dur, err := time.ParseDuration(t)
		if err != nil {
			continue
		}
		if best == "" || dur < bestDur {
			best, bestDur = t, dur
		}
	}
	return best
}
