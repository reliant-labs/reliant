// Copyright (c) 2026 Reliant Labs
package machinehealth

import (
	"math"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/resmetrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

func keys(kv []any) []string {
	var out []string
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, kv[i].(string))
	}
	return out
}

func f64(v float64) *float64 { return &v }
func i64(v int64) *int64     { return &v }
func u64(v uint64) *uint64   { return &v }

// Resources maps a resmetrics sample onto the heartbeat: machine_stats keeps
// its /proc meaning (the machine's load and MemTotal/MemAvailable) and gains
// CPU, swap and open files; the process and cgroup parts carry the daemon's
// own figures. A reading the sample lacks is left out, and a sample with
// nothing for a part leaves that part nil.
func TestResourcesProjectsTheSample(t *testing.T) {
	at := time.UnixMilli(1_763_600_000_000)
	snap := resmetrics.Snapshot{
		At: at,
		Host: &resmetrics.HostStats{
			Scope:  resmetrics.ScopeHost,
			Load:   &resmetrics.LoadAverage{One: 48, Five: 32.5, Fifteen: 16.25},
			Memory: &resmetrics.HostMemory{Total: 131899292 * 1024, Available: 13189929 * 1024, Free: 1000 * 1024},
			CPU: &resmetrics.HostCPU{Utilization: &resmetrics.CPUModes{
				User: 0.5, System: 0.25, Idle: 0.15, IOWait: 0.06, Steal: 0.04,
			}},
			Swap:            &resmetrics.Swap{Total: 8 << 30, Free: 6 << 30},
			FileDescriptors: &resmetrics.CountLimit{Count: 4200, Limit: 9_000_000},
		},
		Process: &resmetrics.ProcessStats{
			StartTime:  at.Add(-time.Hour),
			Goroutines: 321,
			CPU:        &resmetrics.ProcessCPU{Utilization: f64(1.5)},
			Memory:     &resmetrics.ProcessMemory{RSS: 512 << 20},
			Threads:    i64(40),
			FDCount:    i64(97),
			FDLimit:    i64(1 << 20),
		},
		Cgroup: &resmetrics.CgroupStats{
			CPU:      &resmetrics.CgroupCPU{Periods: 1000, ThrottledPeriods: 100},
			CPULimit: f64(4),
			Pressure: resmetrics.Pressure{
				CPU:    &resmetrics.PSI{Some: resmetrics.PSILine{Avg10: 0.3}},
				Memory: &resmetrics.PSI{Some: resmetrics.PSILine{Avg10: 0.2}, Full: &resmetrics.PSILine{Avg10: 0.1}},
				IO:     &resmetrics.PSI{Some: resmetrics.PSILine{Avg10: 0.05}},
			},
			PidsCurrent: u64(300),
		},
	}
	r := NewResources(func() resmetrics.Snapshot { return snap }, 48)

	m, p, c := r.Read()
	require.NotNil(t, m)
	assert.Equal(t, 48.0, m.GetLoad1())
	assert.Equal(t, 32.5, m.GetLoad5())
	assert.Equal(t, 16.25, m.GetLoad15())
	assert.Equal(t, 48.0, m.GetCpus())
	assert.Equal(t, uint64(131899292*1024), m.GetMemTotalBytes())
	assert.Equal(t, uint64(13189929*1024), m.GetMemAvailableBytes())
	assert.Equal(t, at.UnixMilli(), m.GetSampledAtMs())
	assert.InDelta(t, 0.75, m.GetCpuBusyRatio(), 1e-9, "user and system")
	assert.InDelta(t, 0.06, m.GetCpuIowaitRatio(), 1e-9)
	assert.InDelta(t, 0.04, m.GetCpuStealRatio(), 1e-9)
	assert.Equal(t, uint64(8<<30), m.GetSwapTotalBytes())
	assert.Equal(t, uint64(6<<30), m.GetSwapFreeBytes())
	assert.Equal(t, uint64(4200), m.GetOpenFiles())
	assert.Equal(t, uint64(9_000_000), m.GetOpenFilesLimit())

	require.NotNil(t, p)
	assert.Equal(t, 1.5, p.GetCpuCores())
	assert.Equal(t, uint64(512<<20), p.GetRssBytes())
	assert.Equal(t, int64(97), p.GetOpenFds())
	assert.Equal(t, int64(1<<20), p.GetFdLimit())
	assert.Equal(t, int64(40), p.GetThreads())
	assert.Equal(t, int64(321), p.GetGoroutines())
	assert.Equal(t, at.Add(-time.Hour).UnixMilli(), p.GetStartedAtMs())

	require.NotNil(t, c)
	assert.Equal(t, 0.3, c.GetCpuSomeAvg10())
	assert.Equal(t, 0.2, c.GetMemorySomeAvg10())
	assert.Equal(t, 0.1, c.GetMemoryFullAvg10())
	assert.Equal(t, 0.05, c.GetIoSomeAvg10())
	assert.Nil(t, c.IoFullAvg10, "no full line")
	assert.Equal(t, 4.0, c.GetCpuLimitCores())
	assert.Equal(t, uint64(300), c.GetPidsCurrent())
	assert.Nil(t, c.PidsMax, "pids.max is max")
	assert.Nil(t, c.CpuThrottledRatio, "no earlier sample to measure from")

	// The next sample: 200 more periods, 50 of them throttled. The same
	// sample read again keeps the same ratio.
	snap.At = at.Add(10 * time.Second)
	snap.Cgroup.CPU = &resmetrics.CgroupCPU{Periods: 1200, ThrottledPeriods: 150}
	_, _, c = r.Read()
	assert.Equal(t, 0.25, c.GetCpuThrottledRatio())
	_, _, c = r.Read()
	assert.Equal(t, 0.25, c.GetCpuThrottledRatio(), "a re-read of one sample is not a new interval")
}

