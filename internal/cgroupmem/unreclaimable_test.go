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

func TestUnreclaimable(t *testing.T) {
	tests := []struct {
		name    string
		current uint64
		stat    string
		want    uint64
	}{
		{
			name:    "measured machine: file cache and reclaimable slab are reclaimable",
			current: 639 * gb / 10,
			stat:    measuredMachineStat(),
			want:    639*gb/10 - 112*gb/10 - 295*gb/10 - 185*gb/10,
		},
		{
			name:    "anon heavy: nearly everything is unreclaimable",
			current: 62 * gb,
			stat: memoryStatFixture(
				[2]string{"anon", u(60 * gb)},
				[2]string{"inactive_file", u(500_000_000)},
				[2]string{"slab_reclaimable", u(100_000_000)},
			),
			want: 62*gb - 500_000_000 - 100_000_000,
		},
		{
			name:    "active_file is reclaimable cache too",
			current: 10 * gb,
			stat: memoryStatFixture(
				[2]string{"anon", u(gb)},
				[2]string{"active_file", u(8 * gb)},
				[2]string{"inactive_file", u(gb / 2)},
				[2]string{"slab_reclaimable", u(0)},
			),
			want: 10*gb - 8*gb - gb/2,
		},
		{
			// shmem/tmpfs is charged to "file" but sits on the anon LRU, so it
			// is in neither file list and stays unreclaimable.
			name:    "shmem counted in file but not in the file LRU stays in",
			current: 10 * gb,
			stat: memoryStatFixture(
				[2]string{"anon", u(gb)},
				[2]string{"file", u(7 * gb)},
				[2]string{"shmem", u(4 * gb)},
				[2]string{"active_file", u(2 * gb)},
				[2]string{"inactive_file", u(gb)},
			),
			want: 7 * gb,
		},
		{
			name:    "only active_file present",
			current: 10 * gb,
			stat:    memoryStatFixture([2]string{"active_file", u(4 * gb)}),
			want:    6 * gb,
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
			name:    "no key: falls back to memory.current",
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
			name:    "sum of the keys overflowing uint64 clamps to zero",
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
			got := unreclaimable(tt.current, parseMemoryStat([]byte(tt.stat)))
			if got != tt.want {
				t.Errorf("unreclaimable = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestReaderStatsUnreclaimable drives the whole path from files on disk.
func TestReaderStatsUnreclaimable(t *testing.T) {
	dir := writeCgroupFixture(t, u(639*gb/10)+"\n", u(64*gib)+"\n", "", eventsFixture)
	writeStat(t, dir, measuredMachineStat())

	stats, ok := NewReader(dir).Stats()
	if !ok {
		t.Fatal("Stats not ok")
	}
	if stats.CurrentBytes != 639*gb/10 {
		t.Errorf("CurrentBytes = %d, want memory.current verbatim", stats.CurrentBytes)
	}
	if want := 639*gb/10 - 112*gb/10 - 295*gb/10 - 185*gb/10; stats.UnreclaimableBytes != want {
		t.Errorf("UnreclaimableBytes = %d, want %d", stats.UnreclaimableBytes, want)
	}
	if stats.ActiveFileBytes != 295*gb/10 || stats.InactiveFileBytes != 112*gb/10 || stats.SlabReclaimableBytes != 185*gb/10 {
		t.Errorf("diagnostics = active_file %d inactive_file %d slab_reclaimable %d", stats.ActiveFileBytes, stats.InactiveFileBytes, stats.SlabReclaimableBytes)
	}
	if stats.PSI.Valid {
		t.Errorf("PSI = %+v without a memory.pressure file, want invalid", stats.PSI)
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
	if !ok || stats.UnreclaimableBytes != 5*gb || stats.CurrentBytes != 5*gb {
		t.Errorf("Stats = %+v ok=%v, want unreclaimable = memory.current", stats, ok)
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

// TestWatcherPressureUsesUnreclaimable is the regression test for the false
// alarm: memory.current at 93% of the limit, almost all of it page cache.
func TestWatcherPressureUsesUnreclaimable(t *testing.T) {
	const limit = 64 * gib // 68.7 GB

	t.Run("measured machine at 93 percent of memory.current has no pressure", func(t *testing.T) {
		if cur := float64(639*gb/10) / float64(limit); cur < 0.92 {
			t.Fatalf("fixture drifted: memory.current is only %.2f of the limit", cur)
		}
		s := pressureAfterPoll(t, 639*gb/10, limit, measuredMachineStat())
		if s.Pressure {
			t.Errorf("pressure asserted on a healthy machine: %+v", s)
		}
		if s.UnreclaimableBytes >= s.CurrentBytes || s.CurrentBytes != 639*gb/10 {
			t.Errorf("sample = %+v, want unreclaimable below memory.current, current kept", s)
		}
	})

	// A build machine: anon 2.3 GB, the rest file cache — almost all of it
	// ACTIVE because builds reread the same Go cache all day — and inode
	// cache. Counting active_file as used put this at 86% of the limit.
	t.Run("hot file cache with little anon has no pressure", func(t *testing.T) {
		stat := memoryStatFixture(
			[2]string{"anon", u(23 * gb / 10)},
			[2]string{"file", u(58 * gb)},
			[2]string{"active_file", u(56 * gb)},
			[2]string{"inactive_file", u(2 * gb)},
			[2]string{"slab_reclaimable", u(7 * gb)},
			[2]string{"slab_unreclaimable", u(gb / 2)},
		)
		s := pressureAfterPoll(t, 68*gb, limit, stat)
		if s.Pressure {
			t.Errorf("pressure asserted on hot page cache: %+v", s)
		}
		if s.UnreclaimableBytes != 3*gb {
			t.Errorf("UnreclaimableBytes = %d, want 3 GB (68 - 56 - 2 - 7)", s.UnreclaimableBytes)
		}
	})

	t.Run("without slab_reclaimable the file subtraction alone is enough", func(t *testing.T) {
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
			t.Errorf("no memory.stat: unreclaimable is memory.current, 93%% must assert: %+v", s)
		}
	})

	t.Run("inactive_file larger than current does not assert or underflow", func(t *testing.T) {
		stat := memoryStatFixture([2]string{"inactive_file", u(100 * gb)})
		s := pressureAfterPoll(t, 639*gb/10, limit, stat)
		if s.Pressure || s.UnreclaimableBytes != 0 {
			t.Errorf("sample = %+v, want zero unreclaimable and no pressure", s)
		}
	})

	t.Run("garbled memory.stat degrades to memory.current", func(t *testing.T) {
		s := pressureAfterPoll(t, 639*gb/10, limit, "\x00\x01 garbage\ninactive_file NaN\n")
		if !s.Pressure || s.UnreclaimableBytes != 639*gb/10 {
			t.Errorf("sample = %+v, want unreclaimable = memory.current", s)
		}
	})
}

// TestWatcherOOMKillIndependentOfUnreclaimable pins that OOM-kill detection reads
// memory.events only, whatever memory.stat says.
func TestWatcherOOMKillIndependentOfUnreclaimable(t *testing.T) {
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

func writePSI(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "memory.pressure"), []byte(content), 0o644); err != nil {
		t.Fatalf("writing memory.pressure: %v", err)
	}
}

func psiFixture(some, full string) string {
	return "some avg10=" + some + " avg60=0.00 avg300=0.00 total=1234\n" +
		"full avg10=" + full + " avg60=0.00 avg300=0.00 total=567\n"
}

func TestParsePSI(t *testing.T) {
	for _, tt := range []struct {
		name string
		data string
		want PSI
	}{
		{"both lines", psiFixture("12.50", "3.25"), PSI{SomeAvg10: 12.5, FullAvg10: 3.25, Valid: true}},
		{"some only", "some avg10=4.00 avg60=1.00 avg300=0.10 total=9\n", PSI{SomeAvg10: 4, Valid: true}},
		{"empty", "", PSI{}},
		{"garbage", "some avg10=lots\nfull avg10=-1\n", PSI{}},
		{"out of range ignored", "some avg10=250.0\n", PSI{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := parsePSI([]byte(tt.data)); got != tt.want {
				t.Errorf("parsePSI = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestWatcherPressureFromPSI: stalls are pressure whatever the byte figure
// says — a working set thrashing through hot file cache shows nowhere else —
// and clearing needs both signals back down.
func TestWatcherPressureFromPSI(t *testing.T) {
	const limit = 64 * gib
	cache := measuredMachineStat() // ~4.7 GB unreclaimable of 68.7 GB

	dir := writeCgroupFixture(t, u(639*gb/10)+"\n", u(limit)+"\n", "", eventsFixture)
	writeStat(t, dir, cache)
	writePSI(t, dir, psiFixture("40.00", "15.00"))
	w := NewWatcher(NewReader(dir), time.Hour)
	w.poll()
	if s, _ := w.Latest(); !s.Pressure {
		t.Fatalf("PSI full 15%% must assert pressure: %+v", s)
	}

	writePSI(t, dir, psiFixture("8.00", "3.00")) // easing, still above the clear mark
	w.poll()
	if s, _ := w.Latest(); !s.Pressure {
		t.Fatal("PSI full 3% must hold an asserted state (clear mark 2%)")
	}

	writePSI(t, dir, psiFixture("0.50", "0.00"))
	w.poll()
	if s, _ := w.Latest(); s.Pressure {
		t.Fatal("no stalls and little unreclaimable memory must clear")
	}

	// Some tasks waiting on reclaim (some) without the whole cgroup stalling
	// (full) is ordinary cache turnover, not pressure.
	writePSI(t, dir, psiFixture("30.00", "1.00"))
	w.poll()
	if s, _ := w.Latest(); s.Pressure {
		t.Fatal("PSI some alone must not assert pressure")
	}
}

// TestNextPressureWithoutLimit: an unlimited cgroup can still stall.
func TestNextPressureWithoutLimit(t *testing.T) {
	if nextPressure(false, Stats{UnreclaimableBytes: 1 << 40}) {
		t.Error("bytes without a limit must not assert")
	}
	if !nextPressure(false, Stats{PSI: PSI{FullAvg10: 20, Valid: true}}) {
		t.Error("PSI full 20% must assert without a limit")
	}
}
