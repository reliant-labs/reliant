// Copyright (c) 2026 Reliant Labs
package machinehealth

import (
	"sync"
	"time"

	"github.com/reliant-labs/forge/pkg/resmetrics"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// Resources projects the daemon's resource samples (forge/pkg/resmetrics)
// into its heartbeat: the host's load, CPU, memory, swap and open files
// (DaemonHeartbeat.machine_stats), the daemon process's own CPU, memory, file
// descriptors and threads (process_stats), and its cgroup v2's pressure and
// throttling (cgroup_stats). Every daemon sends what its platform's readers
// produce; the gateway stores the latest and logs it for the machines it
// watches.
//
// The sampler does the reading; Resources only maps the fields and keeps the
// one rate the sample leaves to its consumer, the cgroup's throttled share,
// between consecutive samples. Safe for concurrent use (each gateway stream
// heartbeats on its own goroutine), and nil-safe.
type Resources struct {
	latest func() resmetrics.Snapshot
	cpus   float64

	mu sync.Mutex
	// at is the sample the cached throttled ratio belongs to; prevCPU the
	// cgroup cpu.stat of the sample before it.
	at        time.Time
	prevCPU   *resmetrics.CgroupCPU
	throttled *float64
}

// NewResources reads samples from latest (a resmetrics.Sampler's Latest);
// cpus is the CPU count load is divided by (hostslot.DetectMachine().CPUs),
// 0 when unknown.
func NewResources(latest func() resmetrics.Snapshot, cpus float64) *Resources {
	if !validReading(cpus) {
		cpus = 0
	}
	return &Resources{latest: latest, cpus: cpus}
}

// Read projects the latest sample. Each part is nil when the sample has
// nothing for it: no host reading on a platform whose readers are still to
// come, no cgroup outside cgroup v2. A reading the sample lacks is left out,
// never sent as zero.
func (r *Resources) Read() (machine *reliantv1.MachineStats, process *reliantv1.DaemonProcessStats, cgroup *reliantv1.DaemonCgroupStats) {
	if r == nil || r.latest == nil {
		return nil, nil, nil
	}
	snap := r.latest()
	return MachineStatsFrom(snap.Host, r.cpus, snap.At), ProcessStatsFrom(snap.Process, snap.At), r.cgroupStats(snap)
}

// cgroupStats projects snap's cgroup, with the throttled share since the
// previous sample.
func (r *Resources) cgroupStats(snap resmetrics.Snapshot) *reliantv1.DaemonCgroupStats {
	r.mu.Lock()
	if !snap.At.Equal(r.at) {
		var cur *resmetrics.CgroupCPU
		if snap.Cgroup != nil {
			cur = snap.Cgroup.CPU
		}
		r.throttled = throttledRatio(r.prevCPU, cur)
		r.at, r.prevCPU = snap.At, cur
	}
	throttled := r.throttled
	r.mu.Unlock()
	return CgroupStatsFrom(snap.Cgroup, throttled)
}

// throttledRatio is the share of enforcement periods between two cpu.stat
// readings in which the cgroup ran out of quota; nil without both readings,
// when no period elapsed (no quota) or when a counter went backwards.
func throttledRatio(prev, cur *resmetrics.CgroupCPU) *float64 {
	if prev == nil || cur == nil || cur.Periods <= prev.Periods || cur.ThrottledPeriods < prev.ThrottledPeriods {
		return nil
	}
	v := float64(cur.ThrottledPeriods-prev.ThrottledPeriods) / float64(cur.Periods-prev.Periods)
	return &v
}

// MachineStatsFrom is the heartbeat's machine_stats for a host sample taken
// at at; nil when it holds none of load, memory, CPU, swap or open files.
// MemTotalBytes and MemAvailableBytes keep their /proc/meminfo meaning (the
// machine's, not a cgroup's): they are set only when the sample has the
// machine's memory and its total is not 0.
func MachineStatsFrom(h *resmetrics.HostStats, cpus float64, at time.Time) *reliantv1.MachineStats {
	if h == nil {
		return nil
	}
	st := &reliantv1.MachineStats{Cpus: cpus, SampledAtMs: at.UnixMilli()}
	ok := false
	if l := h.Load; l != nil && validReading(l.One) && validReading(l.Five) && validReading(l.Fifteen) {
		st.Load1, st.Load5, st.Load15 = ptr(l.One), ptr(l.Five), ptr(l.Fifteen)
		ok = true
	}
	if m := h.Memory; m != nil && m.Total > 0 {
		st.MemTotalBytes, st.MemAvailableBytes = m.Total, m.Available
		ok = true
	}
	if c := h.CPU; c != nil && c.Utilization != nil {
		u := c.Utilization
		st.CpuBusyRatio, st.CpuIowaitRatio, st.CpuStealRatio = ratio(u.User+u.System), ratio(u.IOWait), ratio(u.Steal)
		ok = ok || st.CpuBusyRatio != nil
	}
	if s := h.Swap; s != nil {
		st.SwapTotalBytes, st.SwapFreeBytes = ptr(s.Total), ptr(s.Free)
		ok = true
	}
	if fd := h.FileDescriptors; fd != nil {
		st.OpenFiles, st.OpenFilesLimit = ptr(fd.Count), ptr(fd.Limit)
		ok = true
	}
	if !ok {
		return nil
	}
	return st
}

// ProcessStatsFrom is the heartbeat's process_stats for a process sample
// taken at at; nil when there is none.
func ProcessStatsFrom(p *resmetrics.ProcessStats, at time.Time) *reliantv1.DaemonProcessStats {
	if p == nil {
		return nil
	}
	st := &reliantv1.DaemonProcessStats{
		Goroutines:  int64(p.Goroutines),
		SampledAtMs: at.UnixMilli(),
		OpenFds:     p.FDCount,
		FdLimit:     p.FDLimit,
		Threads:     p.Threads,
	}
	if !p.StartTime.IsZero() {
		st.StartedAtMs = p.StartTime.UnixMilli()
	}
	if p.CPU != nil && p.CPU.Utilization != nil && validReading(*p.CPU.Utilization) {
		st.CpuCores = ptr(*p.CPU.Utilization)
	}
	if p.Memory != nil {
		st.RssBytes = ptr(p.Memory.RSS)
	}
	return st
}

// CgroupStatsFrom is the heartbeat's cgroup_stats for a cgroup sample, with
// the throttled share the caller measured; nil when there is no cgroup or it
// holds none of the fields.
func CgroupStatsFrom(c *resmetrics.CgroupStats, throttled *float64) *reliantv1.DaemonCgroupStats {
	if c == nil {
		return nil
	}
	st := &reliantv1.DaemonCgroupStats{
		CpuLimitCores: c.CPULimit,
		PidsCurrent:   c.PidsCurrent,
		PidsMax:       c.PidsMax,
	}
	if throttled != nil {
		st.CpuThrottledRatio = ratio(*throttled)
	}
	if p := c.Pressure.CPU; p != nil {
		st.CpuSomeAvg10 = ratio(p.Some.Avg10)
	}
	if p := c.Pressure.Memory; p != nil {
		st.MemorySomeAvg10 = ratio(p.Some.Avg10)
		if p.Full != nil {
			st.MemoryFullAvg10 = ratio(p.Full.Avg10)
		}
	}
	if p := c.Pressure.IO; p != nil {
		st.IoSomeAvg10 = ratio(p.Some.Avg10)
		if p.Full != nil {
			st.IoFullAvg10 = ratio(p.Full.Avg10)
		}
	}
	if st.CpuLimitCores == nil && st.PidsCurrent == nil && st.PidsMax == nil && st.CpuThrottledRatio == nil &&
		st.CpuSomeAvg10 == nil && st.MemorySomeAvg10 == nil && st.IoSomeAvg10 == nil {
		return nil
	}
	return st
}

// ratio is v when it is a share a source can produce (0 to 1), else nil.
func ratio(v float64) *float64 {
	if !validReading(v) || v > 1 {
		return nil
	}
	return &v
}

func ptr[T any](v T) *T { return &v }
