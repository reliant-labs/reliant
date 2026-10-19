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
//	/sys/fs/cgroup/memory.stat    — breakdown of memory.current; the file LRU
//	                                (active_file, inactive_file) and
//	                                slab_reclaimable are what the kernel frees
//	                                before it considers an OOM kill
//	/sys/fs/cgroup/memory.pressure — PSI: share of time tasks stalled on memory
//	                                (optional; needs CONFIG_PSI)
//	/sys/fs/cgroup/memory.max     — limit in bytes, or "max" (unlimited)
//	/sys/fs/cgroup/memory.peak    — high-watermark (kernel >= 5.19; optional)
//	/sys/fs/cgroup/memory.events  — flat keyed counters, incl. "oom_kill";
//	                                hierarchical, so a child cgroup hitting
//	                                its OWN limit (dockerd's) counts here too
//	/sys/fs/cgroup/memory.events.local — the same counters for this cgroup's
//	                                own limit only
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
// memory.current is not a measure of memory in use: it also charges page
// cache and kernel slab, which grow to fill whatever the cgroup allows and are
// freed on demand. A healthy build machine that has read a lot of files sits at
// 90%+ of its limit on memory.current alone, most of it file cache the kernel
// drops the moment anything needs the room. Decisions about pressure therefore
// use UnreclaimableBytes, corroborated by the kernel's own stall accounting
// (PSI) where it exists; CurrentBytes is kept only for diagnostics and for
// comparison against PeakBytes, which has the same basis.
type Stats struct {
	// UnreclaimableBytes is memory.current minus everything the kernel can
	// free without killing a process: the file LRU (active_file +
	// inactive_file) and slab_reclaimable (see unreclaimable). What is left
	// is anonymous memory, shmem/tmpfs, unreclaimable slab, page tables and
	// kernel stacks. Compare it to LimitBytes for pressure; never negative.
	UnreclaimableBytes uint64
	// CurrentBytes is memory.current verbatim, reclaimable cache included.
	CurrentBytes uint64
	// ActiveFileBytes, InactiveFileBytes and SlabReclaimableBytes are the
	// memory.stat values subtracted from CurrentBytes to get
	// UnreclaimableBytes; 0 when memory.stat is absent or lacks the key.
	ActiveFileBytes      uint64
	InactiveFileBytes    uint64
	SlabReclaimableBytes uint64
	// LimitBytes is memory.max; 0 means unlimited or unknown.
	LimitBytes uint64
	// PeakBytes is memory.peak (high-watermark of memory.current, so on the
	// CurrentBytes basis); 0 when the kernel doesn't expose it. After an OOM
	// kill memory.current has already dropped, so the peak is the honest "how
	// high did it get" number for error messages.
	PeakBytes uint64
	// PSI is memory.pressure: how much of the time tasks in the cgroup were
	// stalled waiting for memory. PSI.Valid is false when the kernel does not
	// expose it (CONFIG_PSI off, psi=0, or an older kernel).
	PSI PSI
}

// PSI is the kernel's memory pressure-stall information for the cgroup: the
// share of wall time, in percent averaged over the last 10 seconds, in which
// SOME task (Some) or ALL non-idle tasks (Full) were stalled on memory —
// reclaim, refaulting evicted pages, swap-in. Unlike any byte count it
// measures cost, not occupancy: page cache that sits in memory unused costs
// nothing and does not move it, while a working set thrashing through a
// too-small cache does, whichever kind of page is being thrashed.
type PSI struct {
	SomeAvg10, FullAvg10 float64
	Valid                bool
}

// parsePSI reads the "some" and "full" lines of a cgroup's memory.pressure:
//
//	some avg10=0.12 avg60=0.05 avg300=0.01 total=123456
//	full avg10=0.00 avg60=0.00 avg300=0.00 total=6543
//
// Valid is set only when the "some" line's avg10 parsed; a kernel without
// "full" for the cgroup reports it as 0.
func parsePSI(data []byte) PSI {
	var p PSI
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		var avg10 float64
		ok := false
		for _, f := range fields[1:] {
			if v, found := strings.CutPrefix(f, "avg10="); found {
				if n, err := strconv.ParseFloat(v, 64); err == nil && n >= 0 && n <= 100 {
					avg10, ok = n, true
				}
			}
		}
		if !ok {
			continue
		}
		switch fields[0] {
		case "some":
			p.SomeAvg10, p.Valid = avg10, true
		case "full":
			p.FullAvg10 = avg10
		}
	}
	return p
}

