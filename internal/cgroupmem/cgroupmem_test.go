// Copyright (c) 2025 Reliant Labs
package cgroupmem

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeCgroupFixture creates a fake cgroup v2 memory directory. Pass an empty
// string to skip a file entirely.
func writeCgroupFixture(t *testing.T, current, max, peak, events string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"memory.current": current,
		"memory.max":     max,
		"memory.peak":    peak,
		"memory.events":  events,
	}
	for name, content := range files {
		if content == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("writing fixture %s: %v", name, err)
		}
	}
	return dir
}

const eventsFixture = `low 0
high 42
max 12
oom 5
oom_kill 3
oom_group_kill 0
`

func TestReaderStats(t *testing.T) {
	dir := writeCgroupFixture(t, "1073741824\n", "4294967296\n", "2147483648\n", eventsFixture)
	r := NewReader(dir)

	if !r.Available() {
		t.Fatal("expected reader to be available")
	}
	stats, ok := r.Stats()
	if !ok {
		t.Fatal("expected stats to be readable")
	}
	if stats.UsedBytes != 1<<30 {
		t.Errorf("UsedBytes = %d, want %d", stats.UsedBytes, 1<<30)
	}
	if stats.LimitBytes != 4<<30 {
		t.Errorf("LimitBytes = %d, want %d", stats.LimitBytes, uint64(4<<30))
	}
	if stats.PeakBytes != 2<<30 {
		t.Errorf("PeakBytes = %d, want %d", stats.PeakBytes, uint64(2<<30))
	}
}

func TestReaderStatsUnlimited(t *testing.T) {
	dir := writeCgroupFixture(t, "512\n", "max\n", "", eventsFixture)
	r := NewReader(dir)

	stats, ok := r.Stats()
	if !ok {
		t.Fatal("expected stats to be readable")
	}
	if stats.LimitBytes != 0 {
		t.Errorf("LimitBytes = %d, want 0 for 'max'", stats.LimitBytes)
	}
	if stats.PeakBytes != 0 {
		t.Errorf("PeakBytes = %d, want 0 when memory.peak absent", stats.PeakBytes)
	}
}

func TestReaderAbsentCgroup(t *testing.T) {
	r := NewReader(filepath.Join(t.TempDir(), "does-not-exist"))

	if r.Available() {
		t.Error("expected Available() == false for missing dir")
	}
	if _, ok := r.Stats(); ok {
		t.Error("expected Stats() ok == false for missing dir")
	}
	if _, ok := r.OOMKillCount(); ok {
		t.Error("expected OOMKillCount() ok == false for missing dir")
	}
	snap := r.SnapshotOOMKills()
	if r.OOMKillsSince(snap) {
		t.Error("expected OOMKillsSince == false for missing dir")
	}
	if oom, _ := r.CheckOOMKill(137, snap); oom {
		t.Error("expected CheckOOMKill == false for missing dir")
	}
}

func TestOOMKillCountParsing(t *testing.T) {
	tests := []struct {
		name   string
		events string
		want   uint64
		wantOK bool
	}{
		{"normal", eventsFixture, 3, true},
		{"zero", "low 0\noom_kill 0\n", 0, true},
		{"missing key", "low 0\nhigh 0\n", 0, false},
		{"garbage value", "oom_kill abc\n", 0, false},
		{"no trailing newline", "oom_kill 7", 7, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeCgroupFixture(t, "1\n", "max\n", "", tt.events)
			count, ok := NewReader(dir).OOMKillCount()
			if ok != tt.wantOK || count != tt.want {
				t.Errorf("OOMKillCount() = (%d, %v), want (%d, %v)", count, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// bumpOOMKill rewrites memory.events with a new oom_kill value.
func bumpOOMKill(t *testing.T, dir string, count int) {
	t.Helper()
	events := strings.Replace(eventsFixture, "oom_kill 3", "oom_kill "+strconv.Itoa(count), 1)
	if err := os.WriteFile(filepath.Join(dir, "memory.events"), []byte(events), 0o644); err != nil {
		t.Fatalf("rewriting memory.events: %v", err)
	}
}

func TestOOMKillsSince(t *testing.T) {
	dir := writeCgroupFixture(t, "1073741824\n", "4294967296\n", "", eventsFixture)
	r := NewReader(dir)

	snap := r.SnapshotOOMKills()
	if r.OOMKillsSince(snap) {
		t.Error("no kill happened yet — expected false")
	}

	bumpOOMKill(t, dir, 4)
	if !r.OOMKillsSince(snap) {
		t.Error("counter bumped — expected true")
	}
}

func TestCheckOOMKill(t *testing.T) {
	dir := writeCgroupFixture(t, "4026531840\n", "4294967296\n", "4290772992\n", eventsFixture)
	r := NewReader(dir)

	t.Run("sigkill exit with counter bump is OOM", func(t *testing.T) {
		snap := r.SnapshotOOMKills()
		bumpOOMKill(t, dir, 4)
		oom, msg := r.CheckOOMKill(137, snap)
		if !oom {
			t.Fatal("expected OOM classification")
		}
		want := "command was killed: workspace out of memory (used ~4.0 GiB of 4.0 GiB). Upgrade the machine size or reduce the command's memory usage."
		if msg != want {
			t.Errorf("message = %q, want %q", msg, want)
		}
		bumpOOMKill(t, dir, 3) // restore
	})

	t.Run("signal-killed (-1) with counter bump is OOM", func(t *testing.T) {
		snap := r.SnapshotOOMKills()
		bumpOOMKill(t, dir, 4)
		if oom, _ := r.CheckOOMKill(-1, snap); !oom {
			t.Error("expected OOM classification for exit code -1")
		}
		bumpOOMKill(t, dir, 3)
	})

	t.Run("sigkill exit without counter bump is not OOM", func(t *testing.T) {
		snap := r.SnapshotOOMKills()
		if oom, _ := r.CheckOOMKill(137, snap); oom {
			t.Error("no oom_kill recorded — must not classify as OOM")
		}
	})

	t.Run("ordinary failure with concurrent OOM is not OOM", func(t *testing.T) {
		snap := r.SnapshotOOMKills()
		bumpOOMKill(t, dir, 4)
		if oom, _ := r.CheckOOMKill(1, snap); oom {
			t.Error("exit code 1 must keep its real error even if the counter moved")
		}
		bumpOOMKill(t, dir, 3)
	})
}

func TestOOMKillMessageFallbacks(t *testing.T) {
	msgNoStats := OOMKillMessage(Stats{}, false)
	if msgNoStats != "command was killed: workspace out of memory. Upgrade the machine size or reduce the command's memory usage." {
		t.Errorf("unexpected fallback message: %q", msgNoStats)
	}
	// No limit — also falls back (can't say "of Y").
	msgNoLimit := OOMKillMessage(Stats{UsedBytes: 123}, true)
	if !strings.Contains(msgNoLimit, "workspace out of memory.") {
		t.Errorf("expected generic message without limit, got %q", msgNoLimit)
	}
	// Peak preferred over (post-kill, already dropped) current.
	msg := OOMKillMessage(Stats{UsedBytes: 1 << 20, LimitBytes: 4 << 30, PeakBytes: 4 << 30}, true)
	if !strings.Contains(msg, "used ~4.0 GiB of 4.0 GiB") {
		t.Errorf("expected peak-based usage, got %q", msg)
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		in   uint64
		want string
	}{
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{3 << 20, "3.0 MiB"},
		{7<<30 + 512<<20, "7.5 GiB"},
	}
	for _, tt := range tests {
		if got := FormatBytes(tt.in); got != tt.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
