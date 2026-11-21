package resmetrics

import "time"

// Snapshot is one sample of the process and of its host. Every group, and
// every reader's part of a group, is a pointer that is nil when it was not
// asked for or could not be read this time; Errors says which reader failed
// and why. A failed reader never fails the sample.
type Snapshot struct {
	// At is when the sample was taken.
	At time.Time
	// Process is the binary itself; nil when the process group is off.
	Process *ProcessStats
	// Host is the machine, or only what the container may see of it (see
	// HostStats.Scope); nil when the host group is off.
	Host *HostStats
	// Cgroup is the process's cgroup v2; nil when the host group is off or
	// the process is in no readable cgroup v2 (every non-linux platform).
	Cgroup *CgroupStats
	// Errors holds one entry per reader that failed, keyed by the reader's
	// name ("process.stat", "host.meminfo", "cgroup.memory.stat", ...). It is
	// for logs and debugging; nil when every reader succeeded.
	Errors map[string]error
}

// ProcessStats is the binary's own resource use. The runtime fields and
// StartTime are always set; each pointer is nil when its reader failed.
type ProcessStats struct {
	// StartTime is when the process started: from the kernel on linux, and
	// otherwise when this package was initialised, which is within the
	// binary's start-up.
	StartTime time.Time
	// Goroutines is runtime.NumGoroutine at the sample.
	Goroutines int
	// GoMappedBytes is the memory the Go runtime has mapped and not returned
	// to the OS (runtime/metrics /memory/classes/total:bytes minus
	// /memory/classes/heap/released:bytes).
	GoMappedBytes uint64

	// CPU is the CPU time the process has used, and its rate.
	CPU *ProcessCPU
	// Faults is the page faults the process has taken.
	Faults *PageFaults
	// Memory is the resident and virtual size.
	Memory *ProcessMemory
	// Threads is the number of OS threads.
	Threads *int64
	// ContextSwitches is the scheduler's switches away from the process.
	ContextSwitches *ContextSwitches
	// FDCount is the number of open file descriptors.
	FDCount *int64
	// FDLimit is the soft RLIMIT_NOFILE.
	FDLimit *int64
	// DiskIO is the bytes the process caused to be read from and written to
	// storage.
	DiskIO *DiskIO
}

// Uptime is how long the process has run at the sample's time.
func (p ProcessStats) Uptime(at time.Time) time.Duration { return at.Sub(p.StartTime) }

// ProcessCPU is the CPU time a process has used since it started.
type ProcessCPU struct {
	UserSeconds   float64
	SystemSeconds float64
	// Utilization is the CPU used between the previous sample and this one,
	// in cores (it exceeds 1 when more than one core is busy); nil on the
	// first sample.
	Utilization *float64
}

// PageFaults counts page faults since the process started.
type PageFaults struct {
	Minor uint64
	Major uint64
}

// ProcessMemory is the size of the process.
type ProcessMemory struct {
	// RSS is the resident set size in bytes.
	RSS uint64
	// Virtual is the virtual memory size in bytes.
	Virtual uint64
	// GoGap is RSS minus the Go runtime's mapped memory: what cgo, mmap and
	// fragmentation hold. It is negative when the runtime has mapped more
	// than is resident.
	GoGap int64
}

// ContextSwitches counts the scheduler's switches since the process started.
type ContextSwitches struct {
	Voluntary   uint64
	Involuntary uint64
}

// DiskIO is storage traffic since the process started, in bytes.
type DiskIO struct {
	ReadBytes  uint64
	WriteBytes uint64
}

// Scope says which view of the machine HostStats reports.
type Scope string

const (
	// ScopeHost is the whole machine: a host process, a VM, a laptop.
	ScopeHost Scope = "host"
	// ScopeCgroup is the container's share only: node-wide CPU, load, memory,
	// swap and system-wide counts are left out, because every pod on the node
	// would report the same node under its own name.
	ScopeCgroup Scope = "cgroup"
)

// HostStats is the machine the process runs on. Each pointer is nil when its
// reader failed or Scope leaves it out.
type HostStats struct {
	// Scope is the view reported.
	Scope Scope
	// LogicalCPUs is the CPUs the process may run on: runtime.NumCPU,
	// lowered to the cgroup's CPU quota when one is set.
	LogicalCPUs int

	// CPU is the machine's CPU time by mode and its rate.
	CPU *HostCPU
	// Load is the run-queue load average.
	Load *LoadAverage
	// Memory is the machine's memory.
	Memory *HostMemory
	// MemoryLimit is what the process may use: the cgroup's memory.max when
	// one is set, otherwise the machine's total. Set in either scope.
	MemoryLimit *uint64
	// Swap is the machine's swap space.
	Swap *Swap
	// Filesystems are the volumes the process writes to, at most
	// maxVolumes, one per device.
	Filesystems []Filesystem
	// FileDescriptors is the system-wide count and limit of open files.
	FileDescriptors *CountLimit
	// Processes is the number of processes against the kernel's pid limit.
	Processes *CountLimit
}

