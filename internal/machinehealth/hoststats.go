// Copyright (c) 2026 Reliant Labs
package machinehealth

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// HostStats reads the whole host's load and memory for the daemon's
// heartbeat (DaemonHeartbeat.machine_stats). Every daemon that can read /proc
// sends them; the gateway decides which machines' stats it logs.
type HostStats struct {
	procRoot string
	cpus     float64
}

// NewHostStats reads from procRoot ("" is /proc); cpus is the CPU count load
// is divided by (hostslot.DetectMachine().CPUs), 0 when unknown.
func NewHostStats(procRoot string, cpus float64) *HostStats {
	if procRoot == "" {
		procRoot = "/proc"
	}
	if !validReading(cpus) {
		cpus = 0
	}
	return &HostStats{procRoot: procRoot, cpus: cpus}
}

// Read takes one reading. It is nil when neither /proc/loadavg nor
// /proc/meminfo could be read (macOS, Windows), and on a nil HostStats; a
// file that cannot be read leaves only its own fields unset. Two small reads,
// cheap enough for every heartbeat.
func (h *HostStats) Read(now time.Time) *reliantv1.MachineStats {
	if h == nil {
		return nil
	}
	st := &reliantv1.MachineStats{Cpus: h.cpus, SampledAtMs: now.UnixMilli()}
	ok := false
	if l1, l5, l15, read := readLoadavg(filepath.Join(h.procRoot, "loadavg")); read {
		st.Load1, st.Load5, st.Load15 = &l1, &l5, &l15
		ok = true
	}
	if total, avail, read := readMeminfo(filepath.Join(h.procRoot, "meminfo")); read {
		st.MemTotalBytes, st.MemAvailableBytes = total, avail
		ok = true
	}
	if !ok {
		return nil
	}
	return st
}

// readMeminfo returns MemTotal and MemAvailable from /proc/meminfo, in bytes:
//
//	MemTotal:       131899292 kB
//	MemFree:         1234567 kB
//	MemAvailable:   98765432 kB
//
// ok is false unless both are present and total is not 0 (MemAvailable is
// missing on kernels before 3.14).
func readMeminfo(path string) (total, avail uint64, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, false
	}
	var haveTotal, haveAvail bool
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		key, rest, found := strings.Cut(sc.Text(), ":")
		if !found {
			continue
		}
		var dst *uint64
		switch key {
		case "MemTotal":
			dst, haveTotal = &total, true
		case "MemAvailable":
			dst, haveAvail = &avail, true
		default:
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			return 0, 0, false
		}
		v, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			return 0, 0, false
		}
		if len(f) > 1 && f[1] == "kB" {
			v *= 1024
		}
		*dst = v
	}
	if !haveTotal || !haveAvail || total == 0 {
		return 0, 0, false
	}
	return total, avail, true
}
