//go:build linux

package cli

import (
	"syscall"
	"testing"
	"time"
)

// TestReadProcStampSelf pins the real Linux reader against this process: the
// tick rate comes from AT_CLKTCK, the stamp's group is this process's own,
// and it verifies as started before a mark read now. Every Linux forge runs on
// exposes /proc and the auxiliary vector, so a failure here is a defect.
func TestReadProcStampSelf(t *testing.T) {
	hz, ok := linuxClockTicks()
	if !ok {
		t.Fatal("cannot read USER_HZ (AT_CLKTCK) from the auxiliary vector")
	}
	t.Logf("USER_HZ = %d", hz)
	st, ok := readProcStamp(syscall.Getpid())
	if !ok {
		t.Fatal("cannot read this process's stamp")
	}
	if pgid, _ := syscall.Getpgid(0); st.pgid != pgid {
		t.Errorf("pgid = %d, want %d", st.pgid, pgid)
	}
	// A process younger than one tick cannot verify yet (refused, never
	// wrongly accepted); this one only gets older.
	var mark int64
	if !pollUntil(2*time.Second, func() bool {
		mark, ok = startMark()
		return ok && st.start < mark
	}) {
		t.Errorf("this process started at tick %d, not before the mark %d (ok=%v)", st.start, mark, ok)
	}
}