// A sample from a platform with no readers yet carries only the runtime's
// figures: no machine_stats (it would be all zeros), no cgroup; the process
// part still says how many goroutines run. A first sample has no rates.
func TestResourcesLeaveOutWhatTheSampleLacks(t *testing.T) {
	at := time.UnixMilli(1_763_600_000_000)
	bare := resmetrics.Snapshot{
		At:      at,
		Host:    &resmetrics.HostStats{Scope: resmetrics.ScopeHost, LogicalCPUs: 8},
		Process: &resmetrics.ProcessStats{Goroutines: 12, CPU: &resmetrics.ProcessCPU{}},
	}
	m, p, c := NewResources(func() resmetrics.Snapshot { return bare }, 8).Read()
	assert.Nil(t, m)
	assert.Nil(t, c)
	require.NotNil(t, p)
	assert.Equal(t, int64(12), p.GetGoroutines())
	assert.Nil(t, p.CpuCores)
	assert.Nil(t, p.RssBytes)
	assert.Nil(t, p.OpenFds)

	// Load alone, NaN memory figures aside: a reading no kernel produces is
	// dropped, and a zero total is no memory reading.
	loadOnly := resmetrics.Snapshot{At: at, Host: &resmetrics.HostStats{
		Load:   &resmetrics.LoadAverage{One: 1, Five: 2, Fifteen: 3},
		Memory: &resmetrics.HostMemory{},
		CPU:    &resmetrics.HostCPU{Utilization: &resmetrics.CPUModes{User: math.NaN()}},
	}}
	m, _, _ = NewResources(func() resmetrics.Snapshot { return loadOnly }, 0).Read()
	require.NotNil(t, m)
	assert.Equal(t, 1.0, m.GetLoad1())
	assert.Equal(t, uint64(0), m.GetMemTotalBytes())
	assert.Nil(t, m.CpuBusyRatio)
	assert.Nil(t, m.SwapTotalBytes)

	// Neither groups sampled, and a nil projector.
	m, p, c = NewResources(func() resmetrics.Snapshot { return resmetrics.Snapshot{At: at} }, 4).Read()
	assert.Nil(t, m)
	assert.Nil(t, p)
	assert.Nil(t, c)
	var nilResources *Resources
	m, p, c = nilResources.Read()
	assert.Nil(t, m)
	assert.Nil(t, p)
	assert.Nil(t, c)
}

// A counter that goes backwards (a cgroup recreated under the daemon) or an
// interval with no enforcement period has no throttled share.
func TestThrottledRatio(t *testing.T) {
	cpu := func(periods, throttled uint64) *resmetrics.CgroupCPU {
		return &resmetrics.CgroupCPU{Periods: periods, ThrottledPeriods: throttled}
	}
	assert.Nil(t, throttledRatio(nil, cpu(10, 1)))
	assert.Nil(t, throttledRatio(cpu(10, 1), nil))
	assert.Nil(t, throttledRatio(cpu(10, 1), cpu(10, 1)), "no period elapsed")
	assert.Nil(t, throttledRatio(cpu(10, 5), cpu(20, 4)), "throttled went backwards")
	assert.Nil(t, throttledRatio(cpu(10, 1), cpu(5, 1)), "periods went backwards")
	got := throttledRatio(cpu(10, 1), cpu(20, 1))
	require.NotNil(t, got)
	assert.Equal(t, 0.0, *got)
}

