package worktreereclaim

import (
	"fmt"
	"hash/fnv"
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
	// StartTicks is the process's start time from /proc/<pid>/stat (clock
	// ticks after boot): with PID it names one process, whatever the clock.
	// Zero when stat could not be read.
	StartTicks uint64
	// Started is StartTicks on the wall clock, for display only; zero when
	// the boot time is unknown.
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
	// Unidentified marks observations of a pid whose start time (stat) could
	// not be read. A pid alone does not name one process (pids are reused),
	// so they aggregate only while they keep coming: each within
	// unidentifiedWindow of the last. A gap starts a new entry.
	Unidentified bool `json:"unidentified,omitempty"`
}

// String is the one line a person or agent acts on.
func (b ReclaimBlocker) String() string {
	who := "pid " + strconv.Itoa(b.PID)
	switch {
	case b.Unidentified:
		return fmt.Sprintf("%s (identity unreadable; seen continuously %s to %s) blocks worktree reclaim: %d worktree(s) held; its %s cannot be read, so nothing proves it is not using them",
			who, b.FirstSeen.UTC().Format(time.RFC3339), b.LastSeen.UTC().Format(time.RFC3339), b.Worktrees, b.Unreadable)
	case b.Comm != "" && !b.StartedAt.IsZero() && !b.LastSeen.Before(b.StartedAt):
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

// maxUnidentified bounds the unaggregated observations kept; the oldest goes
// first. Each is one workspace held at one moment, so dropping one loses an
// old observation, never part of a count.
const maxUnidentified = 64

// unidentifiedWindow is how close together observations of a pid with no
// readable start time must come to count as one process. A pid is reused only
// after the kernel wraps pid_max, so one that keeps being met within this
// window is the same process in every practical case; a gap starts a new entry.
const unidentifiedWindow = 2 * time.Minute

// blockerKey names one process: its pid and its start time in clock ticks
// after boot, which the kernel never reuses for the pid while it lives. An
// observation without a start time is keyed by a sequence number instead
// (Unidentified).
type blockerKey struct {
	pid        int
	startTicks uint64
	seq        uint64
}

type blockerState struct {
	ReclaimBlocker
	// roots is every distinct workspace held, by 64-bit hash: exact (a
	// collision is ~2^-64) at 8 bytes each, so the count is never capped.
	roots  map[uint64]struct{}
	warned time.Time
}

type blockerSet struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[blockerKey]*blockerState
	seq uint64
}

func rootHash(root string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(root))
	return h.Sum64()
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
	unidentified := e.StartTicks == 0
	k := blockerKey{pid: e.PID, startTicks: e.StartTicks}
	if unidentified {
		k = s.unidentifiedKeyLocked(e.PID, now)
	}
	b := s.m[k]
	if b == nil {
		b = &blockerState{ReclaimBlocker: ReclaimBlocker{PID: e.PID, StartedAt: e.Started, FirstSeen: now, Unidentified: unidentified}, roots: map[uint64]struct{}{}}
		s.m[k] = b
	}
	b.LastSeen, b.Unreadable = now, e.What
	if e.Comm != "" {
		b.Comm = e.Comm
	}
	// One check is one workspace: its first root is the workspace, any other
	// is that workspace's own (its quarantine).
	if len(roots) > 0 {
		b.roots[rootHash(roots[0])] = struct{}{}
	}
	b.Worktrees = len(b.roots)
	warn := now.Sub(b.warned) >= blockerWarnEvery
	if warn {
		b.warned = now
	}
	line := b.String()
	s.mu.Unlock()
	if warn {
		slog.Warn("worktreereclaim: "+line, "pid", e.PID, "comm", e.Comm, "unreadable", e.What)
	}
}

// unidentifiedKeyLocked is the key for an observation of pid with no start
// time: the pid's unidentified entry last seen within unidentifiedWindow, or a
// new one (making room for it under maxUnidentified).
func (s *blockerSet) unidentifiedKeyLocked(pid int, now time.Time) blockerKey {
	var best *blockerKey
	for k, b := range s.m {
		if b.Unidentified && k.pid == pid && now.Sub(b.LastSeen) <= unidentifiedWindow && (best == nil || k.seq > best.seq) {
			kk := k
			best = &kk
		}
	}
	if best != nil {
		return *best
	}
	s.dropOldestUnidentifiedLocked()
	s.seq++
	return blockerKey{pid: pid, seq: s.seq}
}

// dropOldestUnidentifiedLocked makes room for one more unidentified entry.
func (s *blockerSet) dropOldestUnidentifiedLocked() {
	var oldest *blockerKey
	n := 0
	for k, b := range s.m {
		if !b.Unidentified {
			continue
		}
		n++
		if oldest == nil || k.seq < oldest.seq {
			kk := k
			oldest = &kk
		}
	}
	if n >= maxUnidentified && oldest != nil {
		delete(s.m, *oldest)
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
