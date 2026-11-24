package observe

import (
	"context"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pprofile"
)

func TestProfileCadenceDurationBoundaries(t *testing.T) {
	const maxDuration = time.Duration(1<<63 - 1)
	for _, interval := range []time.Duration{-1, 0, 1, 1000000 * time.Hour, maxDuration - 1, maxDuration} {
		t.Run(interval.String(), func(t *testing.T) {
			clearProfilesEnv(t)
			settings, _ := resolveProfileSettings(ProfilesConfig{CPUInterval: interval})
			if settings.cpuInterval <= 0 {
				t.Fatal("config produced a nonpositive CPU interval")
			}
			t.Setenv(EnvProfilesCPUInterval, interval.String())
			settings, _ = resolveProfileSettings(ProfilesConfig{})
			if settings.cpuInterval <= 0 {
				t.Fatal("environment produced a nonpositive CPU interval")
			}
			for range 100 {
				got := initialProfileWait(interval)
				effective := interval
				if effective <= 0 {
					effective = DefaultCPUInterval
				}
				if got < effective/4 || got >= effective {
					t.Fatalf("initial wait %s outside [%s, %s)", got, effective/4, effective)
				}
				for _, failures := range []int{0, 1, 20} {
					p := profiler{interval: interval, failures: failures}
					if wait := p.nextWait(); wait <= 0 {
						t.Fatalf("next wait overflowed: %s with %d failures", wait, failures)
					}
				}
			}
		})
	}
}

func TestProfileSettingsDefaults(t *testing.T) {
	clearProfilesEnv(t)
	s, warns := resolveProfileSettings(ProfilesConfig{})
	if len(warns) > 0 {
		t.Fatalf("the zero config warned: %v", warns)
	}
	if got := s.types(); !slices.Equal(got, DefaultProfileTypes()) {
		t.Errorf("default types = %v, want %v", got, DefaultProfileTypes())
	}
	if !slices.Equal(DefaultProfileTypes(), []ProfileType{ProfileCPU, ProfileHeap}) {
		t.Errorf("the approved default is CPU and the live heap; got %v", DefaultProfileTypes())
	}
	if s.heapInterval != 2*time.Hour || DefaultHeapInterval != 2*time.Hour {
		t.Errorf("heap interval = %s, want 2h", s.heapInterval)
	}
	if s.cpuInterval != 5*time.Minute || s.cpuDuration != 5*time.Second {
		t.Errorf("CPU cycle = %s / window = %s, want 5m / 5s", s.cpuInterval, s.cpuDuration)
	}
	if got := s.heapDrops(); !slices.Equal(got, []string{"alloc_objects", "alloc_space"}) {
		t.Errorf("default heap drops = %v, want the cumulative alloc types", got)
	}
}

