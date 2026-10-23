// File: profiles_config.go — which profiles the continuous profiler captures,
// and how often.
//
// The defaults are sized by what a profile is worth per stored row. A CPU
// profile is small and answers "where does the time go" every minute. The live
// heap changes slowly, so a snapshot every 15 minutes keeps its shape. Go's
// allocation counters (alloc_space / alloc_objects) are cumulative since process
// start: every snapshot repeats every allocation site the process has ever had,
// so its size grows with uptime. In production they were 97.7% of stored profile
// rows (PROFILES.md), which is why they, and the goroutine / mutex / block
// profiles, are opt-in.
//
// Two ways to change them, and the environment wins:
//
//   - Config.Profiles, for a binary that always wants something else;
//   - FORGE_PROFILES / FORGE_PROFILES_HEAP_INTERVAL, the deploy-time override a
//     workload's `profiling` block renders (forge KCL), so one service can turn
//     a type on for a while without a code change.

package observe

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// ProfileType is one kind of profile the continuous profiler can capture.
type ProfileType string

const (
	// ProfileCPU is a CPU profile of a 10s window, captured every cycle (about
	// once a minute). On by default.
	ProfileCPU ProfileType = "cpu"
	// ProfileHeap is the live heap (inuse_space, inuse_objects), captured every
	// HeapInterval. On by default.
	ProfileHeap ProfileType = "heap"
	// ProfileAlloc is the cumulative allocation counters since process start
	// (alloc_space, alloc_objects), captured with the heap every HeapInterval.
	// Off by default: each snapshot carries every allocation site the process
	// ever had, so it grows with uptime. Values stay cumulative; they are never
	// converted to deltas.
	ProfileAlloc ProfileType = "alloc"
	// ProfileGoroutine is every goroutine's stack, captured every cycle. Off by
	// default.
	ProfileGoroutine ProfileType = "goroutine"
	// ProfileMutex is mutex contention, captured every cycle. Off by default.
	// Enabling it also sets runtime.SetMutexProfileFraction while the profiler
	// runs, because the runtime records nothing without a sampling rate.
	ProfileMutex ProfileType = "mutex"
	// ProfileBlock is time blocked on channels and sync primitives, captured
	// every cycle. Off by default. Enabling it also sets
	// runtime.SetBlockProfileRate while the profiler runs.
	ProfileBlock ProfileType = "block"
)

// allProfileTypes is every ProfileType, in the order they are documented.
var allProfileTypes = []ProfileType{ProfileCPU, ProfileHeap, ProfileAlloc, ProfileGoroutine, ProfileMutex, ProfileBlock}

const (
	// EnvProfiles selects the profile types, as a comma-separated list of
	// ProfileType values ("cpu,heap,alloc"), or "none" for no profile at all.
	// It replaces the default set; it does not add to it.
	EnvProfiles = "FORGE_PROFILES"
	// EnvProfilesHeapInterval is how often the heap (and, when on, alloc)
	// snapshot is taken, as a Go duration ("15m", "1m").
	EnvProfilesHeapInterval = "FORGE_PROFILES_HEAP_INTERVAL"

	// DefaultHeapInterval is how often the heap snapshot is taken by default.
	DefaultHeapInterval = 15 * time.Minute

	// profilerMutexFraction is the runtime's mutex sampling rate while the mutex
	// profile is on: one contention event in this many is recorded.
	profilerMutexFraction = 10
	// profilerBlockRate is the runtime's block sampling rate while the block
	// profile is on: on average one event per this many nanoseconds blocked.
	// Events at least this long are always recorded.
	profilerBlockRate = 10_000
)

// DefaultProfileTypes is what the profiler captures when neither
// Config.Profiles.Types nor FORGE_PROFILES says otherwise: CPU and the live heap.
func DefaultProfileTypes() []ProfileType { return []ProfileType{ProfileCPU, ProfileHeap} }

// ProfilesConfig chooses which profiles the continuous profiler captures. The
// zero value is the default: CPU every cycle and the live heap every 15 minutes.
// FORGE_PROFILES and FORGE_PROFILES_HEAP_INTERVAL, when set and valid, win over
// it: they are the deploy-time override.
type ProfilesConfig struct {
	// Types is the set to capture. Nil means DefaultProfileTypes; an empty,
	// non-nil slice captures nothing, so no profiler starts.
	Types []ProfileType

	// HeapInterval is how often the heap (and alloc) snapshot is taken. Zero
	// means DefaultHeapInterval. A value shorter than the profiler's cycle
	// (one minute) is raised to it.
	HeapInterval time.Duration
}

// profileSettings is a ProfilesConfig resolved against the environment and the
// defaults: what the capture loop actually does.
type profileSettings struct {
	cpu, heap, alloc, goroutine, mutex, block bool
	heapInterval                              time.Duration
}

