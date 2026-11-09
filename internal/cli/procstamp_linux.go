//go:build linux

package cli

import (
	"encoding/binary"
	"os"
	"strconv"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

// readProcStamp reads pid's process group and start time (clock ticks since
// boot) from /proc/<pid>/stat.
func readProcStamp(pid int) (procStamp, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procStamp{}, false
	}
	return parseProcStatStamp(data)
}

// startMark is now in the unit and clock of /proc/<pid>/stat's starttime —
// clock ticks since boot — from /proc/uptime at the kernel's real tick rate.
// It is unavailable when the tick rate cannot be read: forge never guesses
// it, so killProcessTree then signals no process group (stampVerifier.ok).
func startMark() (int64, bool) {
	hz, ok := linuxClockTicks()
	if !ok {
		return 0, false
	}
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, false
	}
	return linuxStartMark(data, hz)
}

var (
	clockTicksOnce sync.Once
	clockTicksHz   int64
	clockTicksOK   bool
)

// linuxClockTicks is the kernel's USER_HZ — the unit of starttime — read once
// from AT_CLKTCK in the auxiliary vector: the runtime's copy (unix.Auxv), or
// /proc/self/auxv when the runtime has none. No cgo, no assumed default.
func linuxClockTicks() (int64, bool) {
	clockTicksOnce.Do(func() {
		if auxv, err := unix.Auxv(); err == nil {
			clockTicksHz, clockTicksOK = clockTicksFromAuxv(auxv)
		}
		if !clockTicksOK {
			if data, err := os.ReadFile("/proc/self/auxv"); err == nil {
				clockTicksHz, clockTicksOK = clockTicksFromAuxv(parseAuxv(data, int(unsafe.Sizeof(uintptr(0))), binary.NativeEndian))
			}
		}
	})
	return clockTicksHz, clockTicksOK
}
