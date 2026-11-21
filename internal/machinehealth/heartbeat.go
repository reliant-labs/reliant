// Copyright (c) 2026 Reliant Labs
package machinehealth

import (
	"math"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// HeartbeatSource is machine_source on the lines written from a self-hosted
// machine's heartbeat, which tells them apart from a managed machine's own
// lines (those carry no machine_source).
const HeartbeatSource = "heartbeat"

// Identity names the machine a heartbeat line is about.
type Identity struct {
	// Machine is the daemon's name ("cicd").
	Machine  string
	DaemonID string
	OrgID    string
}

func (id Identity) attrs() []any {
	return []any{
		"machine_source", HeartbeatSource,
		"machine", id.Machine,
		"daemon_id", id.DaemonID,
		"org_id", id.OrgID,
	}
}

// HeartbeatLines returns the attributes of the `machine health` line and of
// each `machine volume` line for one heartbeat of a self-hosted machine: its
// MachineStats (nil from a daemon that predates them, or that cannot read
// /proc) and its DiskReport (nil when it sent none). now is the receiver's
// clock, against which stats_age_s is measured.
//
// The health line is returned even when the heartbeat carries no reading: its
// identity alone says the machine is reporting. As with the sampler, a reading
// that is missing is left out, never written as zero, and a value no source
// can produce (NaN, negative) is dropped.
//
// The keys are a contract with control-plane's Machine* alert rules and its
// machine-health dashboard, which query them by name in tenant zero's logs;
// TestHeartbeatLinesAttributeContract pins them.
func HeartbeatLines(id Identity, stats *reliantv1.MachineStats, disk *reliantv1.DiskReport, now time.Time) (health []any, volumes [][]any) {
	health = id.attrs()
	if stats != nil {
		cpus := stats.GetCpus()
		if !validReading(cpus) {
			cpus = 0
		}
		load := func(key string, v *float64) (float64, bool) {
			if v == nil || !validReading(*v) {
				return 0, false
			}
			health = append(health, key, round3(*v))
			return *v, true
		}
		l1, ok1 := load("load1", stats.Load1)
		l5, ok5 := load("load5", stats.Load5)
		load("load15", stats.Load15)
		if cpus > 0 {
			health = append(health, "cpus", round3(cpus))
			if ok1 {
				health = append(health, "load1_per_cpu", round3(l1/cpus))
			}
			if ok5 {
				health = append(health, "load5_per_cpu", round3(l5/cpus))
			}
		}
		if total := stats.GetMemTotalBytes(); total > 0 {
			avail := stats.GetMemAvailableBytes()
			health = append(health,
				"mem_total_bytes", total,
				"mem_available_bytes", avail,
				"mem_available_ratio", round3(float64(avail)/float64(total)))
		}
		if ms := stats.GetSampledAtMs(); ms > 0 {
			// Not clamped: a negative age is the daemon's clock running
			// ahead, which is worth seeing.
			health = append(health, "stats_age_s", int64(now.Sub(time.UnixMilli(ms)).Seconds()))
		}
	}
	if p := disk.GetProcesses(); p != nil {
		health = append(health, "processes", p.GetCount())
		if p.GetLimit() > 0 {
			health = append(health, "process_limit", p.GetLimit())
		}
	}
	sized, fullest := sizedVolumes(disk)
	for _, sv := range sized {
		volumes = append(volumes, append(id.attrs(),
			"volume", sv.v.GetLabel(),
			"paths", strings.Join(sv.v.GetPaths(), ","),
			"free_ratio", round3(sv.freeRatio),
			"used_ratio", round3(1-sv.freeRatio),
			"free_bytes", sv.v.GetFreeBytes(),
			"total_bytes", sv.v.GetTotalBytes(),
		))
	}
	if fullest != nil {
		health = append(health, "disk_min_free_ratio", round3(fullest.freeRatio), "disk_min_free_volume", fullest.v.GetLabel())
	}
	return health, volumes
}

// sizedVolume is a reported volume with a size, and its free share.
type sizedVolume struct {
	v         *reliantv1.DiskVolume
	freeRatio float64
}

// sizedVolumes returns the report's volumes that have a size, in report
// order, and the fullest of them (the first, on a tie); nil when none has.
func sizedVolumes(rep *reliantv1.DiskReport) (sized []sizedVolume, fullest *sizedVolume) {
	minRatio := math.Inf(1)
	for _, v := range rep.GetVolumes() {
		if v.GetTotalBytes() == 0 {
			continue
		}
		sized = append(sized, sizedVolume{v: v, freeRatio: float64(v.GetFreeBytes()) / float64(v.GetTotalBytes())})
	}
	for i := range sized {
		if sized[i].freeRatio < minRatio {
			minRatio, fullest = sized[i].freeRatio, &sized[i]
		}
	}
	return sized, fullest
}
