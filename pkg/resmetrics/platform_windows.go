//go:build windows

package resmetrics

import (
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// windows has no /proc: the readers ask the kernel through
// golang.org/x/sys/windows and the kernel32 calls it does not wrap. Each
// reader fails on its own.
const (
	defaultProcRoot   = ""
	defaultCgroupRoot = ""

	nativeReaders    = true
	volumesSupported = true
)

var (
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes        = kernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx  = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetPerformanceInfo    = kernel32.NewProc("K32GetPerformanceInfo")
	procGetProcessHandleCount = kernel32.NewProc("GetProcessHandleCount")
)

// vmCounters is VM_COUNTERS, what NtQueryInformationProcess returns for
// ProcessVmCounters.
type vmCounters struct {
	PeakVirtualSize            uintptr
	VirtualSize                uintptr
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// memoryStatusEx is MEMORYSTATUSEX.
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// performanceInformation is PERFORMANCE_INFORMATION.
type performanceInformation struct {
	CB                uint32
	CommitTotal       uintptr
	CommitLimit       uintptr
	CommitPeak        uintptr
	PhysicalTotal     uintptr
	PhysicalAvailable uintptr
	SystemCache       uintptr
	KernelTotal       uintptr
	KernelPaged       uintptr
	KernelNonpaged    uintptr
	PageSize          uintptr
	HandleCount       uint32
	ProcessCount      uint32
	ThreadCount       uint32
}

// readProcess fills the process group: the start time and CPU time from
// GetProcessTimes, resident (working set) and virtual size from
// NtQueryInformationProcess, threads from a Toolhelp snapshot, and the
// handle count. windows counts page faults without telling major from minor,
// has no file descriptors or context switch counts per process, and its IO
// counters count every transfer (cached reads, devices), not storage, so
// those are left out.
func readProcess(p *ProcessStats, fail func(string, error)) {
	self := windows.CurrentProcess()
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(self, &created, &exited, &kernel, &user); err != nil {
		fail("GetProcessTimes", err)
	} else {
		p.StartTime = time.Unix(0, created.Nanoseconds())
		p.CPU = &ProcessCPU{
			UserSeconds:   filetimeSeconds(user.LowDateTime, user.HighDateTime),
			SystemSeconds: filetimeSeconds(kernel.LowDateTime, kernel.HighDateTime),
		}
	}
	var vm vmCounters
	if err := windows.NtQueryInformationProcess(self, windows.ProcessVmCounters, unsafe.Pointer(&vm), uint32(unsafe.Sizeof(vm)), nil); err != nil {
		fail("NtQueryInformationProcess", err)
	} else {
		p.Memory = &ProcessMemory{RSS: uint64(vm.WorkingSetSize), Virtual: uint64(vm.VirtualSize)}
	}
	if n, err := selfThreads(); err != nil {
		fail("CreateToolhelp32Snapshot", err)
	} else {
		p.Threads = &n
	}
	if err := procGetProcessHandleCount.Find(); err != nil {
		fail("GetProcessHandleCount", err)
	} else {
		var n uint32
		if r, _, err := procGetProcessHandleCount.Call(uintptr(self), uintptr(unsafe.Pointer(&n))); r == 0 {
			fail("GetProcessHandleCount", err)
		} else {
			handles := int64(n)
			p.Handles = &handles
		}
	}
}

// selfThreads finds this process in a Toolhelp snapshot of every process and
// returns its thread count. Only the count is read.
func selfThreads() (int64, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snap) //nolint:errcheck // a read-only snapshot
	pid := windows.GetCurrentProcessId()
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		if entry.ProcessID == pid {
			return int64(entry.Threads), nil
		}
	}
	if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return 0, errors.New("this process is not in the snapshot")
	}
	return 0, err
}

// readHost fills the host group: the memory limit (the machine's physical
// memory) in either scope, and node-wide the CPU from GetSystemTimes and the
// memory from GlobalMemoryStatusEx and GetPerformanceInfo. windows has no
// load average; its page file is commit space, not swap; and it has handle
// and process counts but no limit to set them against: those are left out.
func readHost(h *HostStats, nodeWide bool, fail func(string, error)) {
	ms := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	msOK := false
	if err := procGlobalMemoryStatusEx.Find(); err != nil {
		fail("GlobalMemoryStatusEx", err)
	} else if r, _, err := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms))); r == 0 {
		fail("GlobalMemoryStatusEx", err)
	} else {
		msOK = true
		if h.MemoryLimit == nil {
			total := ms.TotalPhys
			h.MemoryLimit = &total
		}
	}
	if !nodeWide {
		return
	}
	if err := procGetSystemTimes.Find(); err != nil {
		fail("GetSystemTimes", err)
	} else {
		var idle, kernel, user windows.Filetime
		if r, _, err := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user))); r == 0 {
			fail("GetSystemTimes", err)
		} else {
			h.CPU = &HostCPU{
				Seconds: windowsCPUModes(
					filetimeSeconds(idle.LowDateTime, idle.HighDateTime),
					filetimeSeconds(kernel.LowDateTime, kernel.HighDateTime),
					filetimeSeconds(user.LowDateTime, user.HighDateTime),
				),
				NoIOWaitSteal: true,
			}
		}
	}
	if !msOK {
		return
	}
	pi := performanceInformation{CB: uint32(unsafe.Sizeof(performanceInformation{}))}
	if err := procGetPerformanceInfo.Find(); err != nil {
		fail("GetPerformanceInfo", err)
	} else if r, _, err := procGetPerformanceInfo.Call(uintptr(unsafe.Pointer(&pi)), uintptr(pi.CB)); r == 0 {
		fail("GetPerformanceInfo", err)
	} else {
		mem := windowsMemory(ms.TotalPhys, ms.AvailPhys, uint64(pi.SystemCache)*uint64(pi.PageSize))
		h.Memory = &mem
	}
}

// statVolume returns an identity for the volume path is on (a hash of its
// mount point) and the space used and available to this process there.
func statVolume(path string) (dev, used, free uint64, err error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, 0, err
	}
	var avail, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &totalFree); err != nil {
		return 0, 0, 0, fmt.Errorf("GetDiskFreeSpaceEx: %w", err)
	}
	buf := make([]uint16, windows.MAX_PATH+1)
	if err := windows.GetVolumePathName(p, &buf[0], uint32(len(buf))); err != nil {
		return 0, 0, 0, fmt.Errorf("GetVolumePathName: %w", err)
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.ToLower(windows.UTF16ToString(buf))))
	return h.Sum64(), total - min(totalFree, total), avail, nil
}
