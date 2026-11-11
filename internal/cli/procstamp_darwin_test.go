//go:build darwin

package cli

import (
	"syscall"
	"testing"
)

// TestReadProcStampSelf pins the real macOS reader against this process: the
// stamp's group is this process's own, and it started before a mark read now.
func TestReadProcStampSelf(t *testing.T) {
	mark, ok := startMark()
	if !ok {
		t.Fatal("no start mark on macOS")
	}
	st, ok := readProcStamp(syscall.Getpid())
	if !ok {
		t.Fatal("cannot read this process's stamp")
	}
	if pgid, _ := syscall.Getpgid(0); st.pgid != pgid {
		t.Errorf("pgid = %d, want %d", st.pgid, pgid)
	}
	if st.start >= mark {
		t.Errorf("this process started at %dµs, not before the mark %dµs", st.start, mark)
	}
}
