//go:build windows

package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/reliant-labs/forge/internal/procguard"
)

// Windows process semantics, and how they map onto the Unix reference
// (procgroup_unix.go):
//
//   - There is no kill(2) / process group signalling. The stable handle on a
//     process tree is the parent/child relationship, so tree operations walk a
//     Toolhelp32 snapshot (ppidMap), exactly as the Unix killProcessTree does.
//   - SIGKILL == TerminateProcess on the root and every descendant.
//   - SIGTERM has no true equivalent. The best available graceful action is
//     CTRL_BREAK_EVENT, delivered with GenerateConsoleCtrlEvent to the
//     process group whose id is the child's pid. It is sent ONLY when the pid
//     is provably a group leader on our console (ctrlBreakAllowed): for a
//     non-group pid the call broadcasts to the whole console, forge included.
//     That needs (a) the child started with CREATE_NEW_PROCESS_GROUP
//     (startInOwnProcessGroup) and (b) a shared console. Detached
//     (DETACHED_PROCESS) children and teardown from a different console get no
//     event; that is fine because up_reclaim's SIGTERM -> poll -> SIGKILL
//     escalation then terminates them. SIGTERM therefore NEVER returns an
//     error for "could not deliver"; it returns nil after the best effort.
//   - No Job Object: JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE would kill detached
//     stacks that must outlive forge, and a job without it adds nothing over
//     the ppid walk (the walk already catches Air-style respawned children).
//     Assignment after Start is also racy (the child may spawn before it).
//
// PID-reuse guard: a snapshot's parent link is only trusted when the parent
// is alive and was created no later than the child (resolveParentLinks), and
// kills go through handles verified against snapshot creation times, so a
// recycled pid can neither graft a subtree onto the tree nor be hit itself.

var (
	modKernel32            = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleProcList = modKernel32.NewProc("GetConsoleProcessList")
	modIphlpapi            = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTCPTbl  = modIphlpapi.NewProc("GetExtendedTcpTable")
)

// startInOwnProcessGroup makes the child the root of a new console process
// group (pgid == child pid) so CTRL_BREAK can target it. Existing creation
// flags (e.g. DETACHED_PROCESS set by a caller) are preserved.
func startInOwnProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
}

// signalProcessGroup: SIGKILL terminates the whole tree rooted at pid;
// any other signal is the best-effort CTRL_BREAK described above.
func signalProcessGroup(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return nil
	}
	if sig == syscall.SIGKILL {
		return killProcessTree(pid, sig)
	}
	sendCtrlBreak(pid)
	return nil
}

// sendCtrlBreak delivers CTRL_BREAK only when pid is provably a process-group
// leader attached to our console (see ctrlBreakAllowed); otherwise it does
// nothing and the caller's SIGTERM -> poll -> SIGKILL escalation takes over.
// It never targets this command's own lineage, nor the console group this
// command belongs to — that event would land on forge itself (see procguard).
func sendCtrlBreak(pid int) {
	if procguard.Self().Contains(pid) {
		return
	}
	if group, ok := readProcGroupID(os.Getpid()); ok && int(group) == pid {
		return
	}
	sendCtrlBreakWith(pid, ctrlBreakFacts, func(p uint32) error {
		return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, p)
	})
}

func sendCtrlBreakWith(pid int, facts func(pid uint32) (groupID uint32, groupOK, onConsole, consoleOK bool), send func(uint32) error) {
	if pid <= 0 || !processAlive(pid) {
		return
	}
	groupID, groupOK, onConsole, consoleOK := facts(uint32(pid))
	if !ctrlBreakAllowed(uint32(pid), groupID, groupOK, onConsole, consoleOK) {
		return
	}
	_ = send(uint32(pid))
}

// ctrlBreakFacts reads pid's ProcessGroupId and whether pid is on our console.
func ctrlBreakFacts(pid uint32) (groupID uint32, groupOK, onConsole, consoleOK bool) {
	groupID, groupOK = readProcGroupID(int(pid))
	list, consoleOK := consoleProcessList()
	for _, p := range list {
		if p == pid {
			onConsole = true
		}
	}
	return
}

