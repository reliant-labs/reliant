// Copyright (c) 2026 Reliant Labs
package heavyjob

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/hostslot"
)

const gib = uint64(1) << 30

func TestDefaultMemoryHigh(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mem   uint64
		slots int
		want  uint64
	}{
		{"this host: 125.7 GiB, 16 slots", 125*gib + 7*gib/10, 16, 12 * gib},
		{"wider slots than the memory supports keep forge's budget", 32 * gib, 16, 12 * gib},
		{"fewer slots give each more", 128 * gib, 4, 48 * gib},
		{"unknown memory is forge's budget", 0, 8, 12 * gib},
		{"no slot count reads as one slot", 16 * gib, 0, 24 * gib},
	} {
		if got := DefaultMemoryHigh(tc.mem, tc.slots); got != tc.want {
			t.Errorf("%s: DefaultMemoryHigh(%d, %d) = %d GiB, want %d GiB", tc.name, tc.mem, tc.slots, got/gib, tc.want/gib)
		}
	}
}

func TestParseMemoryHigh(t *testing.T) {
	for in, want := range map[string]int64{
		"":         0,
		"off":      MemoryHighOff,
		" OFF ":    MemoryHighOff,
		"16G":      16 << 30,
		"16GiB":    16 << 30,
		"16gb":     16 << 30,
		"512M":     512 << 20,
		"512MiB":   512 << 20,
		"1T":       1 << 40,
		"4096k":    4096 << 10,
		"12884901": 12884901,
	} {
		if got, err := ParseMemoryHigh(in); err != nil || got != want {
			t.Errorf("ParseMemoryHigh(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"lots", "0", "-1G", "1.5G", "12X", "G", "12 bg", "99999999999T"} {
		if got, err := ParseMemoryHigh(in); err == nil {
			t.Errorf("ParseMemoryHigh(%q) = %d, want an error", in, got)
		}
	}
}

// A slot from a pool of slots in its own directory.
func testSlot(t *testing.T, slots int) *hostslot.Slot {
	t.Helper()
	s, err := hostslot.Acquire(context.Background(), hostslot.Heavy, hostslot.Options{Dir: t.TempDir(), Slots: slots})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Release() })
	return s
}

func TestBoundsOf(t *testing.T) {
	slot := testSlot(t, 3)
	derived := DefaultMemoryHigh(slot.Capacity().Machine.MemBytes, 3)
	for _, tc := range []struct {
		name string
		env  []string
		want bounds
	}{
		{"derived", nil, bounds{weight: DefaultCPUWeight, memoryHigh: derived}},
		{"configured", []string{cpuWeightEnv + "=50", memoryHighEnv + "=17179869184"}, bounds{weight: 50, memoryHigh: 16 << 30}},
		{"weight at the kernel's default lowers nothing", []string{cpuWeightEnv + "=100"}, bounds{memoryHigh: derived}},
		{"memory off", []string{memoryHighEnv + "=off"}, bounds{weight: DefaultCPUWeight}},
		{"unreadable values keep the defaults", []string{cpuWeightEnv + "=x", memoryHighEnv + "=lots"}, bounds{weight: DefaultCPUWeight, memoryHigh: derived}},
	} {
		if got := boundsOf(slot, tc.env); got != tc.want {
			t.Errorf("%s: boundsOf = %+v, want %+v", tc.name, got, tc.want)
		}
	}
	if got := boundsOf(nil, nil); got != (bounds{}) {
		t.Errorf("a command without a slot is bounded: %+v", got)
	}
}

// The Gate passes what it was configured with to the wrapper in its
// environment, nothing for what the wrapper derives; the wrapper does not
// pass it on to the command.
func TestPreparePassesConfiguredBoundsToTheWrapperAlone(t *testing.T) {
	prepared := func(cfg Config) []string {
		t.Helper()
		cfg.Env, cfg.Self = []string{"FORGE_SLOTS_DIR=" + t.TempDir(), "FORGE_HEAVY_SLOTS=3"}, testSelf(t)
		g, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		c := exec.Command("sh", "-c", "go test ./...")
		c.Env = []string{"PATH=/bin"}
		if ok, err := g.Prepare(c, Job{Command: "go test ./..."}); !ok || err != nil {
			t.Fatalf("not rewritten: %v", err)
		}
		return c.Env
	}
	env := prepared(Config{CPUWeight: 40, MemoryHigh: 8 << 30})
	for _, want := range []string{cpuWeightEnv + "=40", memoryHighEnv + "=8589934592"} {
		if !slices.Contains(env, want) {
			t.Errorf("wrapper env %q lacks %s", env, want)
		}
	}
	if env := prepared(Config{MemoryHigh: MemoryHighOff}); !slices.Contains(env, memoryHighEnv+"=off") {
		t.Errorf("wrapper env %q does not switch the memory bound off", env)
	}
	for _, kv := range prepared(Config{}) {
		if k, _, _ := strings.Cut(kv, "="); k == cpuWeightEnv || k == memoryHighEnv {
			t.Errorf("a derived bound was passed: %s", kv)
		}
	}
	child := childEnv(append(env, "A=1"), nil)
	if !slices.Equal(child, []string{"PATH=/bin", "GOMAXPROCS=1", "A=1"}) {
		t.Errorf("the command inherits %q", child)
	}
}
