package cli

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestResolveBuildConcurrency(t *testing.T) {
	const gib = int64(1) << 30

	tests := []struct {
		name            string
		userSet         bool
		parallelDefault bool
		budget          buildMemoryBudget
		wantParallel    bool
		wantCaps        bool // expect non-empty caps
	}{
		{
			name:            "explicit parallel wins even when constrained",
			userSet:         true,
			parallelDefault: true,
			budget:          buildMemoryBudget{limitBytes: 4 * gib, source: "cgroup"},
			wantParallel:    true,
			wantCaps:        false,
		},
		{
			name:            "explicit no-parallel wins even when unconstrained",
			userSet:         true,
			parallelDefault: false,
			budget:          buildMemoryBudget{limitBytes: 64 * gib, source: "system"},
			wantParallel:    false,
			wantCaps:        false,
		},
		{
			name:            "constrained auto-serializes and caps",
			userSet:         false,
			parallelDefault: true,
			budget:          buildMemoryBudget{limitBytes: 4 * gib, source: "cgroup"},
			wantParallel:    false,
			wantCaps:        true,
		},
		{
			name:            "exactly at threshold is constrained",
			userSet:         false,
			parallelDefault: true,
			budget:          buildMemoryBudget{limitBytes: lowMemoryThresholdBytes, source: "cgroup"},
			wantParallel:    false,
			wantCaps:        true,
		},
		{
			name:            "unconstrained keeps parallel default, no caps",
			userSet:         false,
			parallelDefault: true,
			budget:          buildMemoryBudget{limitBytes: 32 * gib, source: "system"},
			wantParallel:    true,
			wantCaps:        false,
		},
		{
			name:            "unknown budget keeps parallel default, no caps",
			userSet:         false,
			parallelDefault: true,
			budget:          buildMemoryBudget{limitBytes: 0, source: "unknown"},
			wantParallel:    true,
			wantCaps:        false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parallel, caps := resolveBuildConcurrency(tc.userSet, tc.parallelDefault, tc.budget)
			if parallel != tc.wantParallel {
				t.Errorf("parallel = %v, want %v", parallel, tc.wantParallel)
			}
			if caps.empty() == tc.wantCaps {
				t.Errorf("caps.empty() = %v, want caps present=%v", caps.empty(), tc.wantCaps)
			}
		})
	}
}

func TestDeriveBuildMemoryCaps(t *testing.T) {
	const gib = int64(1) << 30

	// 4 GiB pod: reserve = max(512MiB, 4Gi/5=819MiB) = 819MiB;
	// budget = 4096 - 819 = 3277 MiB.
	caps := deriveBuildMemoryCaps(4 * gib)
	if caps.goMemLimit == "" || !strings.HasSuffix(caps.goMemLimit, "MiB") {
		t.Errorf("goMemLimit = %q, want a MiB value", caps.goMemLimit)
	}
	if !strings.HasPrefix(caps.nodeOptions, "--max-old-space-size=") {
		t.Errorf("nodeOptions = %q, want --max-old-space-size prefix", caps.nodeOptions)
	}
	if caps.goMaxProcs == "" {
		t.Fatalf("goMaxProcs unset")
	}

	// GOMAXPROCS must never exceed host cores nor drop below 1.
	caps1 := deriveBuildMemoryCaps(1 * gib) // tiny -> clamps to >=1
	if caps1.goMaxProcs != "1" && atoiOrZero(caps1.goMaxProcs) < 1 {
		t.Errorf("goMaxProcs = %q, want >= 1", caps1.goMaxProcs)
	}
	capsBig := deriveBuildMemoryCaps(6 * gib)
	if got := atoiOrZero(capsBig.goMaxProcs); got > runtime.NumCPU() {
		t.Errorf("goMaxProcs = %d, want <= host cores %d", got, runtime.NumCPU())
	}
}

func TestApplyGoMemoryCaps(t *testing.T) {
	env := applyGoMemoryCaps([]string{"PATH=/usr/bin"}, buildMemoryCaps{
		goMemLimit: "3200MiB", goMaxProcs: "3",
	})
	assertHas(t, env, "GOMEMLIMIT=3200MiB")
	assertHas(t, env, "GOMAXPROCS=3")

	// Empty caps append nothing.
	env2 := applyGoMemoryCaps([]string{"PATH=/usr/bin"}, buildMemoryCaps{})
	if len(env2) != 1 {
		t.Errorf("empty caps mutated env: %v", env2)
	}
}

func TestWithMergedNodeOptions(t *testing.T) {
	// No existing NODE_OPTIONS: append fresh.
	got := withMergedNodeOptions([]string{"PATH=/x"}, "--max-old-space-size=3200")
	assertHas(t, got, "NODE_OPTIONS=--max-old-space-size=3200")

	// Existing NODE_OPTIONS: forge's flag is appended after (wins on repeat key).
	got2 := withMergedNodeOptions([]string{"NODE_OPTIONS=--enable-source-maps"}, "--max-old-space-size=3200")
	found := ""
	for _, e := range got2 {
		if strings.HasPrefix(e, "NODE_OPTIONS=") {
			found = e
		}
	}
	if !strings.Contains(found, "--enable-source-maps") || !strings.Contains(found, "--max-old-space-size=3200") {
		t.Errorf("merged NODE_OPTIONS = %q, want both flags", found)
	}
	if strings.Index(found, "--max-old-space-size") < strings.Index(found, "--enable-source-maps") {
		t.Errorf("forge cap should come last so it wins: %q", found)
	}
}

func TestCgroupMemoryLimitParsing(t *testing.T) {
	// "max" sentinel => unlimited.
	dir := t.TempDir()
	maxFile := dir + "/memory.max"
	if err := writeFile(maxFile, "max\n"); err != nil {
		t.Fatal(err)
	}
	saved := cgroupMemoryMaxPaths
	cgroupMemoryMaxPaths = []string{maxFile}
	defer func() { cgroupMemoryMaxPaths = saved }()

	if v, ok := cgroupMemoryLimitBytes(); ok {
		t.Errorf("'max' should be unlimited, got %d", v)
	}

	// A concrete numeric limit.
	if err := writeFile(maxFile, "4294967296\n"); err != nil {
		t.Fatal(err)
	}
	v, ok := cgroupMemoryLimitBytes()
	if !ok || v != 4294967296 {
		t.Errorf("got (%d,%v), want (4294967296,true)", v, ok)
	}
}

// helpers

func assertHas(t *testing.T, env []string, want string) {
	t.Helper()
	for _, e := range env {
		if e == want {
			return
		}
	}
	t.Errorf("env missing %q: %v", want, env)
}

func atoiOrZero(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