// consoleProcessList returns the pids attached to this process's console.
// GetConsoleProcessList returns the needed count, without filling the buffer,
// when it is too small; it returns 0 on failure.
func consoleProcessList() ([]uint32, bool) {
	buf := make([]uint32, 64)
	for attempt := 0; attempt < 4; attempt++ {
		n, _, _ := procGetConsoleProcList.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		switch {
		case n == 0:
			return nil, false
		case int(n) <= len(buf):
			return buf[:n], true
		default:
			buf = make([]uint32, n+8)
		}
	}
	return nil, false
}

// processAlive reports whether pid is a live process. Only
// ERROR_INVALID_PARAMETER from OpenProcess means "no such pid"; every other
// failure (access denied included) counts as alive so a false "dead" can
// never let a caller reclaim a live owner's state. Liveness of an openable
// process is WaitForSingleObject(h, 0): exit codes cannot tell a running
// process from one that exited with STILL_ACTIVE (259).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return aliveFromOpenError(err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	return aliveFromWait(windows.WaitForSingleObject(h, 0))
}

// killProcessTree: SIGKILL terminates pid and every descendant (root first so
// a supervisor cannot respawn). Handles are opened while walking the snapshot
// and verified against the snapshot's creation time, then terminated through
// those handles, so a pid recycled after the walk is never hit. Other signals
// send CTRL_BREAK (when safe) and return nil. A member of this command's own
// lineage is refused outright and never terminated as a descendant (see
// procguard).
func killProcessTree(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return nil
	}
	self := procguard.Self()
	if self.Contains(pid) {
		return &procguard.LineageError{PID: pid, Command: commandLabel(readProcArgv(pid))}
	}
	if sig != syscall.SIGKILL {
		sendCtrlBreak(pid)
		return nil
	}
	entries := snapshotProcesses()
	created := make(map[int]time.Time, len(entries))
	for _, e := range entries {
		if e.CreatedOK {
			created[e.PID] = e.Created
		}
	}
	targets := []int{pid}
	for _, d := range descendantsOf(pid, resolveParentLinks(entries)) {
		if !self.Contains(d) {
			targets = append(targets, d)
		}
	}
	var failures []error
	var handles []windows.Handle
	defer func() {
		for _, h := range handles {
			_ = windows.CloseHandle(h)
		}
	}()
	for _, target := range targets {
		h, err := openForKill(target, created[target])
		if err != nil {
			failures = append(failures, fmt.Errorf("open pid %d: %w", target, err))
			continue
		}
		if h == 0 {
			continue
		}
		handles = append(handles, h)
	}
	for _, h := range handles {
		if err := windows.TerminateProcess(h, 1); err != nil {
			if exitedAfterFailedTerminate(h) {
				continue
			}
			failures = append(failures, fmt.Errorf("terminate: %w", err))
		}
	}
	return errors.Join(failures...)
}

// terminateExitGrace bounds how long a failed TerminateProcess waits for the
// process to finish exiting before the failure counts.
const terminateExitGrace = 2 * time.Second

// exitedAfterFailedTerminate reports whether a process TerminateProcess just
// refused is in fact gone. Windows answers ERROR_ACCESS_DENIED for a process
// that is already EXITING — the kernel has begun tearing it down, but its
// handle is not signaled yet. Killing a tree hits that constantly: the
// CTRL_BREAK a SIGTERM sent, or killing the root, starts a child's exit a
// moment before we reach it. An instant WaitForSingleObject(h, 0) races that
// window (TestKillProcessTree failed on it in CI), so wait for the exit
// instead; a process that is truly protected stays unsignaled and the
// failure stands.
func exitedAfterFailedTerminate(h windows.Handle) bool {
	ev, err := windows.WaitForSingleObject(h, uint32(terminateExitGrace/time.Millisecond))
	return err == nil && ev == waitObject0
}

