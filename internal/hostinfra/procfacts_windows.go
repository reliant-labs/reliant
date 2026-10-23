//go:build windows

package hostinfra

import (
	"time"

	"golang.org/x/sys/windows"
)

// readProcFacts reads pid's image path (QueryFullProcessImageName) and
// creation time (GetProcessTimes).
func readProcFacts(pid int) procFacts {
	var f procFacts
	if pid <= 0 {
		return f
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return f
	}
	defer func() { _ = windows.CloseHandle(h) }()

	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err == nil {
		f.exe, f.exeOK = windows.UTF16ToString(buf[:n]), true
	}
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err == nil {
		f.start, f.startOK = time.Unix(0, creation.Nanoseconds()), true
	}
	return f
}
