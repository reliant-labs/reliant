// Copyright (c) 2026 Reliant Labs
package machinehealth

import (
	"math"
	"path/filepath"
	"testing"
	"time"

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

// HostStats reads /proc/loadavg and MemTotal/MemAvailable from /proc/meminfo
// (kB, in bytes on the wire); a file it cannot read leaves only its fields
// unset, and with neither there is no reading at all.
func TestHostStatsReadsLoadavgAndMeminfo(t *testing.T) {
	now := time.UnixMilli(1_763_600_000_000)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "loadavg"), "48.00 32.50 16.25 3/1234 5678\n")
	writeFile(t, filepath.Join(root, "meminfo"), "MemTotal:       131899292 kB\nMemFree:         1000 kB\nMemAvailable:   13189929 kB\nBuffers:  12 kB\n")

	st := NewHostStats(root, 48).Read(now)
	require.NotNil(t, st)
	assert.Equal(t, 48.0, st.GetLoad1())
	assert.Equal(t, 32.5, st.GetLoad5())
	assert.Equal(t, 16.25, st.GetLoad15())
	assert.Equal(t, 48.0, st.GetCpus())
	assert.Equal(t, uint64(131899292*1024), st.GetMemTotalBytes())
	assert.Equal(t, uint64(13189929*1024), st.GetMemAvailableBytes())
	assert.Equal(t, now.UnixMilli(), st.GetSampledAtMs())

	// No meminfo: load only.
	loadOnly := t.TempDir()
	writeFile(t, filepath.Join(loadOnly, "loadavg"), "1 2 3 1/1 1\n")
	st = NewHostStats(loadOnly, 0).Read(now)
	require.NotNil(t, st)
	assert.Equal(t, uint64(0), st.GetMemTotalBytes())
	assert.NotNil(t, st.Load1)

	// meminfo without MemAvailable (kernels before 3.14): no memory reading.
	oldKernel := t.TempDir()
	writeFile(t, filepath.Join(oldKernel, "meminfo"), "MemTotal: 1000 kB\nMemFree: 10 kB\n")
	assert.Nil(t, NewHostStats(oldKernel, 4).Read(now), "neither reading taken")

	// No /proc at all (macOS), and a nil reader.
	assert.Nil(t, NewHostStats(filepath.Join(t.TempDir(), "absent"), 4).Read(now))
	var nilReader *HostStats
	assert.Nil(t, nilReader.Read(now))
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

	health, volumes := HeartbeatLines(id, stats, disk, now)
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

	health, volumes := HeartbeatLines(id, nil, &reliantv1.DiskReport{
		Volumes:   []*reliantv1.DiskVolume{{Label: "home", TotalBytes: 10, FreeBytes: 1}},
		Processes: &reliantv1.ProcessReport{Count: 12},
	}, now)
	assert.Equal(t, []string{
		"machine_source", "machine", "daemon_id", "org_id",
		"processes", "disk_min_free_ratio", "disk_min_free_volume",
	}, keys(health), "an unknown process limit is not a zero")
	assert.Len(t, volumes, 1)

	health, volumes = HeartbeatLines(id, &reliantv1.MachineStats{
		Load1: f64(math.NaN()), Load5: f64(2), Cpus: 0, MemAvailableBytes: 5,
	}, nil, now)
	assert.Equal(t, []string{"machine_source", "machine", "daemon_id", "org_id", "load5"}, keys(health))
	assert.Empty(t, volumes)

	health, _ = HeartbeatLines(id, nil, nil, now)
	assert.Equal(t, []string{"machine_source", "machine", "daemon_id", "org_id"}, keys(health),
		"a heartbeat with nothing still says the machine is reporting")
}