// MemoryStat holds the memory.stat counters the unreclaimable figure is built
// from. A Has* flag is false when the key was absent or its value unparsable.
type MemoryStat struct {
	ActiveFile, InactiveFile, SlabReclaimable          uint64
	HasActiveFile, HasInactiveFile, HasSlabReclaimable bool
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
		case "active_file", "inactive_file", "slab_reclaimable":
		default:
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "active_file":
			st.ActiveFile, st.HasActiveFile = v, true
		case "inactive_file":
			st.InactiveFile, st.HasInactiveFile = v, true
		default:
			st.SlabReclaimable, st.HasSlabReclaimable = v, true
		}
	}
	return st
}

// unreclaimable is memory.current less everything memory.stat reports as
// reclaimable: the whole file LRU (active_file + inactive_file) and
// slab_reclaimable — the same memory Linux counts as available in
// MemAvailable.
//
// active_file is excluded too. It is clean (or writeback-able) file cache the
// kernel demotes to the inactive list and drops as soon as anything needs the
// room; on a build machine that rereads the same caches all day it is tens of
// GB, and counting it made "memory used" read 50–90% of the limit with 2 GB
// of anonymous memory. The one way hot file pages hurt is thrashing, when the
// working set no longer fits and evicted pages are faulted straight back in —
// and that is a stall, which PSI measures directly, not an occupancy any byte
// count can see. So pressure is judged on this figure AND PSI (nextPressure).
//
// shmem/tmpfs stays in: it is charged to "file" but lives on the anon LRU
// (it is not in active_file/inactive_file) and cannot be dropped without swap.
// slab_reclaimable is dentry and inode cache the shrinkers free before the
// OOM killer runs; docker and node_modules tree walks inflate it by tens of GB.
//
// The result is clamped at 0 (memory.current and memory.stat are separate
// reads, so the stat can be newer than the current). With none of the keys
// present nothing can be proven reclaimable and it falls back to
// memory.current, the conservative answer.
func unreclaimable(current uint64, st MemoryStat) uint64 {
	if !st.HasActiveFile && !st.HasInactiveFile && !st.HasSlabReclaimable {
		return current
	}
	var reclaimable uint64
	for _, v := range []uint64{st.ActiveFile, st.InactiveFile, st.SlabReclaimable} {
		sum := reclaimable + v
		if sum < reclaimable { // overflow: garbage counters, nothing is provable
			return 0
		}
		reclaimable = sum
	}
	if reclaimable >= current {
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
	// memory.stat is how the reclaimable share is known; without it nothing
	// is provably reclaimable and the figure is memory.current.
	if data, err := os.ReadFile(filepath.Join(r.root, "memory.stat")); err == nil {
		st = parseMemoryStat(data)
	}
	stats.UnreclaimableBytes = unreclaimable(used, st)
	stats.ActiveFileBytes, stats.InactiveFileBytes, stats.SlabReclaimableBytes = st.ActiveFile, st.InactiveFile, st.SlabReclaimable
	if data, err := os.ReadFile(filepath.Join(r.root, "memory.pressure")); err == nil {
		stats.PSI = parsePSI(data)
	}
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

// ChildEvents returns the memory.events counters of each direct child cgroup
// (dockerd/, daemon/ on a cloud machine), keyed by directory name. The parent's
// memory.events is hierarchical, so this is how growth there is attributed:
// a child that hit its OWN memory.max (dockerd's container cap) shows up in
// the parent's "max" counter although the parent's limit was never reached.
// Children without a readable memory.events are omitted.
func (r *Reader) ChildEvents() map[string]Events {
	if r == nil || r.root == "" {
		return nil
	}
	entries, err := os.ReadDir(r.root)
	if err != nil {
		return nil
	}
	out := map[string]Events{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(r.root, e.Name(), "memory.events"))
		if err != nil {
			continue
		}
		if ev, ok := parseMemoryEvents(data); ok {
			out[e.Name()] = ev
		}
	}
	return out
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
