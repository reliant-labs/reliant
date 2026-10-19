// Copyright (c) 2025 Reliant Labs
package cgroupmem

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// memoryStatFixture renders a memory.stat in the kernel's flat format with the
// given counters first and the usual unrelated keys after them.
func memoryStatFixture(counters ...[2]string) string {
	out := ""
	for _, c := range counters {
		out += c[0] + " " + c[1] + "\n"
	}
	return out + "kernel_stack 4096000\npgfault 123456\nworkingset_refault_file 0\n"
}

func u(v uint64) string { return strconv.FormatUint(v, 10) }

const (
	gb  = uint64(1_000_000_000)
	gib = uint64(1) << 30
)

// measuredMachineStat is the memory.stat of the agent machine that raised the
// false alarm: memory.current 63.9 GB of a 68.7 GB (64 GiB) limit with anon
// 1.7 GB, file 40.8 GB (inactive_file 11.2, active_file 29.5), slab 19.3 GB and
// oom_kill 0. The kernel's slab split was not captured; almost all of it is
// assumed to be dentry/inode cache (slab_reclaimable), as on docker hosts.
func measuredMachineStat() string {
	return memoryStatFixture(
		[2]string{"anon", u(17 * gb / 10)},
		[2]string{"file", u(408 * gb / 10)},
		[2]string{"inactive_file", u(112 * gb / 10)},
		[2]string{"active_file", u(295 * gb / 10)},
		[2]string{"slab", u(193 * gb / 10)},
		[2]string{"slab_reclaimable", u(185 * gb / 10)},
		[2]string{"slab_unreclaimable", u(8 * gb / 10)},
	)
}

func writeStat(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "memory.stat"), []byte(content), 0o644); err != nil {
		t.Fatalf("writing memory.stat: %v", err)
	}
}

