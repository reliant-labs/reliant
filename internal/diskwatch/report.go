// Copyright (c) 2025 Reliant Labs
package diskwatch

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// The server half: the gateway stores a heartbeat's report in
// daemons.storage_state, and the registry and the storage inbox item read it
// back. It is stored on the daemon row, not on the attachment row with the
// memory telemetry, because a full disk is still full after a disconnect —
// and so that the pressure episode (Stamp) survives one.

// Stamp returns a copy of a heartbeat's report ready to store: the server's
// clock as its reported-at, and its pressure episode. prev is the report
// stored before it (nil when none). An episode that was already running
// carries its start forward, so a reconnect, a daemon restart or a suspend
// and resume in the middle of one long spell of low disk does not begin a new
// one; a report with no volume under pressure ends it.
func Stamp(rep, prev *reliantv1.DiskReport, now time.Time) *reliantv1.DiskReport {
	cp := proto.Clone(rep).(*reliantv1.DiskReport)
	cp.ReportedAtMs = now.UnixMilli()
	switch {
	case len(UnderPressure(cp)) == 0:
		cp.PressureSinceMs = 0
	case prev.GetPressureSinceMs() > 0:
		cp.PressureSinceMs = prev.GetPressureSinceMs()
	default:
		cp.PressureSinceMs = cp.ReportedAtMs
	}
	// The disk guard's episode is carried the same way: the worktree sweep
	// re-checks a machine's kept workspaces once per episode, so a reconnect
	// mid-episode must not start a new one.
	switch {
	case !Guarding(cp):
		cp.GuardSinceMs = 0
	case prev.GetGuardSinceMs() > 0:
		cp.GuardSinceMs = prev.GetGuardSinceMs()
	default:
		cp.GuardSinceMs = cp.ReportedAtMs
	}
	// And the process guard's refusal episode: one long spell near the
	// process limit is one inbox item.
	if p := cp.GetProcesses(); p != nil {
		switch {
		case !p.RefusingHeavy:
			p.RefusingSinceMs = 0
		case prev.GetProcesses().GetRefusingSinceMs() > 0:
			p.RefusingSinceMs = prev.GetProcesses().GetRefusingSinceMs()
		default:
			p.RefusingSinceMs = cp.ReportedAtMs
		}
	}
	return cp
}

// RefusingForProcesses reports whether the machine is refusing background
// heavy commands for being near its process limit.
func RefusingForProcesses(rep *reliantv1.DiskReport) bool {
	return rep.GetProcesses().GetRefusingHeavy()
}

// Guarding reports whether the daemon's disk guard is acting on any volume:
// reclaiming, or refusing heavy commands.
func Guarding(rep *reliantv1.DiskReport) bool {
	for _, v := range rep.GetVolumes() {
		if v.Guard != reliantv1.DiskGuardState_DISK_GUARD_STATE_UNSPECIFIED {
			return true
		}
	}
	return false
}

// Encode serializes a Stamped report for storage.
func Encode(rep *reliantv1.DiskReport) (string, error) {
	b, err := protojson.Marshal(rep)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Severity grades a machine's disk picture for the storage inbox item. Within
// one pressure episode the item is raised again only when the severity climbs,
// so every step up means the machine is materially closer to failing builds.
type Severity int

const (
	// SeverityNone: no volume is under pressure.
	SeverityNone Severity = iota
	// SeverityLow: a volume is under pressure (the daemon's hysteresis bit).
	SeverityLow
	// SeverityCritical: a volume under pressure has less than its free floor
	// left, so the next build burst may not fit, or almost no inodes.
	SeverityCritical
	// SeverityFailing: a command on the machine failed with "no space left on
	// device" on a volume under pressure.
	SeverityFailing
)

// CriticalInodeFraction is the inode use at which a volume under pressure is
// critical: ENOSPC is also what a full inode table returns.
const CriticalInodeFraction = 0.98

// String is the severity's stable name; storage inbox item ids embed it.
func (s Severity) String() string {
	switch s {
	case SeverityLow:
		return "low"
	case SeverityCritical:
		return "critical"
	case SeverityFailing:
		return "failing"
	default:
		return "none"
	}
}

// SeverityOf grades a report by its worst volume under pressure.
func SeverityOf(rep *reliantv1.DiskReport) Severity {
	worst := SeverityNone
	for _, v := range UnderPressure(rep) {
		s := SeverityLow
		switch {
		case v.LastNoSpaceAtMs > 0:
			s = SeverityFailing
		case v.FreeBytes < freeFloor(v.TotalBytes),
			Usage{FilesTotal: v.FilesTotal, FilesFree: v.FilesFree}.InodeFraction() >= CriticalInodeFraction:
			s = SeverityCritical
		}
		worst = max(worst, s)
	}
	// Heavy commands refused for want of space is critical on its own: the
	// user's builds are not running, whatever the pressure bit says.
	for _, v := range rep.GetVolumes() {
		if v.Guard == reliantv1.DiskGuardState_DISK_GUARD_STATE_REFUSING_HEAVY {
			worst = max(worst, SeverityCritical)
		}
	}
	return worst
}

// Decode reads a stored report. Nil when there is none, it is undecodable, or
// it has no volumes — which includes the shape stored before reports existed
// (one worktree-root reading, path/free_bytes/total_bytes), deliberately: that
// reading measured the wrong volume, and showing it is how the UI said "fine"
// while the cache volume was full.
func Decode(stored string) *reliantv1.DiskReport {
	if strings.TrimSpace(stored) == "" {
		return nil
	}
	var rep reliantv1.DiskReport
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(stored), &rep); err != nil {
		return nil
	}
	if len(rep.Volumes) == 0 {
		return nil
	}
	return &rep
}

