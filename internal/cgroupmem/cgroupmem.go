// Copyright (c) 2025 Reliant Labs

// Package cgroupmem reads cgroup v2 memory accounting for the container the
// daemon runs in, classifies child-process deaths as kernel OOM kills, and
// watches for memory pressure so it can be reported to the gateway before
// the OOM killer fires.
//
// Cloud workspace daemons run inside a k8s pod whose cgroup is shared with
// dockerd and every process tool calls spawn. Inside the container the pod's
// cgroup v2 controllers are mounted at /sys/fs/cgroup, so:
//
//	/sys/fs/cgroup/memory.current — bytes currently charged to the cgroup
//	                                (page cache and kernel slab included)
//	/sys/fs/cgroup/memory.stat    — breakdown of memory.current; inactive_file
//	                                and slab_reclaimable are what the kernel
//	                                frees before it considers an OOM kill
//	/sys/fs/cgroup/memory.max     — limit in bytes, or "max" (unlimited)
//	/sys/fs/cgroup/memory.peak    — high-watermark (kernel >= 5.19; optional)
//	/sys/fs/cgroup/memory.events  — flat keyed counters, incl. "oom_kill"
//
// Everything degrades gracefully when those files are absent (macOS, local
// Linux daemons outside a limited cgroup): readers report not-available and
// all checks answer false.
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
//
//forge:exclude-contract: reads cgroup v2 files for the daemon's own container and classifies OOM kills; local /sys reads only, degrades to not-available
package cgroupmem

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultRoot is the cgroup v2 mount point as seen from inside a container.
const DefaultRoot = "/sys/fs/cgroup"

// Stats is a point-in-time reading of the cgroup's memory accounting.
//
// memory.current is not a measure of memory in use: it also charges
// reclaimable page cache and kernel slab, which grow to fill whatever the
// cgroup allows and are freed on demand. A healthy machine that has read a
// lot of files sits at 90%+ of its limit on memory.current alone. Decisions
// about pressure therefore use WorkingSetBytes; CurrentBytes is kept only for
// diagnostics and for comparison against PeakBytes, which has the same basis.
type Stats struct {
	// WorkingSetBytes is memory.current minus the memory the kernel reclaims
	// before anything is killed: inactive_file and slab_reclaimable (see
	// workingSet). Compare it to LimitBytes for pressure; never negative.
	WorkingSetBytes uint64
	// CurrentBytes is memory.current verbatim, reclaimable cache included.
	CurrentBytes uint64
	// InactiveFileBytes and SlabReclaimableBytes are the memory.stat values
	// subtracted from CurrentBytes to get WorkingSetBytes; 0 when memory.stat
	// is absent or lacks the key.
	InactiveFileBytes    uint64
	SlabReclaimableBytes uint64
	// LimitBytes is memory.max; 0 means unlimited or unknown.
	LimitBytes uint64
	// PeakBytes is memory.peak (high-watermark of memory.current, so on the
	// CurrentBytes basis); 0 when the kernel doesn't expose it. After an OOM
	// kill memory.current has already dropped, so the peak is the honest "how
	// high did it get" number for error messages.
	PeakBytes uint64
}

// MemoryStat holds the memory.stat counters the working set is built from.
// A Has* flag is false when the key was absent or its value unparsable.
type MemoryStat struct {
	InactiveFile, SlabReclaimable       uint64
	HasInactiveFile, HasSlabReclaimable bool
}

// parseMemoryStat reads the keys it knows from the flat "<key> <value>" format
// of memory.stat and ignores every other line, malformed ones included. When a
// key repeats the last value wins.
func parseMemoryStat(data []byte) MemoryStat {
	var st MemoryStat
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case "inactive_file", "slab_reclaimable":
		default:
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		if fields[0] == "inactive_file" {
			st.InactiveFile, st.HasInactiveFile = v, true
		} else {
			st.SlabReclaimable, st.HasSlabReclaimable = v, true
		}
	}
	return st
}

