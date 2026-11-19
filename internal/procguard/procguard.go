// Package procguard keeps forge from signalling the processes it is running
// inside.
//
// forge stops processes it started: `forge env down <env>`, `forge env down
// --all`, the `forge env up` pre-flight that replaces a running stack,
// host-infra shutdown and `forge debug stop`. Each selects its targets by
// forge's ownership markers, and those markers are ENVIRONMENT VARIABLES —
// inherited by every descendant of a process forge started. A forge-started
// server that itself runs work (an agent server hosting sessions, a shell, an
// IDE task runner) therefore hands the markers to everything it spawns,
// including a `forge env down` typed inside it. That command used to select
// the very server hosting it, SIGTERM the tree, and end its own session along
// with every sibling session running in it.
//
// The rule here is structural, not a heuristic: a forge command never signals
// its own LINEAGE — itself and every ancestor up to init. Lineage is read from
// the kernel's parent links, so no environment variable, process name or
// configuration can widen or narrow it, and the same process table always
// produces the same decision.
package procguard

import (
	"errors"
	"fmt"
	"os"
)

// Entry is what a lineage walk reads about one process.
type Entry struct {
	// PPID is the parent pid. 0 means "no trustworthy parent" and ends the
	// walk (on Windows a recorded parent that is dead or younger than its
	// child is reported as 0 — see trustedParent).
	PPID int
	// PGID is the process-group id, or 0 on a platform whose process groups
	// a signal cannot target (Windows).
	PGID int
}

// Lookup reports the table entry for pid; ok is false when pid is unknown
// (exited, or unreadable).
type Lookup func(pid int) (Entry, bool)

// maxDepth bounds a walk over a corrupt table. Real chains are a dozen deep
// (launchd → terminal → shell → forge-hosted server → shell → forge); a
// repeated pid already ends the walk, so this only guards a table that keeps
// inventing new parents.
const maxDepth = 4096

// Lineage is one process and every ancestor above it, nearest first.
type Lineage struct {
	chain  []int
	member map[int]bool
	groups map[int]bool
}

// Walk builds the lineage of self from lookup. self is always a member, even
// when lookup knows nothing about it. The walk ends at a pid <= 0, a process
// that is its own parent, a pid already seen (a cycle), or a pid lookup cannot
// answer for. Pure: the decision is a function of the table it is handed.
func Walk(self int, lookup Lookup) Lineage {
	l := Lineage{member: map[int]bool{}, groups: map[int]bool{}}
	pid := self
	for depth := 0; pid > 0 && depth < maxDepth && !l.member[pid]; depth++ {
		l.chain = append(l.chain, pid)
		l.member[pid] = true
		e, ok := lookup(pid)
		if !ok {
			break
		}
		if e.PGID > 0 {
			l.groups[e.PGID] = true
		}
		if e.PPID == pid {
			break
		}
		pid = e.PPID
	}
	return l
}

// Self returns this process's lineage, read from the operating system.
//
// It is read fresh on every call rather than cached: forge also runs embedded
// in long-lived servers, and a process whose parent dies is re-parented, so a
// cached chain could go on protecting a pid the OS has since recycled.
// A walk is a handful of syscalls.
func Self() Lineage {
	self := os.Getpid()
	lookup := osLookup()
	return Walk(self, func(pid int) (Entry, bool) {
		e, ok := lookup(pid)
		if !ok && pid == self {
			// The kernel always knows our own parent, even on a platform
			// with no process-table reader.
			return Entry{PPID: os.Getppid()}, true
		}
		return e, ok
	})
}

// Contains reports whether pid is this process or one of its ancestors.
func (l Lineage) Contains(pid int) bool { return l.member[pid] }

// ContainsGroup reports whether any member of the lineage belongs to process
// group pgid — so a group-wide signal to it would reach this command.
func (l Lineage) ContainsGroup(pgid int) bool { return l.groups[pgid] }

// Self reports whether pid is the process the lineage was walked from.
func (l Lineage) Self(pid int) bool { return len(l.chain) > 0 && l.chain[0] == pid }

// PIDs returns the lineage nearest first: this process, its parent, and so on.
func (l Lineage) PIDs() []int { return append([]int(nil), l.chain...) }

// ErrLineage is wrapped by every refusal to signal a member of this command's
// own lineage. Callers test for it with errors.Is.
var ErrLineage = errors.New("process is in this command's own lineage")

// LineageError is the refusal for one pid.
type LineageError struct {
	PID int
	// Command names the process for the reader ("" when unreadable).
	Command string
}

func (e *LineageError) Error() string {
	who := fmt.Sprintf("pid %d", e.PID)
	if e.Command != "" {
		who = fmt.Sprintf("pid %d (%s)", e.PID, e.Command)
	}
	return fmt.Sprintf("refused to signal %s: it is an ancestor of this command — stopping it would end the session running you", who)
}

func (e *LineageError) Unwrap() error { return ErrLineage }