// Signature changes when a report differs materially from the last one
// stored: a volume's pressure, a 1%-of-the-volume step in free space or
// inodes, a fresh consumer measurement, a no-space failure, or a clean-up
// starting or finishing. The gateway writes only when it changes (or when the
// stored report is old), not on every 15s heartbeat.
func Signature(rep *reliantv1.DiskReport) string {
	if rep == nil {
		return ""
	}
	var b strings.Builder
	for _, v := range rep.Volumes {
		step := max(v.TotalBytes/100, 1)
		fstep := max(v.FilesTotal/100, 1)
		fmt.Fprintf(&b, "%s|%d|%d|%d|%s;", UrgentVolumeSignature(v), v.TotalBytes, v.FreeBytes/step, v.FilesFree/fstep, strings.Join(v.Paths, ","))
	}
	// The process count only in 5%-of-the-limit steps, and its largest tree
	// while refusing: the stored report names the trees the inbox shows.
	if p := rep.GetProcesses(); p != nil {
		step := max(p.Limit/20, 1)
		fmt.Fprintf(&b, "procs|%t|%d|%d", p.RefusingHeavy, p.Count/step, p.Limit)
		if p.RefusingHeavy && len(p.Top) > 0 {
			fmt.Fprintf(&b, "|%s|%d", p.Top[0].Command, p.Top[0].Tasks/step)
		}
	}
	return b.String()
}

// UrgentSignature changes only when the UI should hear about it now — a
// pressure flip, new consumers, a no-space failure, a clean-up starting or
// ending, the disk guard changing state or reclaiming — as opposed to free
// space drifting.
func UrgentSignature(rep *reliantv1.DiskReport) string {
	if rep == nil {
		return ""
	}
	var b strings.Builder
	for _, v := range rep.Volumes {
		b.WriteString(UrgentVolumeSignature(v))
		b.WriteByte(';')
	}
	if RefusingForProcesses(rep) {
		b.WriteString("procs-refusing")
	}
	return b.String()
}

// UrgentVolumeSignature is one volume's part of UrgentSignature.
func UrgentVolumeSignature(v *reliantv1.DiskVolume) string {
	var clearing []string
	for _, c := range v.Consumers {
		if c.Clearing {
			clearing = append(clearing, c.Kind)
		}
	}
	sort.Strings(clearing)
	return fmt.Sprintf("%s|%t|%d|%d|%s|%d|%d", v.Label, v.Pressure, v.ConsumersMeasuredAtMs, v.LastNoSpaceAtMs, strings.Join(clearing, ","), v.Guard, v.ReclaimedAtMs)
}

// UnderPressure returns the report's volumes under pressure, fullest first.
func UnderPressure(rep *reliantv1.DiskReport) []*reliantv1.DiskVolume {
	if rep == nil {
		return nil
	}
	var out []*reliantv1.DiskVolume
	for _, v := range rep.Volumes {
		if v.Pressure {
			out = append(out, v)
		}
	}
	SortFullestFirst(out)
	return out
}

// SortFullestFirst orders volumes by the fraction of space used, highest
// first.
func SortFullestFirst(vols []*reliantv1.DiskVolume) {
	used := func(v *reliantv1.DiskVolume) float64 {
		if v.TotalBytes == 0 {
			return 0
		}
		return 1 - float64(v.FreeBytes)/float64(v.TotalBytes)
	}
	sort.SliceStable(vols, func(i, j int) bool { return used(vols[i]) > used(vols[j]) })
}