// enabled reports whether any profile is captured at all.
func (s profileSettings) enabled() bool {
	return s.cpu || s.heap || s.alloc || s.goroutine || s.mutex || s.block
}

// heapSnapshot reports whether the heap profile is read at all (for its live
// or its cumulative sample types).
func (s profileSettings) heapSnapshot() bool { return s.heap || s.alloc }

// heapDrops names the heap sample types that are not exported.
func (s profileSettings) heapDrops() []string {
	var drop []string
	if !s.alloc {
		drop = append(drop, "alloc_objects", "alloc_space")
	}
	if !s.heap {
		drop = append(drop, "inuse_objects", "inuse_space")
	}
	return drop
}

// types lists the enabled types, in documented order.
func (s profileSettings) types() []ProfileType {
	on := map[ProfileType]bool{ProfileCPU: s.cpu, ProfileHeap: s.heap, ProfileAlloc: s.alloc, ProfileGoroutine: s.goroutine, ProfileMutex: s.mutex, ProfileBlock: s.block}
	var out []ProfileType
	for _, t := range allProfileTypes {
		if on[t] {
			out = append(out, t)
		}
	}
	return out
}

// resolveProfileSettings applies, most specific first: the environment, then
// cfg, then the defaults. An invalid environment value is ignored and returned
// as a warning, so a typo in a deploy override never stops the service or turns
// profiling off; an invalid cfg value is a programming error and is returned the
// same way.
func resolveProfileSettings(cfg ProfilesConfig) (profileSettings, []error) {
	var warns []error

	types := DefaultProfileTypes()
	if cfg.Types != nil {
		if err := validateProfileTypes(cfg.Types); err != nil {
			warns = append(warns, fmt.Errorf("observe.Config.Profiles.Types: %w; using the default %s", err, joinProfileTypes(types)))
		} else {
			types = cfg.Types
		}
	}
	if v := envValue(EnvProfiles); v != "" {
		if parsed, err := parseProfileTypes(v); err != nil {
			warns = append(warns, fmt.Errorf("%s=%q: %w; ignoring it", EnvProfiles, v, err))
		} else {
			types = parsed
		}
	}

	interval := DefaultHeapInterval
	if cfg.HeapInterval < 0 {
		warns = append(warns, fmt.Errorf("observe.Config.Profiles.HeapInterval %s is negative; using %s", cfg.HeapInterval, interval))
	} else if cfg.HeapInterval > 0 {
		interval = cfg.HeapInterval
	}
	if v := envValue(EnvProfilesHeapInterval); v != "" {
		if d, err := time.ParseDuration(v); err != nil || d <= 0 {
			warns = append(warns, fmt.Errorf("%s=%q: want a positive Go duration such as 15m; ignoring it", EnvProfilesHeapInterval, v))
		} else {
			interval = d
		}
	}
	if interval < profilerInterval {
		interval = profilerInterval
	}

	s := profileSettings{heapInterval: interval}
	for _, t := range types {
		switch t {
		case ProfileCPU:
			s.cpu = true
		case ProfileHeap:
			s.heap = true
		case ProfileAlloc:
			s.alloc = true
		case ProfileGoroutine:
			s.goroutine = true
		case ProfileMutex:
			s.mutex = true
		case ProfileBlock:
			s.block = true
		}
	}
	return s, warns
}

// parseProfileTypes parses FORGE_PROFILES: a comma-separated list of
// ProfileType values (case-insensitive, spaces allowed), or "none".
func parseProfileTypes(v string) ([]ProfileType, error) {
	if strings.EqualFold(strings.TrimSpace(v), "none") {
		return []ProfileType{}, nil
	}
	out := []ProfileType{}
	for _, f := range strings.Split(v, ",") {
		f = strings.ToLower(strings.TrimSpace(f))
		if f == "" {
			continue
		}
		t := ProfileType(f)
		if !slices.Contains(allProfileTypes, t) {
			return nil, fmt.Errorf("unknown profile type %q (want a comma-separated list of %s, or none)", f, joinProfileTypes(allProfileTypes))
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("names no profile type (write none to turn every type off)")
	}
	return out, nil
}

func validateProfileTypes(types []ProfileType) error {
	for _, t := range types {
		if !slices.Contains(allProfileTypes, t) {
			return fmt.Errorf("unknown profile type %q (want %s)", t, joinProfileTypes(allProfileTypes))
		}
	}
	return nil
}

func joinProfileTypes(types []ProfileType) string {
	s := make([]string, len(types))
	for i, t := range types {
		s[i] = string(t)
	}
	return strings.Join(s, ",")
}