func TestWorkingSet(t *testing.T) {
	tests := []struct {
		name    string
		current uint64
		stat    string
		want    uint64
	}{
		{
			name:    "measured machine: cache and reclaimable slab are not working set",
			current: 639 * gb / 10,
			stat:    measuredMachineStat(),
			want:    639*gb/10 - 112*gb/10 - 185*gb/10,
		},
		{
			name:    "anon heavy: nearly everything is working set",
			current: 62 * gb,
			stat: memoryStatFixture(
				[2]string{"anon", u(60 * gb)},
				[2]string{"inactive_file", u(500_000_000)},
				[2]string{"slab_reclaimable", u(100_000_000)},
			),
			want: 62*gb - 500_000_000 - 100_000_000,
		},
		{
			name:    "active_file stays in the working set",
			current: 10 * gb,
			stat: memoryStatFixture(
				[2]string{"active_file", u(9 * gb)},
				[2]string{"inactive_file", u(gb)},
				[2]string{"slab_reclaimable", u(0)},
			),
			want: 9 * gb,
		},
		{
			name:    "only inactive_file present",
			current: 10 * gb,
			stat:    memoryStatFixture([2]string{"inactive_file", u(4 * gb)}),
			want:    6 * gb,
		},
		{
			name:    "only slab_reclaimable present",
			current: 10 * gb,
			stat:    memoryStatFixture([2]string{"slab_reclaimable", u(3 * gb)}),
			want:    7 * gb,
		},
		{
			name:    "neither key: falls back to memory.current",
			current: 10 * gb,
			stat:    memoryStatFixture([2]string{"anon", u(gb)}),
			want:    10 * gb,
		},
		{
			name:    "empty memory.stat: falls back to memory.current",
			current: 10 * gb,
			stat:    "",
			want:    10 * gb,
		},
		{
			name:    "garbled values are ignored, the other key still counts",
			current: 10 * gb,
			stat:    "inactive_file lots\nslab_reclaimable " + u(2*gb) + "\nthis line is nonsense\ninactive_file\n",
			want:    8 * gb,
		},
		{
			name:    "negative and non-numeric values are ignored",
			current: 10 * gb,
			stat:    "inactive_file -5\nslab_reclaimable 1.5\n",
			want:    10 * gb,
		},
		{
			name:    "inactive_file larger than current clamps to zero",
			current: 1 * gb,
			stat:    memoryStatFixture([2]string{"inactive_file", u(5 * gb)}),
			want:    0,
		},
		{
			name:    "reclaimable equal to current is zero",
			current: 4 * gb,
			stat:    memoryStatFixture([2]string{"inactive_file", u(3 * gb)}, [2]string{"slab_reclaimable", u(gb)}),
			want:    0,
		},
		{
			name:    "sum of the two keys overflowing uint64 clamps to zero",
			current: 10 * gb,
			stat: memoryStatFixture(
				[2]string{"inactive_file", u(^uint64(0))},
				[2]string{"slab_reclaimable", u(^uint64(0))},
			),
			want: 0,
		},
		{
			name:    "CRLF and extra whitespace",
			current: 10 * gb,
			stat:    "inactive_file   " + u(gb) + "\r\n",
			want:    9 * gb,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := workingSet(tt.current, parseMemoryStat([]byte(tt.stat)))
			if got != tt.want {
				t.Errorf("workingSet = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestReaderStatsWorkingSet drives the whole path from files on disk.
func TestReaderStatsWorkingSet(t *testing.T) {
	dir := writeCgroupFixture(t, u(639*gb/10)+"\n", u(64*gib)+"\n", "", eventsFixture)
	writeStat(t, dir, measuredMachineStat())

	stats, ok := NewReader(dir).Stats()
	if !ok {
		t.Fatal("Stats not ok")
	}
	if stats.CurrentBytes != 639*gb/10 {
		t.Errorf("CurrentBytes = %d, want memory.current verbatim", stats.CurrentBytes)
	}
	if want := 639*gb/10 - 112*gb/10 - 185*gb/10; stats.WorkingSetBytes != want {
		t.Errorf("WorkingSetBytes = %d, want %d", stats.WorkingSetBytes, want)
	}
	if stats.InactiveFileBytes != 112*gb/10 || stats.SlabReclaimableBytes != 185*gb/10 {
		t.Errorf("diagnostics = inactive_file %d slab_reclaimable %d", stats.InactiveFileBytes, stats.SlabReclaimableBytes)
	}
	if stats.LimitBytes != 64*gib {
		t.Errorf("LimitBytes = %d", stats.LimitBytes)
	}
}

func TestReaderStatsUnreadableMemoryStat(t *testing.T) {
	dir := writeCgroupFixture(t, u(5*gb)+"\n", u(8*gb)+"\n", "", eventsFixture)
	// A directory where the file should be: ReadFile fails, Stats must not.
	if err := os.Mkdir(filepath.Join(dir, "memory.stat"), 0o755); err != nil {
		t.Fatal(err)
	}
	stats, ok := NewReader(dir).Stats()
	if !ok || stats.WorkingSetBytes != 5*gb || stats.CurrentBytes != 5*gb {
		t.Errorf("Stats = %+v ok=%v, want working set = memory.current", stats, ok)
	}
}

// pressureAfterPoll runs one watcher poll over a fixture and returns the
// sample.
func pressureAfterPoll(t *testing.T, current, limit uint64, stat string) Sample {
	t.Helper()
	dir := writeCgroupFixture(t, u(current)+"\n", u(limit)+"\n", "", eventsFixture)
	if stat != "" {
		writeStat(t, dir, stat)
	}
	w := NewWatcher(NewReader(dir), time.Hour)
	w.poll()
	sample, ok := w.Latest()
	if !ok {
		t.Fatal("no sample after poll")
	}
	return sample
}

// TestWatcherPressureUsesWorkingSet is the regression test for the false
// alarm: memory.current at 93% of the limit, almost all of it page cache.
func TestWatcherPressureUsesWorkingSet(t *testing.T) {
	const limit = 64 * gib // 68.7 GB

	t.Run("measured machine at 93 percent of memory.current has no pressure", func(t *testing.T) {
		if cur := float64(639*gb/10) / float64(limit); cur < 0.92 {
			t.Fatalf("fixture drifted: memory.current is only %.2f of the limit", cur)
		}
		s := pressureAfterPoll(t, 639*gb/10, limit, measuredMachineStat())
		if s.Pressure {
			t.Errorf("pressure asserted on a healthy machine: %+v", s)
		}
		if s.WorkingSetBytes >= s.CurrentBytes || s.CurrentBytes != 639*gb/10 {
			t.Errorf("sample = %+v, want working set below memory.current, current kept", s)
		}
	})

	t.Run("without slab_reclaimable the inactive_file subtraction alone is enough", func(t *testing.T) {
		stat := memoryStatFixture(
			[2]string{"anon", u(17 * gb / 10)},
			[2]string{"inactive_file", u(112 * gb / 10)},
			[2]string{"active_file", u(295 * gb / 10)},
		)
		if s := pressureAfterPoll(t, 639*gb/10, limit, stat); s.Pressure {
			t.Errorf("pressure asserted: %+v", s)
		}
	})

	t.Run("anon heavy machine asserts pressure", func(t *testing.T) {
		stat := memoryStatFixture(
			[2]string{"anon", u(60 * gb)},
			[2]string{"file", u(2 * gb)},
			[2]string{"inactive_file", u(gb)},
			[2]string{"active_file", u(gb)},
			[2]string{"slab_reclaimable", u(gb / 2)},
		)
		s := pressureAfterPoll(t, 639*gb/10, limit, stat)
		if !s.Pressure {
			t.Errorf("pressure not asserted with 60 GB anon of 68.7 GB: %+v", s)
		}
	})

	t.Run("memory.stat missing keeps the old conservative behaviour", func(t *testing.T) {
		if s := pressureAfterPoll(t, 639*gb/10, limit, ""); !s.Pressure {
			t.Errorf("no memory.stat: working set is memory.current, 93%% must assert: %+v", s)
		}
	})

	t.Run("inactive_file larger than current does not assert or underflow", func(t *testing.T) {
		stat := memoryStatFixture([2]string{"inactive_file", u(100 * gb)})
		s := pressureAfterPoll(t, 639*gb/10, limit, stat)
		if s.Pressure || s.WorkingSetBytes != 0 {
			t.Errorf("sample = %+v, want zero working set and no pressure", s)
		}
	})

	t.Run("garbled memory.stat degrades to memory.current", func(t *testing.T) {
		s := pressureAfterPoll(t, 639*gb/10, limit, "\x00\x01 garbage\ninactive_file NaN\n")
		if !s.Pressure || s.WorkingSetBytes != 639*gb/10 {
			t.Errorf("sample = %+v, want working set = memory.current", s)
		}
	})
}

// TestWatcherOOMKillIndependentOfWorkingSet pins that OOM-kill detection reads
// memory.events only, whatever memory.stat says.
func TestWatcherOOMKillIndependentOfWorkingSet(t *testing.T) {
	dir := writeCgroupFixture(t, u(5*gb)+"\n", u(64*gib)+"\n", "", eventsFixture)
	writeStat(t, dir, memoryStatFixture([2]string{"inactive_file", u(100 * gb)}))
	r := NewReader(dir)
	snap := r.SnapshotOOMKills()
	if err := os.WriteFile(filepath.Join(dir, "memory.events"), []byte("high 42\nmax 12\noom 6\noom_kill 4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !r.OOMKillsSince(snap) {
		t.Error("OOM kill not detected")
	}
}

// TestMeasuredMachineIsNotUnderPressure asserts only the pressure bit, the
// observable the false alarm was about.
func TestMeasuredMachineIsNotUnderPressure(t *testing.T) {
	if s := pressureAfterPoll(t, 639*gb/10, 64*gib, measuredMachineStat()); s.Pressure {
		t.Error("healthy machine (anon 1.7 GB of 68.7 GB, rest reclaimable cache) reported memory pressure")
	}
}