func TestProfileSettingsPrecedence(t *testing.T) {
	cases := map[string]struct {
		cfg          ProfilesConfig
		env          map[string]string
		wantTypes    []ProfileType
		wantInterval time.Duration
		wantWarn     string
	}{
		"config replaces the default": {
			cfg:       ProfilesConfig{Types: []ProfileType{ProfileHeap, ProfileMutex}, HeapInterval: 5 * time.Minute},
			wantTypes: []ProfileType{ProfileHeap, ProfileMutex}, wantInterval: 5 * time.Minute,
		},
		"env wins over config": {
			cfg:       ProfilesConfig{Types: []ProfileType{ProfileHeap}, HeapInterval: 5 * time.Minute},
			env:       map[string]string{EnvProfiles: "cpu, HEAP ,alloc,cpu", EnvProfilesHeapInterval: "10m"},
			wantTypes: []ProfileType{ProfileCPU, ProfileHeap, ProfileAlloc}, wantInterval: 10 * time.Minute,
		},
		"none selects nothing": {
			env:       map[string]string{EnvProfiles: " None "},
			wantTypes: nil, wantInterval: DefaultHeapInterval,
		},
		"every type": {
			env:       map[string]string{EnvProfiles: "block,mutex,goroutine,alloc,heap,cpu"},
			wantTypes: allProfileTypes, wantInterval: DefaultHeapInterval,
		},
		"an unknown env type is ignored with a warning": {
			cfg:       ProfilesConfig{Types: []ProfileType{ProfileHeap}},
			env:       map[string]string{EnvProfiles: "cpu,allocs"},
			wantTypes: []ProfileType{ProfileHeap}, wantInterval: DefaultHeapInterval,
			wantWarn: `unknown profile type "allocs"`,
		},
		"an empty env list is ignored with a warning": {
			env:       map[string]string{EnvProfiles: " , "},
			wantTypes: DefaultProfileTypes(), wantInterval: DefaultHeapInterval,
			wantWarn: "names no profile type",
		},
		"an unknown config type is ignored with a warning": {
			cfg:       ProfilesConfig{Types: []ProfileType{"threadcreate"}},
			wantTypes: DefaultProfileTypes(), wantInterval: DefaultHeapInterval,
			wantWarn: `unknown profile type "threadcreate"`,
		},
		"a bad interval is ignored with a warning": {
			env:       map[string]string{EnvProfilesHeapInterval: "15"},
			wantTypes: DefaultProfileTypes(), wantInterval: DefaultHeapInterval,
			wantWarn: "Go duration",
		},
		"a negative config interval is ignored with a warning": {
			cfg:       ProfilesConfig{HeapInterval: -time.Minute},
			wantTypes: DefaultProfileTypes(), wantInterval: DefaultHeapInterval,
			wantWarn: "negative",
		},
		"an interval below one cycle is raised to it": {
			env:       map[string]string{EnvProfilesHeapInterval: "10s"},
			wantTypes: DefaultProfileTypes(), wantInterval: DefaultCPUInterval,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			clearProfilesEnv(t)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			s, warns := resolveProfileSettings(c.cfg)
			if got := s.types(); !slices.Equal(got, c.wantTypes) {
				t.Errorf("types = %v, want %v", got, c.wantTypes)
			}
			if s.heapInterval != c.wantInterval {
				t.Errorf("heap interval = %s, want %s", s.heapInterval, c.wantInterval)
			}
			if c.wantWarn == "" && len(warns) > 0 {
				t.Errorf("unexpected warnings: %v", warns)
			}
			if c.wantWarn != "" && (len(warns) != 1 || !strings.Contains(warns[0].Error(), c.wantWarn)) {
				t.Errorf("warnings = %v, want one containing %q", warns, c.wantWarn)
			}
		})
	}
}