// workingSet is memory.current less the reclaimable memory memory.stat
// reports: inactive_file (the cadvisor / kubelet working-set definition) and
// slab_reclaimable.
//
// slab_reclaimable is excluded as well because it is the same kind of memory:
// dentry and inode caches that the kernel's shrinkers free under pressure
// before it invokes the OOM killer, and that Linux itself counts as available
// in MemAvailable. Docker and build workloads inflate it by tens of GB. It is
// not guaranteed reclaimable (pinned dentries), but the pressure signal is an
// early warning of imminent OOM, which pinned slab alone does not cause.
// active_file stays in: the kernel demotes it only when reclaim is already
// under way, and evicting hot pages thrashes, so it is genuinely working set.
//
// The result is clamped at 0 (memory.current and memory.stat are separate
// reads, so the stat can be newer than the current). With neither key present
// nothing can be proven reclaimable and it falls back to memory.current, the
// conservative answer.
func workingSet(current uint64, st MemoryStat) uint64 {
	if !st.HasInactiveFile && !st.HasSlabReclaimable {
		return current
	}
	reclaimable := st.InactiveFile + st.SlabReclaimable
	if reclaimable < st.InactiveFile || reclaimable >= current {
		return 0
	}
	return current - reclaimable
}

// Reader reads memory accounting from a cgroup v2 directory. The zero-cost
// constructor never fails; use Available to find out whether the files exist.
type Reader struct {
	root string
}

// NewReader returns a Reader rooted at the given cgroup v2 directory
// (DefaultRoot in production; a fixture dir in tests).
func NewReader(root string) *Reader {
	return &Reader{root: root}
}

// Available reports whether cgroup v2 memory accounting exists at the root.
// False on macOS, on cgroup v1 hosts, and outside containers.
func (r *Reader) Available() bool {
	if r == nil || r.root == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(r.root, "memory.current"))
	return err == nil
}

// Stats reads the current memory accounting. ok is false when the cgroup
// files are absent or unreadable.
func (r *Reader) Stats() (stats Stats, ok bool) {
	if r == nil || r.root == "" {
		return Stats{}, false
	}
	used, err := readUint(filepath.Join(r.root, "memory.current"))
	if err != nil {
		return Stats{}, false
	}
	stats.CurrentBytes = used
	var st MemoryStat
	// memory.stat is how the reclaimable share is known; without it the
	// working set is memory.current.
	if data, err := os.ReadFile(filepath.Join(r.root, "memory.stat")); err == nil {
		st = parseMemoryStat(data)
	}
	stats.WorkingSetBytes = workingSet(used, st)
	stats.InactiveFileBytes, stats.SlabReclaimableBytes = st.InactiveFile, st.SlabReclaimable
	// "max" (unlimited) parses to 0 — LimitBytes==0 means no limit.
	if limit, err := readLimit(filepath.Join(r.root, "memory.max")); err == nil {
		stats.LimitBytes = limit
	}
	// memory.peak is optional (kernel >= 5.19); ignore absence.
	if peak, err := readUint(filepath.Join(r.root, "memory.peak")); err == nil {
		stats.PeakBytes = peak
	}
	return stats, true
}

// OOMKillCount returns the cumulative "oom_kill" counter from memory.events.
// ok is false when the file is absent or the key is missing.
func (r *Reader) OOMKillCount() (count uint64, ok bool) {
	if r == nil || r.root == "" {
		return 0, false
	}
	data, err := os.ReadFile(filepath.Join(r.root, "memory.events"))
	if err != nil {
		return 0, false
	}
	return parseMemoryEventsOOMKill(data)
}

// parseMemoryEventsOOMKill extracts the "oom_kill" counter from the flat
// keyed format of memory.events ("<key> <value>" per line).
func parseMemoryEventsOOMKill(data []byte) (uint64, bool) {
	events, ok := parseMemoryEvents(data)
	return events.OOMKill, ok && events.hasOOMKill
}

// Events are the cumulative counters in memory.events that say how hard the
// cgroup is being squeezed. High counts times usage went over memory.high and
// the kernel throttled the cgroup into reclaim; Max counts times it hit
// memory.max and had to reclaim or fail an allocation; OOMKill counts kills.
type Events struct {
	High, Max, OOMKill uint64
	hasOOMKill         bool
}

