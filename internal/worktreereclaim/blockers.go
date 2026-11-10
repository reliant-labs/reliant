package worktreereclaim

import (
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"
)

// UninspectableProcessError is a hold by a process of this user whose state
// (cwd, exe, fds or maps) cannot be read, so nothing proves it is not using
// the workspace. The check stays closed on it; this type exists so the hold
// can name the process to a person who can end it (ReclaimBlocker).
type UninspectableProcessError struct {
	PID int
	// Comm is the process's name, "" when even its identity is unreadable.
	Comm string
	// What is the state that could not be read: "cwd", "exe", "fd 3", "maps".
	What string
	// Started is when the process started; zero when unknown.
	Started time.Time
	Err     error
}

func (e *UninspectableProcessError) Error() string {
	if e.Comm == "" {
		return fmt.Sprintf("process %d %s unreadable: %v", e.PID, e.What, e.Err)
	}
	return fmt.Sprintf("process %d (%s) %s unreadable: %v", e.PID, e.Comm, e.What, e.Err)
}

func (e *UninspectableProcessError) Unwrap() error { return e.Err }

// ReclaimBlocker is one process holding worktree reclaim because it cannot be
// inspected, aggregated over every check that met it: one undumpable process
// of the user (a leaked test fixture, an orphaned sandbox init) holds every
// worktree on the machine, and is the one thing a person needs to know.
type ReclaimBlocker struct {
	PID        int       `json:"pid"`
	Comm       string    `json:"comm,omitempty"`
	Unreadable string    `json:"unreadable"`
	StartedAt  time.Time `json:"started_at,omitzero"`
	FirstSeen  time.Time `json:"first_seen"`
	LastSeen   time.Time `json:"last_seen"`
	// Worktrees is how many distinct workspaces it has held since FirstSeen.
	Worktrees int `json:"worktrees"`
}

// String is the one line a person or agent acts on.
func (b ReclaimBlocker) String() string {
	who := "pid " + strconv.Itoa(b.PID)
	switch {
	case b.Comm != "" && !b.StartedAt.IsZero():
		who += fmt.Sprintf(" (%s, up %s)", b.Comm, b.LastSeen.Sub(b.StartedAt).Round(time.Minute))
	case b.Comm != "":
		who += " (" + b.Comm + ")"
	}
	return fmt.Sprintf("%s blocks worktree reclaim: %d worktree(s) held; its %s cannot be read (an undumpable process of this user), so nothing proves it is not using them",
		who, b.Worktrees, b.Unreadable)
}

// blockerTTL is how long a blocker stays reported after a check last met it.
const blockerTTL = time.Hour

// blockerWarnEvery bounds the daemon's warning about one blocker.
const blockerWarnEvery = time.Hour

// maxBlockerRoots bounds the workspaces remembered per blocker.
const maxBlockerRoots = 10_000

type blockerKey struct {
	pid     int
	started time.Time
}

type blockerState struct {
	ReclaimBlocker
	roots  map[string]bool
	warned time.Time
}

type blockerSet struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[blockerKey]*blockerState
}

// blockers is every check's record in this process: the daemon's retirement,
// its machine sweep and its activity observation all meet the same blocker.
var blockers = &blockerSet{now: time.Now}

// note records that e held roots.
func (s *blockerSet) note(e *UninspectableProcessError, roots []string) {
	now := s.now()
	s.mu.Lock()
	if s.m == nil {
		s.m = map[blockerKey]*blockerState{}
	}
	s.expireLocked(now)
	k := blockerKey{e.PID, e.Started}
	b := s.m[k]
	if b == nil {
		b = &blockerState{ReclaimBlocker: ReclaimBlocker{PID: e.PID, StartedAt: e.Started, FirstSeen: now}, roots: map[string]bool{}}
		s.m[k] = b
	}
	b.LastSeen, b.Unreadable = now, e.What
	if e.Comm != "" {
		b.Comm = e.Comm
	}
	// One check is one workspace: its first root is the workspace, any other
	// is that workspace's own (its quarantine).
	if len(roots) > 0 && len(b.roots) < maxBlockerRoots {
		b.roots[roots[0]] = true
	}
	b.Worktrees = len(b.roots)
	warn := now.Sub(b.warned) >= blockerWarnEvery
	if warn {
		b.warned = now
	}
	line := b.ReclaimBlocker.String()
	s.mu.Unlock()
	if warn {
		slog.Warn("worktreereclaim: "+line, "pid", e.PID, "comm", e.Comm, "unreadable", e.What)
	}
}

func (s *blockerSet) expireLocked(now time.Time) {
	for k, b := range s.m {
		if now.Sub(b.LastSeen) > blockerTTL {
			delete(s.m, k)
		}
	}
}

// list is every blocker met within blockerTTL, the most worktrees first.
func (s *blockerSet) list() []ReclaimBlocker {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(s.now())
	out := make([]ReclaimBlocker, 0, len(s.m))
	for _, b := range s.m {
		out = append(out, b.ReclaimBlocker)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Worktrees != out[j].Worktrees {
			return out[i].Worktrees > out[j].Worktrees
		}
		return out[i].PID < out[j].PID
	})
	return out
}

// Blockers is every process that held worktree reclaim in this process within
// the last hour because it could not be inspected, one entry per process.
func Blockers() []ReclaimBlocker { return blockers.list() }