// TestHeapSnapshotFollowsTheHeapInterval drives the cycle with a fake clock:
// the heap goes out on the first cycle, then only once the interval (less half
// a cycle of jitter slack) has passed; a failed delivery is retried next cycle.
func TestHeapSnapshotFollowsTheHeapInterval(t *testing.T) {
	clearProfilesEnv(t)
	t.Setenv(EnvProfiles, "heap")
	rec := &recordingSender{}
	fail := false
	p := testProfiler(func(context.Context) error { return nil }, func(ctx context.Context, pd pprofile.Profiles) error {
		if fail {
			return context.DeadlineExceeded
		}
		return rec.send(ctx, pd)
	})
	p.settings, _ = resolveProfileSettings(ProfilesConfig{HeapInterval: 15 * time.Minute})
	clock := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return clock }

	heaps := func() int { return rec.snapshot()["inuse_space"] }
	step := func(d time.Duration) error {
		clock = clock.Add(d)
		return p.cycle(context.Background())
	}
	if err := step(0); err != nil || heaps() != 1 {
		t.Fatalf("first cycle: err=%v heaps=%d, want the first snapshot at once", err, heaps())
	}
	for i := 1; i <= 14; i++ {
		if err := step(time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if heaps() != 1 {
		t.Fatalf("after 14 minutes heaps=%d, want still 1 (cadence 15m)", heaps())
	}
	if err := step(40 * time.Second); err != nil || heaps() != 2 {
		// 14m40s since the last one: inside the half-cycle jitter slack.
		t.Fatalf("at 14m40s heaps=%d err=%v, want the second snapshot", heaps(), err)
	}
	fail = true
	if err := step(15 * time.Minute); err == nil {
		t.Fatal("a failed delivery must fail the cycle")
	}
	fail = false
	if err := step(time.Minute); err != nil || heaps() != 3 {
		t.Fatalf("after a failed heap delivery heaps=%d err=%v, want a retry on the next cycle", heaps(), err)
	}
	if err := step(time.Minute); err != nil || heaps() != 3 {
		t.Fatalf("a delivered retry restarts the cadence; heaps=%d", heaps())
	}
}

func TestContentionSamplingIsSetOnlyWhileSelected(t *testing.T) {
	prev := runtime.SetMutexProfileFraction(-1)
	t.Cleanup(func() { runtime.SetMutexProfileFraction(prev); runtime.SetBlockProfileRate(0) })

	runtime.SetMutexProfileFraction(0)
	restore := enableContentionSampling(profileSettings{cpu: true, heap: true})
	if got := runtime.SetMutexProfileFraction(-1); got != 0 {
		t.Errorf("the default set changed the mutex fraction to %d", got)
	}
	restore()

	restore = enableContentionSampling(profileSettings{mutex: true, block: true})
	if got := runtime.SetMutexProfileFraction(-1); got != profilerMutexFraction {
		t.Errorf("mutex fraction = %d while the mutex profile is on, want %d", got, profilerMutexFraction)
	}
	restore()
	if got := runtime.SetMutexProfileFraction(-1); got != 0 {
		t.Errorf("mutex fraction = %d after stop, want the previous 0", got)
	}
}

func TestProfileCadenceSettings(t *testing.T) {
	cases := map[string]struct {
		cfg          ProfilesConfig
		env          map[string]string
		wantCycle    time.Duration
		wantWindow   time.Duration
		wantHeap     time.Duration
		wantWarnPart string
	}{
		"defaults": {wantCycle: 5 * time.Minute, wantWindow: 5 * time.Second, wantHeap: 2 * time.Hour},
		"config sets the cadence": {
			cfg:       ProfilesConfig{CPUInterval: time.Minute, CPUDuration: 10 * time.Second, HeapInterval: 15 * time.Minute},
			wantCycle: time.Minute, wantWindow: 10 * time.Second, wantHeap: 15 * time.Minute,
		},
		"env wins over config": {
			cfg:       ProfilesConfig{CPUInterval: time.Minute, CPUDuration: 10 * time.Second},
			env:       map[string]string{EnvProfilesCPUInterval: "2m", EnvProfilesCPUDuration: "3s"},
			wantCycle: 2 * time.Minute, wantWindow: 3 * time.Second, wantHeap: 2 * time.Hour,
		},
		"the heap is never faster than the cycle": {
			env:       map[string]string{EnvProfilesCPUInterval: "10m", EnvProfilesHeapInterval: "1m"},
			wantCycle: 10 * time.Minute, wantWindow: 5 * time.Second, wantHeap: 10 * time.Minute,
		},
		"a window over half the cycle is ignored": {
			env:       map[string]string{EnvProfilesCPUInterval: "1m", EnvProfilesCPUDuration: "40s"},
			wantCycle: time.Minute, wantWindow: 5 * time.Second, wantHeap: 2 * time.Hour,
			wantWarnPart: EnvProfilesCPUDuration,
		},
		"a cycle under the minimum is ignored": {
			env:       map[string]string{EnvProfilesCPUInterval: "5s"},
			wantCycle: 5 * time.Minute, wantWindow: 5 * time.Second, wantHeap: 2 * time.Hour,
			wantWarnPart: EnvProfilesCPUInterval,
		},
		"a malformed duration is ignored": {
			env:       map[string]string{EnvProfilesCPUDuration: "five"},
			wantCycle: 5 * time.Minute, wantWindow: 5 * time.Second, wantHeap: 2 * time.Hour,
			wantWarnPart: EnvProfilesCPUDuration,
		},
		"a negative config value is ignored": {
			cfg:       ProfilesConfig{CPUInterval: -time.Minute},
			wantCycle: 5 * time.Minute, wantWindow: 5 * time.Second, wantHeap: 2 * time.Hour,
			wantWarnPart: "CPUInterval",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			clearProfilesEnv(t)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			s, warns := resolveProfileSettings(c.cfg)
			if s.cpuInterval != c.wantCycle || s.cpuDuration != c.wantWindow || s.heapInterval != c.wantHeap {
				t.Errorf("cycle/window/heap = %s/%s/%s, want %s/%s/%s", s.cpuInterval, s.cpuDuration, s.heapInterval, c.wantCycle, c.wantWindow, c.wantHeap)
			}
			if c.wantWarnPart == "" && len(warns) > 0 {
				t.Errorf("unexpected warnings: %v", warns)
			}
			if c.wantWarnPart != "" && (len(warns) != 1 || !strings.Contains(warns[0].Error(), c.wantWarnPart)) {
				t.Errorf("warnings = %v, want one naming %q", warns, c.wantWarnPart)
			}
		})
	}
}
