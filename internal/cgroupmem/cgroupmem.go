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
//	/sys/fs/cgroup/memory.max     — limit in bytes, or "max" (unlimited)
//	/sys/fs/cgroup/memory.peak    — high-watermark (kernel >= 5.19; optional)
//	/sys/fs/cgroup/memory.events  — flat keyed counters, incl. "oom_kill"
//
// Everything degrades gracefully when those files are absent (macOS, local
// Linux daemons outside a limited cgroup): readers report not-available and
// all checks answer false.
//
// forge:exclude-contract
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
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
type Stats struct {
	// UsedBytes is memory.current.
	UsedBytes uint64
	// LimitBytes is memory.max; 0 means unlimited or unknown.
	LimitBytes uint64
	// PeakBytes is memory.peak (high-watermark); 0 when the kernel doesn't
	// expose it. After an OOM kill memory.current has already dropped, so the
	// peak is the honest "how high did it get" number for error messages.
	PeakBytes uint64
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
	stats.UsedBytes = used
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
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "oom_kill" {
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return v, true
	}
	return 0, false
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
		used := stats.UsedBytes
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
