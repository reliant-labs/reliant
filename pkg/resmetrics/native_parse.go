package resmetrics

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// The decoders of the darwin and windows readers. They build on every
// platform, so their tests run on linux, where the readers that call them do
// not. Every darwin structure is the 64-bit layout, little-endian on both
// amd64 and arm64.

// darwinCLKTCK is the tick rate of darwin's host CPU counters
// (HOST_CPU_LOAD_INFO): CLK_TCK in <time.h>, fixed at 100.
const darwinCLKTCK = 100

// sizeofTaskInfo is sizeof(struct proc_taskinfo) (<sys/proc_info.h>),
// PROC_PIDTASKINFO_SIZE.
const sizeofTaskInfo = 96

// decodeTaskInfo reads struct proc_taskinfo: pti_virtual_size at 0,
// pti_resident_size at 8, pti_threadnum (int32) at 84.
func decodeTaskInfo(b []byte) (rss, virt uint64, threads int64, err error) {
	if len(b) < sizeofTaskInfo {
		return 0, 0, 0, fmt.Errorf("proc_taskinfo: %d bytes, want %d", len(b), sizeofTaskInfo)
	}
	le := binary.LittleEndian
	virt, rss = le.Uint64(b[0:]), le.Uint64(b[8:])
	threads = int64(int32(le.Uint32(b[84:]))) //nolint:gosec // pti_threadnum is an int32
	if threads < 1 {
		return 0, 0, 0, fmt.Errorf("proc_taskinfo: %d threads", threads)
	}
	return rss, virt, threads, nil
}

// decodeLoadavg reads sysctl vm.loadavg, struct loadavg: three fixed-point
// uint32 averages, 4 bytes of padding, then the int64 scale.
func decodeLoadavg(b []byte) (LoadAverage, error) {
	if len(b) < 24 {
		return LoadAverage{}, fmt.Errorf("vm.loadavg: %d bytes, want 24", len(b))
	}
	le := binary.LittleEndian
	scale := int64(le.Uint64(b[16:])) //nolint:gosec // fscale is a C long
	if scale <= 0 {
		return LoadAverage{}, fmt.Errorf("vm.loadavg: scale %d", scale)
	}
	f := func(off int) float64 { return float64(le.Uint32(b[off:])) / float64(scale) }
	return LoadAverage{One: f(0), Five: f(4), Fifteen: f(8)}, nil
}

// decodeSwapUsage reads sysctl vm.swapusage, struct xsw_usage: xsu_total,
// xsu_avail and xsu_used (uint64), then the page size and the encrypted flag.
func decodeSwapUsage(b []byte) (Swap, error) {
	if len(b) < 32 {
		return Swap{}, fmt.Errorf("vm.swapusage: %d bytes, want 32", len(b))
	}
	le := binary.LittleEndian
	total, avail := le.Uint64(b[0:]), le.Uint64(b[8:])
	if avail > total {
		return Swap{}, fmt.Errorf("vm.swapusage: %d available of %d", avail, total)
	}
	return Swap{Total: total, Free: avail}, nil
}

// darwinCPUModes converts host_cpu_load_info's ticks (user, system, idle,
// nice; CPU_STATE_*) to seconds. Nice is counted as user, as on linux;
// darwin accounts no iowait or steal time.
func darwinCPUModes(ticks [4]uint32) CPUModes {
	s := func(t uint64) float64 { return float64(t) / darwinCLKTCK }
	return CPUModes{
		User:   s(uint64(ticks[0]) + uint64(ticks[3])),
		System: s(uint64(ticks[1])),
		Idle:   s(uint64(ticks[2])),
	}
}

// sizeofVMStatistics64 is sizeof(vm_statistics64_data_t) (<mach/vm_statistics.h>).
const sizeofVMStatistics64 = 152

// decodeVMStatistics64 turns vm_statistics64 into the machine's memory:
// free_count (natural_t, at 0, which includes speculative pages) is free,
// external_page_count (at 136, file-backed pages) plus purgeable_count (at
// 88) is cached, and the rest of total is used.
func decodeVMStatistics64(b []byte, pageSize, total uint64) (HostMemory, error) {
	if len(b) < sizeofVMStatistics64 {
		return HostMemory{}, fmt.Errorf("vm_statistics64: %d bytes, want %d", len(b), sizeofVMStatistics64)
	}
	if pageSize == 0 || total == 0 {
		return HostMemory{}, errors.New("vm_statistics64: no page size or total memory")
	}
	le := binary.LittleEndian
	free := uint64(le.Uint32(b[0:])) * pageSize
	cached := (uint64(le.Uint32(b[136:])) + uint64(le.Uint32(b[88:]))) * pageSize
	free = min(free, total)
	cached = min(cached, total-free)
	return HostMemory{Total: total, Free: free, Cached: cached, Used: total - free - cached, Available: free + cached}, nil
}

// filetimeSeconds is a windows FILETIME duration (100ns units, split into two
// uint32 halves) in seconds.
func filetimeSeconds(low, high uint32) float64 {
	return float64(uint64(high)<<32|uint64(low)) / 1e7
}

// windowsCPUModes converts GetSystemTimes' totals, in seconds, to modes:
// kernel time includes idle time, so system is kernel minus idle. windows
// accounts no iowait or steal time.
func windowsCPUModes(idle, kernel, user float64) CPUModes {
	return CPUModes{User: user, System: max(kernel-idle, 0), Idle: idle}
}

// windowsMemory splits the machine's memory from GlobalMemoryStatusEx (total
// and available physical memory) and GetPerformanceInfo (the system cache):
// available memory is free plus standby, and the system cache's share of it
// is cached.
func windowsMemory(total, avail, systemCache uint64) HostMemory {
	avail = min(avail, total)
	cached := min(systemCache, avail)
	return HostMemory{Total: total, Free: avail - cached, Cached: cached, Used: total - avail, Available: avail}
}
