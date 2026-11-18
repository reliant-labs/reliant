package cli

import (
	"errors"
	"reflect"
	"syscall"
	"testing"
	"time"
)

func ent(pid, ppid int, created int64, createdOK, alive bool) procSnapshotEntry {
	return procSnapshotEntry{PID: pid, PPID: ppid, Created: time.Unix(created, 0), CreatedOK: createdOK, Alive: alive}
}

func TestResolveParentLinks(t *testing.T) {
	entries := []procSnapshotEntry{
		ent(10, 0, 100, true, true),   // good parent
		ent(11, 10, 200, true, true),  // kept: parent older
		ent(12, 10, 50, true, true),   // dropped: created before parent (pid reuse)
		ent(13, 10, 100, true, true),  // kept: equal times
		ent(14, 10, 300, false, true), // dropped: child time unreadable
		ent(20, 0, 100, false, true),  // parent time unreadable
		ent(21, 20, 200, true, true),  // dropped
		ent(30, 0, 100, true, false),  // parent not alive
		ent(31, 30, 200, true, true),  // dropped
		ent(40, 999, 200, true, true), // parent absent from snapshot
		ent(50, 50, 200, true, true),  // self-parent
	}
	got := resolveParentLinks(entries)
	want := map[int]int{10: 0, 11: 10, 12: 0, 13: 10, 14: 0, 20: 0, 21: 0, 30: 0, 31: 0, 40: 0, 50: 0}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// explorer.exe's recorded parent is userinit.exe, long dead; every user app
// shares that stale ppid. A dead root must own nothing.
func TestDescendantsOfStalePpidDeadRoot(t *testing.T) {
	const userinit = 700 // dead
	entries := []procSnapshotEntry{
		ent(800, userinit, 500, true, true), // explorer
		ent(801, 800, 600, true, true),
		ent(802, userinit, 510, true, true), // another app with stale ppid
	}
	ppids := resolveParentLinks(entries)
	if got := descendantsOf(userinit, ppids); len(got) != 0 {
		t.Fatalf("dead root must have no descendants, got %v", got)
	}
	if got := descendantsOf(800, ppids); !reflect.DeepEqual(got, []int{801}) {
		t.Fatalf("live root: got %v", got)
	}
}

func TestDescendantsOfRecycledRoot(t *testing.T) {
	// root pid 100 was recycled: new owner created at t=900, "child" 101
	// recorded ppid 100 but was created at t=400 (before the new owner).
	entries := []procSnapshotEntry{
		ent(100, 0, 900, true, true),
		ent(101, 100, 400, true, true),
		ent(102, 100, 950, true, true),
		ent(103, 102, 960, true, true),
	}
	got := descendantsOf(100, resolveParentLinks(entries))
	if !reflect.DeepEqual(got, []int{102, 103}) {
		t.Fatalf("got %v", got)
	}
}

func TestCtrlBreakAllowed(t *testing.T) {
	cases := []struct {
		name                          string
		pid, gid                      uint32
		groupOK, onConsole, consoleOK bool
		want                          bool
	}{
		{"leader on console", 5, 5, true, true, true, true},
		{"not leader", 5, 1, true, true, true, false},
		{"leader off console", 5, 5, true, false, true, false},
		{"group unreadable", 5, 5, false, true, true, false},
		{"console unreadable", 5, 5, true, true, false, false},
	}
	for _, c := range cases {
		if got := ctrlBreakAllowed(c.pid, c.gid, c.groupOK, c.onConsole, c.consoleOK); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestAliveVerdicts(t *testing.T) {
	if aliveFromOpenError(syscall.Errno(87)) {
		t.Error("ERROR_INVALID_PARAMETER must mean dead")
	}
	if aliveFromOpenError(errors.Join(syscall.Errno(87))) {
		t.Error("wrapped invalid parameter must mean dead")
	}
	for _, code := range []syscall.Errno{5, 1, 2, 6, 299} {
		if !aliveFromOpenError(code) {
			t.Errorf("errno %d must fail closed to alive", code)
		}
	}
	if aliveFromWait(waitObject0, nil) {
		t.Error("signalled handle is dead")
	}
	if !aliveFromWait(waitTimeout, nil) {
		t.Error("timeout is alive")
	}
	if !aliveFromWait(0xFFFFFFFF, errors.New("boom")) || !aliveFromWait(0x80, nil) {
		t.Error("unknown wait result must be alive")
	}
}

func TestManagedProcessExited(t *testing.T) {
	if managedProcessExited(nil) || managedProcessExited(&managedProcess{}) {
		t.Fatal("nil/no-done must not count as exited")
	}
	mp := &managedProcess{done: make(chan struct{})}
	if managedProcessExited(mp) {
		t.Fatal("open done channel")
	}
	close(mp.done)
	if !managedProcessExited(mp) {
		t.Fatal("closed done channel")
	}
}

func TestNTStringOffset(t *testing.T) {
	const base, hdr, total = 1000, 16, 64
	if off, ok := ntStringOffset(base+16, base, 8, total, hdr); !ok || off != 16 {
		t.Fatalf("valid: %d %v", off, ok)
	}
	bad := []struct {
		name   string
		buffer uintptr
		length uint16
	}{
		{"below base", base - 2, 4},
		{"inside header", base + 8, 4},
		{"odd offset", base + 17, 4},
		{"odd length", base + 16, 3},
		{"overruns", base + 60, 8},
		{"far pointer", base + 1<<20, 2},
	}
	for _, c := range bad {
		if _, ok := ntStringOffset(c.buffer, base, c.length, total, hdr); ok {
			t.Errorf("%s accepted", c.name)
		}
	}
}

func TestDecodeUTF16LE(t *testing.T) {
	if got := decodeUTF16LE([]byte{'h', 0, 'i', 0, 0x3d, 0xd8, 0x00, 0xde}); got != "hi😀" {
		t.Fatalf("got %q", got)
	}
}
