//go:build windows

package pgtest

import (
	"os"
	"testing"
	"time"
)

func TestReadProcFactsSelfWindows(t *testing.T) {
	f := readProcFacts(os.Getpid())
	exe, _ := os.Executable()
	if !f.exeOK || !samePath(f.exe, exe) {
		t.Fatalf("exe %q ok=%v want %q", f.exe, f.exeOK, exe)
	}
	if !f.startOK || f.start.After(time.Now()) || time.Since(f.start) > 24*time.Hour {
		t.Fatalf("start %v ok=%v", f.start, f.startOK)
	}
}
