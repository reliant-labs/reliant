package hostinfra

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// procFacts are the identity facts the OS reports about a pid. A pid recorded
// in a pidfile may have been recycled by an unrelated process, so before
// signalling it we compare what it is running and when it started against
// what the pidfile says. Zero-valued fields mean "could not be read".
type procFacts struct {
	exe     string
	exeOK   bool
	start   time.Time
	startOK bool
}

type pidVerdict int

const (
	// verdictUnknown: a fact could not be read. Never signal.
	verdictUnknown pidVerdict = iota
	// verdictOurs: right binary, and created no later than the pidfile.
	verdictOurs
	// verdictForeign: readable, and provably not the process that wrote the pidfile.
	verdictForeign
)

// classifyPid decides whether the process described by facts could be the one
// that wrote a pidfile last modified at pidfileMtime and that runs expectedExe.
// A process created after the pidfile was written cannot be its writer.
func classifyPid(facts procFacts, pidfileMtime time.Time, expectedExe string) pidVerdict {
	if facts.exeOK && !samePath(facts.exe, expectedExe) {
		return verdictForeign
	}
	if facts.startOK && facts.start.After(pidfileMtime) {
		return verdictForeign
	}
	if !facts.exeOK || !facts.startOK {
		return verdictUnknown
	}
	return verdictOurs
}

// shouldReap reports whether it is safe to signal the recorded pid.
func shouldReap(facts procFacts, pidfileMtime time.Time, expectedExe string) bool {
	return classifyPid(facts, pidfileMtime, expectedExe) == verdictOurs
}

// verifyRecordedPid reads pid's facts and classifies them against pidFile's mtime.
func verifyRecordedPid(pid int, pidFile, expectedExe string) pidVerdict {
	info, err := os.Stat(pidFile)
	if err != nil {
		return verdictUnknown
	}
	return classifyPid(readProcFacts(pid), info.ModTime(), expectedExe)
}

// samePath compares two executable paths after resolving symlinks (macOS
// reports /private/var for /var); case-insensitive on Windows.
func samePath(a, b string) bool {
	norm := func(p string) string {
		p = strings.TrimSuffix(p, " (deleted)")
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		return filepath.Clean(p)
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(norm(a), norm(b))
	}
	return norm(a) == norm(b)
}

// linuxStartTime converts /proc/<pid>/stat into a wall-clock start time.
// Field 22 is starttime in clock ticks since boot; comm (field 2) may contain
// spaces and parens, so fields are counted from the LAST ')'.
func linuxStartTime(stat string, bootTime time.Time, ticksPerSec int64) (time.Time, bool) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 || ticksPerSec <= 0 {
		return time.Time{}, false
	}
	fields := strings.Fields(stat[i+1:])
	if len(fields) < 20 {
		return time.Time{}, false
	}
	ticks, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil || ticks < 0 {
		return time.Time{}, false
	}
	return bootTime.Add(time.Duration(ticks) * time.Second / time.Duration(ticksPerSec)), true
}
