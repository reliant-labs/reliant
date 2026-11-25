//go:build windows

package daemon

import "golang.org/x/sys/windows"

// AvailableBytes: see the unix version.
func AvailableBytes(path string) (uint64, bool) {
	p, err := nearestExisting(path)
	if err != nil {
		return 0, false
	}
	ptr, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return 0, false
	}
	var free uint64
	if err := windows.GetDiskFreeSpaceEx(ptr, &free, nil, nil); err != nil {
		return 0, false
	}
	return free, true
}