// TestHeartbeatLinesAttributeContract pins the exact keys of the lines the
// gateway writes for a self-hosted machine. control-plane's Machine* alert
// SQL (MachineDiskFilling/Full, MachineMemoryLow, MachineLoadSustained,
// MachineNotReporting) and its machine-health dashboard read these keys by
// name: a rename here breaks them silently, so change both together.
func TestHeartbeatLinesAttributeContract(t *testing.T) {
	now := time.Date(2026, 11, 20, 6, 0, 0, 0, time.UTC)
	id := Identity{Machine: "cicd", DaemonID: "d-1", OrgID: "org-0"}
	stats := &reliantv1.MachineStats{
		Load1: f64(72), Load5: f64(48), Load15: f64(24.1234),
		Cpus:          48,
		MemTotalBytes: 1000, MemAvailableBytes: 80,
		SampledAtMs: now.Add(-7 * time.Second).UnixMilli(),
	}
	disk := &reliantv1.DiskReport{
		Volumes: []*reliantv1.DiskVolume{
			{Label: "home", Paths: []string{"/home/ubuntu"}, TotalBytes: 1000, FreeBytes: 500},
			{Label: "build caches & docker", Paths: []string{"/var/cache/go-build", "/var/lib/docker"}, TotalBytes: 1000, FreeBytes: 150},
			{Label: "unsized"},
		},
		Processes: &reliantv1.ProcessReport{Count: 900, Limit: 4096},
	}

	// A heartbeat of a daemon that sends only the original readings: the
	// keys the alert rules read, exactly.
	health, volumes := HeartbeatLines(id, &reliantv1.DaemonHeartbeat{MachineStats: stats, Disk: disk}, now)
	assert.Equal(t, []string{
		"machine_source", "machine", "daemon_id", "org_id",
		"load1", "load5", "load15", "cpus", "load1_per_cpu", "load5_per_cpu",
		"mem_total_bytes", "mem_available_bytes", "mem_available_ratio",
		"stats_age_s",
		"processes", "process_limit",
		"disk_min_free_ratio", "disk_min_free_volume",
	}, keys(health))
	h := attrs(health)
	assert.Equal(t, HeartbeatSource, h["machine_source"])
	assert.Equal(t, "heartbeat", h["machine_source"])
	assert.Equal(t, "cicd", h["machine"])
	assert.Equal(t, "d-1", h["daemon_id"])
	assert.Equal(t, "org-0", h["org_id"])
	assert.Equal(t, 24.123, h["load15"])
	assert.Equal(t, 48.0, h["cpus"])
	assert.Equal(t, 1.5, h["load1_per_cpu"])
	assert.Equal(t, 1.0, h["load5_per_cpu"])
	assert.Equal(t, uint64(1000), h["mem_total_bytes"])
	assert.Equal(t, uint64(80), h["mem_available_bytes"])
	assert.Equal(t, 0.08, h["mem_available_ratio"])
	assert.Equal(t, int64(7), h["stats_age_s"])
	assert.Equal(t, uint64(900), h["processes"])
	assert.Equal(t, uint64(4096), h["process_limit"])
	assert.Equal(t, 0.15, h["disk_min_free_ratio"])
	assert.Equal(t, "build caches & docker", h["disk_min_free_volume"])

	require.Len(t, volumes, 2, "a volume with no size is not reported")
	for _, v := range volumes {
		assert.Equal(t, []string{
			"machine_source", "machine", "daemon_id", "org_id",
			"volume", "paths", "free_ratio", "used_ratio", "free_bytes", "total_bytes",
		}, keys(v))
	}
	v := attrs(volumes[1])
	assert.Equal(t, "heartbeat", v["machine_source"])
	assert.Equal(t, "cicd", v["machine"])
	assert.Equal(t, "build caches & docker", v["volume"])
	assert.Equal(t, "/var/cache/go-build,/var/lib/docker", v["paths"])
	assert.Equal(t, 0.15, v["free_ratio"])
	assert.Equal(t, 0.85, v["used_ratio"])
	assert.Equal(t, uint64(150), v["free_bytes"])
	assert.Equal(t, uint64(1000), v["total_bytes"])
}

// A missing reading is left out, never logged as zero: an old daemon (no
// stats) still logs its identity, disk and processes; no CPU count means no
// per-CPU load; a value no kernel produces is dropped.
func TestHeartbeatLinesOmitMissingReadings(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	id := Identity{Machine: "ovh-build-1", DaemonID: "d-2", OrgID: "org-0"}

	health, volumes := HeartbeatLines(id, &reliantv1.DaemonHeartbeat{Disk: &reliantv1.DiskReport{
		Volumes:   []*reliantv1.DiskVolume{{Label: "home", TotalBytes: 10, FreeBytes: 1}},
		Processes: &reliantv1.ProcessReport{Count: 12},
	}}, now)
	assert.Equal(t, []string{
		"machine_source", "machine", "daemon_id", "org_id",
		"processes", "disk_min_free_ratio", "disk_min_free_volume",
	}, keys(health), "an unknown process limit is not a zero")
	assert.Len(t, volumes, 1)

	health, volumes = HeartbeatLines(id, &reliantv1.DaemonHeartbeat{MachineStats: &reliantv1.MachineStats{
		Load1: f64(math.NaN()), Load5: f64(2), Cpus: 0, MemAvailableBytes: 5, CpuBusyRatio: f64(-1),
	}, ProcessStats: &reliantv1.DaemonProcessStats{OpenFds: i64(-1)}}, now)
	assert.Equal(t, []string{"machine_source", "machine", "daemon_id", "org_id", "load5"}, keys(health))
	assert.Empty(t, volumes)

	health, _ = HeartbeatLines(id, nil, now)
	assert.Equal(t, []string{"machine_source", "machine", "daemon_id", "org_id"}, keys(health),
		"a heartbeat with nothing still says the machine is reporting")
}