// parseMemoryEvents reads the counters it knows and ignores the rest. ok is
// false when the file has none of them.
func parseMemoryEvents(data []byte) (Events, bool) {
	var ev Events
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "high":
			ev.High, found = v, true
		case "max":
			ev.Max, found = v, true
		case "oom_kill":
			ev.OOMKill, ev.hasOOMKill, found = v, true, true
		}
	}
	return ev, found
}

// Events returns the cgroup's cumulative memory.events counters. ok is false
// when the file is absent or has none of them.
func (r *Reader) Events() (Events, bool) {
	if r == nil || r.root == "" {
		return Events{}, false
	}
	data, err := os.ReadFile(filepath.Join(r.root, "memory.events"))
	if err != nil {
		return Events{}, false
	}
	return parseMemoryEvents(data)
}

// OOMSnapshot captures the oom_kill counter before running a command so an
// increase during the command's lifetime can be detected afterwards.
type OOMSnapshot struct {
	kills uint64
	valid bool
}

// SnapshotOOMKills captures the current oom_kill counter. On hosts without
// cgroup accounting the snapshot is invalid and every later check answers
// false.
func (r *Reader) SnapshotOOMKills() OOMSnapshot {
	count, ok := r.OOMKillCount()
	return OOMSnapshot{kills: count, valid: ok}
}

// OOMKillsSince reports whether the cgroup recorded at least one OOM kill
// after the snapshot was taken.
func (r *Reader) OOMKillsSince(snap OOMSnapshot) bool {
	if !snap.valid {
		return false
	}
	count, ok := r.OOMKillCount()
	return ok && count > snap.kills
}

// sigkillExitCode is what a shell reports when a child of the shell was
// SIGKILLed (128+9). When the shell process itself is the OOM victim, Go's
// os/exec reports ExitCode() == -1 (terminated by signal) instead.
const sigkillExitCode = 137

// CheckOOMKill classifies a failed command: it returns true (plus a
// user/model-actionable message) when the exit status is consistent with a
// SIGKILL and the cgroup recorded an OOM kill during the command's lifetime.
//
// exitCode conventions: -1 for "process terminated by signal" (Go's
// os/exec), 137 for "shell reports a SIGKILLed child". Any other exit code
// is never classified as OOM — a command that failed on its own merits must
// keep its real error even if an unrelated OOM kill happened concurrently.
func (r *Reader) CheckOOMKill(exitCode int, snap OOMSnapshot) (bool, string) {
	if exitCode != -1 && exitCode != sigkillExitCode {
		return false, ""
	}
	if !r.OOMKillsSince(snap) {
		return false, ""
	}
	stats, ok := r.Stats()
	return true, OOMKillMessage(stats, ok)
}

// OOMKillMessage renders the structured, actionable error for an OOM-killed
// command. Both the LLM tool-result path and user RPC errors carry this text.
func OOMKillMessage(stats Stats, statsOK bool) string {
	if statsOK && stats.LimitBytes > 0 {
		// memory.peak is a high-watermark of memory.current, so the kill
		// message stays on that basis.
		used := stats.CurrentBytes
		if stats.PeakBytes > used {
			used = stats.PeakBytes
		}
		return fmt.Sprintf(
			"command was killed: workspace out of memory (used ~%s of %s). Upgrade the machine size or reduce the command's memory usage.",
			FormatBytes(used), FormatBytes(stats.LimitBytes))
	}
	return "command was killed: workspace out of memory. Upgrade the machine size or reduce the command's memory usage."
}

// FormatBytes renders a byte count in binary units with one decimal
// (e.g. "3.7 GiB", "512.0 MiB").
func FormatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTP"[exp])
}

// readUint reads a file containing a single unsigned integer.
func readUint(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
}

// readLimit reads memory.max, mapping the literal "max" (unlimited) to 0.
func readLimit(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(data))
	if s == "max" {
		return 0, nil
	}
	return strconv.ParseUint(s, 10, 64)
}