// CPUModes is a value per CPU mode. Nice time is counted as user, and
// interrupt time (irq and softirq) as system, so the five add up to the whole.
type CPUModes struct {
	User   float64
	System float64
	Idle   float64
	IOWait float64
	Steal  float64
}

// HostCPU is the machine's CPU time since boot, summed over every CPU.
type HostCPU struct {
	// Seconds is the time spent in each mode.
	Seconds CPUModes
	// Utilization is each mode's share of all CPU time between the previous
	// sample and this one, from 0 to 1; nil on the first sample.
	Utilization *CPUModes
}

// LoadAverage is the kernel's run-queue load average.
type LoadAverage struct {
	One, Five, Fifteen float64
}

// HostMemory is the machine's memory in bytes. Used, Free and Cached add up to
// Total.
type HostMemory struct {
	Total uint64
	Free  uint64
	// Available is the kernel's estimate of memory a new workload can use
	// without swapping (MemAvailable).
	Available uint64
	// Cached is page cache, buffers and reclaimable slab.
	Cached uint64
	// Used is Total minus Free and Cached.
	Used uint64
}

// Swap is the machine's swap space in bytes.
type Swap struct {
	Total, Free uint64
}

// Filesystem is one volume the process writes to, named by its role and never
// by its path.
type Filesystem struct {
	// Role is "cwd" or "tmp".
	Role string
	// UsedBytes is the space in use; FreeBytes the space available to an
	// unprivileged process. The blocks reserved for root are in neither.
	UsedBytes, FreeBytes uint64
}

// CountLimit is a count of something against the kernel's limit on it.
type CountLimit struct {
	Count, Limit uint64
}

// CgroupStats is the process's cgroup v2. Each pointer is nil when its file
// could not be read; a limit is also nil when it is "max".
type CgroupStats struct {
	// MemoryCurrent is memory.current in bytes.
	MemoryCurrent *uint64
	// MemoryStat is the parts of memory.stat a working set is computed from.
	MemoryStat *CgroupMemoryStat
	// MemoryMax is memory.max in bytes.
	MemoryMax *uint64
	// OOMKills is memory.events oom_kill: processes the kernel killed for
	// this cgroup's memory limit.
	OOMKills *uint64
	// CPU is cpu.stat.
	CPU *CgroupCPU
	// CPULimit is cpu.max as cores (quota over period).
	CPULimit *float64
	// Pressure is the cgroup's pressure stall information.
	Pressure Pressure
	// PidsCurrent and PidsMax are pids.current and pids.max.
	PidsCurrent *uint64
	PidsMax     *uint64
}

// WorkingSet is memory.current minus inactive_file, the kubelet's definition;
// ok is false when either part is missing.
func (c CgroupStats) WorkingSet() (bytes uint64, ok bool) {
	if c.MemoryCurrent == nil || c.MemoryStat == nil {
		return 0, false
	}
	if c.MemoryStat.InactiveFile >= *c.MemoryCurrent {
		return 0, true
	}
	return *c.MemoryCurrent - c.MemoryStat.InactiveFile, true
}

// CgroupMemoryStat is the reclaimable parts of memory.stat, in bytes.
type CgroupMemoryStat struct {
	Anon            uint64
	File            uint64
	ActiveFile      uint64
	InactiveFile    uint64
	SlabReclaimable uint64
}

// CgroupCPU is cpu.stat.
type CgroupCPU struct {
	UsageSeconds float64
	// Periods is the enforcement periods that have elapsed with a quota set;
	// ThrottledPeriods the ones in which the cgroup ran out of quota.
	Periods          uint64
	ThrottledPeriods uint64
	ThrottledSeconds float64
}

// Pressure is pressure stall information for each resource; nil when the
// file could not be read.
type Pressure struct {
	CPU, Memory, IO *PSI
}

// PSI is one resource's pressure: "some" is time at least one task stalled on
// it, "full" time every task did (nil when the kernel reports no full line).
type PSI struct {
	Some PSILine
	Full *PSILine
}

// PSILine is one line of a pressure file.
type PSILine struct {
	// Avg10 is the share of the last ten seconds stalled, from 0 to 1.
	Avg10 float64
	// TotalSeconds is the stall time since the cgroup was created.
	TotalSeconds float64
}