// TestHeartbeatLinesResourceKeys pins the keys the process, cgroup and newer
// machine readings add to the health line, after the original ones and before
// the disk's. Like the original keys, dashboards may query them by name.
func TestHeartbeatLinesResourceKeys(t *testing.T) {
	now := time.Date(2026, 11, 21, 6, 0, 0, 0, time.UTC)
	id := Identity{Machine: "cicd", DaemonID: "d-1", OrgID: "org-0"}
	hb := &reliantv1.DaemonHeartbeat{
		MachineStats: &reliantv1.MachineStats{
			Load1: f64(4), Load5: f64(4), Load15: f64(4), Cpus: 8,
			MemTotalBytes: 1000, MemAvailableBytes: 500, SampledAtMs: now.UnixMilli(),
			CpuBusyRatio: f64(0.61234), CpuIowaitRatio: f64(0.02), CpuStealRatio: f64(0),
			SwapTotalBytes: u64(2048), SwapFreeBytes: u64(1024),
			OpenFiles: u64(3000), OpenFilesLimit: u64(100000),
		},
		ProcessStats: &reliantv1.DaemonProcessStats{
			CpuCores: f64(0.25), RssBytes: u64(300 << 20), OpenFds: i64(64), FdLimit: i64(1024), Threads: i64(30),
			Goroutines: 200,
		},
		CgroupStats: &reliantv1.DaemonCgroupStats{
			CpuSomeAvg10: f64(0.1), MemorySomeAvg10: f64(0.2), MemoryFullAvg10: f64(0.05),
			IoSomeAvg10: f64(0.3), IoFullAvg10: f64(0.15), CpuLimitCores: f64(4), CpuThrottledRatio: f64(0.5),
			PidsCurrent: u64(120), PidsMax: u64(4096),
		},
		Disk: &reliantv1.DiskReport{Volumes: []*reliantv1.DiskVolume{{Label: "home", TotalBytes: 10, FreeBytes: 5}}},
	}
	health, _ := HeartbeatLines(id, hb, now)
	assert.Equal(t, []string{
		"machine_source", "machine", "daemon_id", "org_id",
		"load1", "load5", "load15", "cpus", "load1_per_cpu", "load5_per_cpu",
		"mem_total_bytes", "mem_available_bytes", "mem_available_ratio",
		"stats_age_s",
		"cpu_busy_ratio", "cpu_iowait_ratio", "cpu_steal_ratio",
		"swap_total_bytes", "swap_free_bytes", "open_files", "open_files_limit",
		"daemon_cpu_cores", "daemon_rss_bytes", "daemon_open_fds", "daemon_fd_limit", "daemon_threads",
		"cgroup_psi_cpu_some_avg10", "cgroup_psi_memory_some_avg10", "cgroup_psi_memory_full_avg10",
		"cgroup_psi_io_some_avg10", "cgroup_psi_io_full_avg10",
		"cgroup_cpu_limit_cores", "cgroup_cpu_throttled_ratio", "cgroup_pids", "cgroup_pids_max",
		"disk_min_free_ratio", "disk_min_free_volume",
	}, keys(health))
	h := attrs(health)
	assert.Equal(t, 0.612, h["cpu_busy_ratio"])
	assert.Equal(t, uint64(1024), h["swap_free_bytes"])
	assert.Equal(t, uint64(100000), h["open_files_limit"])
	assert.Equal(t, 0.25, h["daemon_cpu_cores"])
	assert.Equal(t, uint64(300<<20), h["daemon_rss_bytes"])
	assert.Equal(t, int64(64), h["daemon_open_fds"])
	assert.Equal(t, int64(1024), h["daemon_fd_limit"])
	assert.Equal(t, int64(30), h["daemon_threads"])
	assert.Equal(t, 0.5, h["cgroup_cpu_throttled_ratio"])
	assert.Equal(t, uint64(4096), h["cgroup_pids_max"])
}
