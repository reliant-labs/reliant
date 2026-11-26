//go:build darwin

package resmetrics

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// darwin has no /proc: the readers ask the kernel directly, through
// golang.org/x/sys/unix (getrusage, getrlimit, sysctl) and the libSystem
// calls in libsystem_darwin.go. Each reader fails on its own.
const (
	defaultProcRoot   = ""
	defaultCgroupRoot = ""

	nativeReaders = true
)

// readProcess fills the process group: CPU time, page faults and context
// switches from getrusage; the start time from the kernel's process table;
// resident and virtual size and threads from proc_pidinfo; open descriptors
// from /dev/fd; their limit from getrlimit. darwin keeps no per-process
// storage byte counts in getrusage (only block counts), so DiskIO is left out.
func readProcess(p *ProcessStats, fail func(string, error)) {
	var ru unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &ru); err != nil {
		fail("getrusage", err)
	} else {
		p.CPU = &ProcessCPU{UserSeconds: float64(ru.Utime.Nano()) / 1e9, SystemSeconds: float64(ru.Stime.Nano()) / 1e9}
		p.Faults = &PageFaults{Minor: uint64(ru.Minflt), Major: uint64(ru.Majflt)}                        //nolint:gosec // kernel counters are non-negative
		p.ContextSwitches = &ContextSwitches{Voluntary: uint64(ru.Nvcsw), Involuntary: uint64(ru.Nivcsw)} //nolint:gosec // kernel counters are non-negative
	}
	if kp, err := unix.SysctlKinfoProc("kern.proc.pid", os.Getpid()); err != nil {
		fail("sysctl kern.proc.pid", err)
	} else {
		p.StartTime = time.Unix(kp.Proc.P_starttime.Unix())
	}
	if rss, virt, threads, err := taskInfo(); err != nil {
		fail("proc_pidinfo", err)
	} else {
		p.Memory = &ProcessMemory{RSS: rss, Virtual: virt}
		p.Threads = &threads
	}
	// /dev/fd lists this process's open descriptors, the directory's own
	// among them, as /proc/self/fd does on linux.
	if ents, err := os.ReadDir("/dev/fd"); err != nil {
		fail("/dev/fd", err)
	} else {
		n := int64(len(ents))
		p.FDCount = &n
	}
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &lim); err != nil {
		fail("getrlimit", err)
	} else if lim.Cur >= unix.RLIM_INFINITY {
		fail("getrlimit", errors.New("open files is unlimited"))
	} else {
		n := int64(lim.Cur) //nolint:gosec // below RLIM_INFINITY, which is MaxInt64
		p.FDLimit = &n
	}
}

// readHost fills the host group: the memory limit (the machine's total) in
// either scope, and node-wide the CPU and memory from the mach host
// statistics, the load average, swap and the open-file table from sysctl.
// darwin has no system-wide process limit worth the name to set a count
// against, so Processes is left out.
func readHost(h *HostStats, nodeWide bool, fail func(string, error)) {
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		fail("sysctl hw.memsize", err)
	} else if h.MemoryLimit == nil {
		h.MemoryLimit = &total
	}
	if !nodeWide {
		return
	}
	if ticks, err := hostCPUTicks(); err != nil {
		fail("host_statistics cpu", err)
	} else {
		h.CPU = &HostCPU{Seconds: darwinCPUModes(ticks), NoIOWaitSteal: true}
	}
	if total > 0 {
		readVMStatistics(h, total, fail)
	}
	if b, err := unix.SysctlRaw("vm.loadavg"); err != nil {
		fail("sysctl vm.loadavg", err)
	} else if l, err := decodeLoadavg(b); err != nil {
		fail("sysctl vm.loadavg", err)
	} else {
		h.Load = &l
	}
	if b, err := unix.SysctlRaw("vm.swapusage"); err != nil {
		fail("sysctl vm.swapusage", err)
	} else if sw, err := decodeSwapUsage(b); err != nil {
		fail("sysctl vm.swapusage", err)
	} else {
		h.Swap = &sw
	}
	files, errN := unix.SysctlUint32("kern.num_files")
	maxFiles, errM := unix.SysctlUint32("kern.maxfiles")
	if err := errors.Join(errN, errM); err != nil {
		fail("sysctl kern.num_files", err)
	} else {
		h.FileDescriptors = &CountLimit{Count: uint64(files), Limit: uint64(maxFiles)}
	}
}

func readVMStatistics(h *HostStats, total uint64, fail func(string, error)) {
	pageSize, err := hostPageSize()
	if err != nil {
		fail("host_page_size", err)
		return
	}
	b, err := hostVMStatistics64()
	if err != nil {
		fail("host_statistics64 vm", err)
		return
	}
	mem, err := decodeVMStatistics64(b, pageSize, total)
	if err != nil {
		fail("host_statistics64 vm", err)
		return
	}
	h.Memory = &mem
}
