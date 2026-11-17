package storage

import "golang.org/x/sys/windows"

// DiskSpace reports capacity on the physical host filesystem.
func DiskSpace(path string) (Disk, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return Disk{}, err
	}
	var available, total, free uint64
	err = windows.GetDiskFreeSpaceEx(p, &available, &total, &free)
	return Disk{Path: path, Available: available, Capacity: total}, err
}
