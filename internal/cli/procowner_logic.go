package cli

import (
	"errors"
	"sort"
	"syscall"
	"time"
	"unicode/utf16"
)

// Platform-neutral decision logic for the Windows process-ownership guards.
// Each function takes injected facts so it can be tested anywhere; the
// Windows files gather the facts.

// procSnapshotEntry is one row of a process-table snapshot plus the facts
// needed to decide whether its recorded parent link is trustworthy.
type procSnapshotEntry struct {
	PID, PPID int
	Created   time.Time
	CreatedOK bool // creation time was readable
	Alive     bool // the process currently exists (not merely a held handle)
}

// resolveParentLinks maps every snapshot pid to its parent pid, or to 0 when
// the recorded link cannot be trusted. Windows never re-parents: the ppid is
// the creator at creation time, and pids are recycled quickly, so a link is
// kept only when the parent pid is alive now, both creation times are
// readable, and the parent was created no later than the child. Anything else
// is dropped (fail closed). Callers treat ppid <= 1 as "no parent".
func resolveParentLinks(entries []procSnapshotEntry) map[int]int {
	byPID := make(map[int]procSnapshotEntry, len(entries))
	for _, e := range entries {
		byPID[e.PID] = e
	}
	out := make(map[int]int, len(entries))
	for _, e := range entries {
		out[e.PID] = 0
		parent, ok := byPID[e.PPID]
		if !ok || e.PPID == e.PID || !parent.Alive || !parent.CreatedOK || !e.CreatedOK {
			continue
		}
		if parent.Created.After(e.Created) {
			continue
		}
		out[e.PID] = e.PPID
	}
	return out
}

// descendantsOf returns every transitive child of root in a resolved
// pid->ppid map (see resolveParentLinks), breadth first, children in pid
// order. A root that is dead has no trusted children, so it yields nothing.
func descendantsOf(root int, ppids map[int]int) []int {
	children := map[int][]int{}
	for pid, ppid := range ppids {
		if ppid > 1 {
			children[ppid] = append(children[ppid], pid)
		}
	}
	for _, kids := range children {
		sort.Ints(kids)
	}
	var result []int
	seen := map[int]bool{root: true}
	queue := []int{root}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, c := range children[p] {
			if seen[c] {
				continue
			}
			seen[c] = true
			result = append(result, c)
			queue = append(queue, c)
		}
	}
	return result
}

// ctrlBreakAllowed reports whether CTRL_BREAK may be sent to pid.
// GenerateConsoleCtrlEvent with a pid that is not a process-group id
// broadcasts to every process on the console (microsoft/terminal#335), which
// includes forge itself and the user's shell, so it is sent only when pid is
// provably a group leader (its ProcessGroupId equals its pid) that is also
// attached to our console. Any unreadable fact denies.
func ctrlBreakAllowed(pid uint32, groupID uint32, groupOK bool, onConsole bool, consoleOK bool) bool {
	return groupOK && consoleOK && groupID == pid && onConsole
}

const (
	errnoInvalidParameter syscall.Errno = 87  // ERROR_INVALID_PARAMETER: no such pid
	waitObject0           uint32        = 0   // WAIT_OBJECT_0: process handle signalled = exited
	waitTimeout           uint32        = 258 // WAIT_TIMEOUT: still running
)

// aliveFromOpenError maps an OpenProcess failure to a liveness verdict. Only
// ERROR_INVALID_PARAMETER means the pid does not exist; access-denied and
// every other failure count as alive, because a false "dead" lets lockfile
// reclaim delete a live owner's lock.
func aliveFromOpenError(err error) bool {
	return !errors.Is(err, errnoInvalidParameter)
}

// aliveFromWait maps WaitForSingleObject(h, 0) on a process handle to a
// liveness verdict: signalled means exited; timeout means running; anything
// else is unknown and treated as alive.
func aliveFromWait(event uint32, err error) bool {
	if err != nil {
		return true
	}
	return event != waitObject0
}

// managedProcessExited reports whether mp's Wait has returned. Once it has,
// its pid has been reaped and no longer names that process, so signalling or
// tree-killing it could hit an unrelated process that recycled the pid.
func managedProcessExited(mp *managedProcess) bool {
	if mp == nil || mp.done == nil {
		return false
	}
	select {
	case <-mp.done:
		return true
	default:
		return false
	}
}

// ntStringOffset locates a UNICODE_STRING's text inside the local copy buf of
// a remote/kernel-filled struct. The kernel writes Buffer as an absolute
// pointer into buf; deriving the offset from addresses (instead of
// dereferencing the pointer) keeps the read independent of escape analysis
// and of stack copying. headerSize is sizeof(UNICODE_STRING); length is in
// bytes.
func ntStringOffset(buffer, base uintptr, length uint16, bufLen, headerSize uintptr) (uintptr, bool) {
	if buffer < base {
		return 0, false
	}
	off := buffer - base
	if off < headerSize || off%2 != 0 || length%2 != 0 || off+uintptr(length) > bufLen {
		return 0, false
	}
	return off, true
}

// decodeUTF16LE decodes little-endian UTF-16 bytes.
func decodeUTF16LE(b []byte) string {
	units := make([]uint16, len(b)/2)
	for i := range units {
		units[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(units))
}
