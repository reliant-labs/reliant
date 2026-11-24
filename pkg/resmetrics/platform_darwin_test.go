//go:build darwin

package resmetrics

import (
	"context"
	"testing"
	"time"
)

// The darwin readers against the real kernel: every one must read cleanly,
// and what it reads must be plausible. The decoders are unit-tested on every
// platform (native_parse_test.go); this is the only proof the libSystem
// calls and sysctl names are right, so it runs wherever darwin tests do.
func TestRealDarwin(t *testing.T) {
	s, err := Start(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	snap := s.Latest()
	if err := snap.Err(); err != nil {
		t.Fatalf("a darwin reader failed: %v", err)
	}
	p, h := snap.Process, snap.Host
	if p.CPU == nil || p.Faults == nil || p.ContextSwitches == nil || p.Memory == nil || p.Threads == nil || p.FDCount == nil || p.FDLimit == nil {
		t.Fatalf("process fields left unread: %+v", p)
	}
	if p.Memory.RSS == 0 || p.Memory.Virtual < p.Memory.RSS || *p.Threads < 1 || *p.FDCount < 3 || *p.FDLimit < *p.FDCount {
		t.Errorf("implausible process values: %+v threads %d fds %d/%d", p.Memory, *p.Threads, *p.FDCount, *p.FDLimit)
	}
	if up := p.Uptime(time.Now()); up < 0 || up > 24*time.Hour {
		t.Errorf("uptime %v: the start time is wrong", up)
	}
	if p.DiskIO != nil || p.Handles != nil {
		t.Error("darwin reported a field it has no source for")
	}
	if h.CPU == nil || !h.CPU.NoIOWaitSteal || h.Load == nil || h.Memory == nil || h.MemoryLimit == nil || h.Swap == nil || h.FileDescriptors == nil {
		t.Fatalf("host fields left unread: %+v", h)
	}
	if m := h.Memory; m.Total != *h.MemoryLimit || m.Used+m.Free+m.Cached != m.Total {
		t.Errorf("implausible memory: %+v (limit %d)", m, *h.MemoryLimit)
	}
	if c := h.CPU.Seconds; c.User+c.System+c.Idle <= 0 {
		t.Errorf("no host CPU time: %+v", c)
	}
	if len(h.Filesystems) == 0 {
		t.Error("no filesystem was read")
	}
}
