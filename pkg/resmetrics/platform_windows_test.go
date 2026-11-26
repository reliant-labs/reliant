//go:build windows

package resmetrics

import (
	"context"
	"testing"
	"time"
)

// The windows readers against the real kernel: every one must read cleanly,
// and what it reads must be plausible. The conversions are unit-tested on
// every platform (native_parse_test.go); this is the only proof the kernel32
// and ntdll calls are right, and forge's merge tier runs it on windows for a
// change to this package.
func TestRealWindows(t *testing.T) {
	s, err := Start(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	snap := s.Latest()
	if err := snap.Err(); err != nil {
		t.Fatalf("a windows reader failed: %v", err)
	}
	p, h := snap.Process, snap.Host
	if p.CPU == nil || p.Memory == nil || p.Threads == nil || p.Handles == nil {
		t.Fatalf("process fields left unread: %+v", p)
	}
	if p.Memory.RSS == 0 || p.Memory.Virtual < p.Memory.RSS || *p.Threads < 1 || *p.Handles < 1 {
		t.Errorf("implausible process values: %+v threads %d handles %d", p.Memory, *p.Threads, *p.Handles)
	}
	if up := p.Uptime(time.Now()); up < 0 || up > 24*time.Hour {
		t.Errorf("uptime %v: the start time is wrong", up)
	}
	if p.Faults != nil || p.ContextSwitches != nil || p.FDCount != nil || p.FDLimit != nil || p.DiskIO != nil {
		t.Error("windows reported a field it has no source for")
	}
	if h.CPU == nil || !h.CPU.NoIOWaitSteal || h.Memory == nil || h.MemoryLimit == nil {
		t.Fatalf("host fields left unread: %+v", h)
	}
	if h.Load != nil || h.Swap != nil || h.FileDescriptors != nil || h.Processes != nil {
		t.Error("windows reported a host field it has no source for")
	}
	if m := h.Memory; m.Total != *h.MemoryLimit || m.Used+m.Free+m.Cached != m.Total || m.Available == 0 {
		t.Errorf("implausible memory: %+v (limit %d)", m, *h.MemoryLimit)
	}
	if c := h.CPU.Seconds; c.User+c.System+c.Idle <= 0 {
		t.Errorf("no host CPU time: %+v", c)
	}
	if len(h.Filesystems) == 0 {
		t.Error("no filesystem was read")
	}
}