// openForKill opens a terminate-capable handle to pid, returning (0, nil) when
// the process is gone or is no longer the process the snapshot saw (pid
// reused, or creation time unknown).
func openForKill(pid int, want time.Time) (windows.Handle, error) {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return 0, nil
		}
		return 0, err
	}
	if want.IsZero() {
		_ = windows.CloseHandle(h)
		return 0, nil
	}
	got, ok := handleCreationTime(h)
	if !ok || !got.Equal(want) {
		_ = windows.CloseHandle(h)
		return 0, nil
	}
	return h, nil
}

func handleCreationTime(h windows.Handle) (time.Time, bool) {
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return time.Time{}, false
	}
	return time.Unix(0, creation.Nanoseconds()), true
}

func processCreationTime(pid int) (time.Time, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return time.Time{}, false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	return handleCreationTime(h)
}

// snapshotProcesses reads the Toolhelp32 process table and annotates every
// row with creation time and liveness (the facts resolveParentLinks needs).
func snapshotProcesses() []procSnapshotEntry {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	entry := windows.ProcessEntry32{}
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snap, &entry); err != nil {
		return nil
	}
	var out []procSnapshotEntry
	for {
		pid := int(entry.ProcessID)
		e := procSnapshotEntry{PID: pid, PPID: int(entry.ParentProcessID)}
		e.Created, e.CreatedOK = processCreationTime(pid)
		e.Alive = processAlive(pid)
		out = append(out, e)
		if err := windows.Process32Next(snap, &entry); err != nil {
			break
		}
	}
	return out
}

// ppidMap returns a pid->ppid snapshot of the whole process table. Toolhelp's
// parent id is the creator at creation time and is never updated, and Windows
// recycles pids fast, so links that cannot be shown to be genuine are dropped
// (mapped to 0); see resolveParentLinks. This protects every consumer: the
// ancestry walk in up_reclaim.go and descendantPIDs.
func ppidMap() map[int]int {
	entries := snapshotProcesses()
	if len(entries) == 0 {
		return nil
	}
	return resolveParentLinks(entries)
}

// descendantPIDs returns every trusted transitive child of root.
func descendantPIDs(root int) []int {
	return descendantsOf(root, ppidMap())
}

// portListenerPID returns the pid listening on TCP <port> (IPv4 or IPv6, any
// local address, matching the lsof behaviour), or 0 when none is found.
func portListenerPID(port int) int {
	if port <= 0 || port > 65535 {
		return 0
	}
	if pid := listenerPIDFromTable(windows.AF_INET, port, tcp4RowLayout); pid != 0 {
		return pid
	}
	return listenerPIDFromTable(windows.AF_INET6, port, tcp6RowLayout)
}

// listenerPIDFromTable reads GetExtendedTcpTable(TCP_TABLE_OWNER_PID_LISTENER)
// and scans it with the row layout for family (tcp_table.go).
func listenerPIDFromTable(family uint32, port int, layout tcpRowLayout) int {
	const tcpTableOwnerPIDListener = 3
	size := uint32(0)
	var buf []byte
	for attempt := 0; attempt < 5; attempt++ {
		var p unsafe.Pointer
		if len(buf) > 0 {
			p = unsafe.Pointer(&buf[0])
		}
		r, _, _ := procGetExtendedTCPTbl.Call(uintptr(p), uintptr(unsafe.Pointer(&size)), 0,
			uintptr(family), tcpTableOwnerPIDListener, 0)
		switch syscall.Errno(r) {
		case 0:
			return scanListenerTable(buf, port, layout)
		case windows.ERROR_INSUFFICIENT_BUFFER:
			buf = make([]byte, size)
		default:
			return 0
		}
	}
	return 0
}

// procStartTimes returns each pid's creation time; unreadable pids are omitted.
func procStartTimes(pids []int) map[int]time.Time {
	out := map[int]time.Time{}
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		if t, ok := processCreationTime(pid); ok {
			out[pid] = t
		}
	}
	return out
}
