//go:build windows

package procguard

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// osLookup reads one Toolhelp32 snapshot of the process table and answers
// parent links from it, dropping any link trustedParent cannot vouch for.
// Windows has no process group a signal can target, so PGID stays 0.
func osLookup() Lookup {
	parents := snapshotParents()
	return func(pid int) (Entry, bool) {
		ppid, ok := parents[pid]
		if !ok {
			return Entry{}, false
		}
		childCreated, childOK := creationTime(pid)
		parentCreated, parentOK := creationTime(ppid)
		if !trustedParent(childCreated, parentCreated, childOK, parentOK) {
			return Entry{}, true // known process, no trustworthy parent
		}
		return Entry{PPID: ppid}, true
	}
}

func snapshotParents() map[int]int {
	out := map[int]int{}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return out
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	entry := windows.ProcessEntry32{}
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err := windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		out[int(entry.ProcessID)] = int(entry.ParentProcessID)
	}
	return out
}

func creationTime(pid int) (time.Time, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return time.Time{}, false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return time.Time{}, false
	}
	return time.Unix(0, creation.Nanoseconds()), true
}
