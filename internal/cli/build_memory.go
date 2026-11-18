package cli

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Memory-aware build orchestration.
//
// `forge build` runs a full Go compile of the module AND a production
// frontend build (`next build`). Each peaks at gigabytes; run concurrently
// (the default), their combined peak can exceed a memory-constrained
// environment's limit and the OOM killer reaps the build. This is exactly
// what happens in a cloud daemon workspace pod (a 4Gi cgroup): two heavy
// compilers racing under one cap.
//
// Rather than make every constrained caller remember `--no-parallel`, forge
// probes the memory limit that actually bounds THIS process — the cgroup v2
// `memory.max` when running under a container/pod, else total system memory —
// and, when the user hasn't explicitly chosen, serializes the build and caps
// the child compilers (Go's GC via GOMEMLIMIT, the toolchain's parallelism
// via GOMAXPROCS, and Node's heap via NODE_OPTIONS) so each stays within the
// envelope. An explicit `--parallel`/`--no-parallel` always wins.

// lowMemoryThresholdBytes is the memory ceiling at or below which forge
// serializes the build by default. 8 GiB: below this a concurrent Go +
// `next build` peak is a realistic OOM risk; at or above it the machine has
// headroom for the parallel default. Chosen to keep the common developer
// laptop (16 GiB+) on the fast parallel path while catching the constrained
// cloud-daemon tiers (2–4 GiB).
const lowMemoryThresholdBytes = int64(8) << 30

// cgroupMemoryMaxPaths are the cgroup v2 (then v1) files that report the
// memory limit applied to this process. cgroup v2 writes the literal string
// "max" when unlimited; v1 reports a sentinel near int64 max.
var cgroupMemoryMaxPaths = []string{
	"/sys/fs/cgroup/memory.max",                   // cgroup v2 (unified)
	"/sys/fs/cgroup/memory/memory.limit_in_bytes", // cgroup v1
}

// buildMemoryBudget is the resolved memory picture forge builds decisions on:
// the effective limit in bytes and where it came from (for the log line).
type buildMemoryBudget struct {
	limitBytes int64  // effective memory limit; 0 => unknown/unlimited
	source     string // "cgroup", "system", or "unknown" (for logging)
}

// constrained reports whether the budget is known and at/below the low-memory
// threshold — i.e. the build should serialize + cap by default.
func (b buildMemoryBudget) constrained() bool {
	return b.limitBytes > 0 && b.limitBytes <= lowMemoryThresholdBytes
}

// detectBuildMemoryBudget resolves the memory limit bounding this build. It
// prefers the cgroup limit (the real bound inside a container/pod) and falls
// back to total system memory. A cgroup limit that is "max"/unset or larger
// than system memory is ignored in favour of the system figure, so an
// unconfined process on a big host isn't spuriously throttled.
func detectBuildMemoryBudget() buildMemoryBudget {
	sys := systemTotalMemoryBytes()
	if cg, ok := cgroupMemoryLimitBytes(); ok {
		// A cgroup limit only means something when it's tighter than the
		// host — otherwise it's the "no real cap" sentinel or a limit above
		// physical RAM, neither of which should drive throttling.
		if cg > 0 && (sys == 0 || cg < sys) {
			return buildMemoryBudget{limitBytes: cg, source: "cgroup"}
		}
	}
	if sys > 0 {
		return buildMemoryBudget{limitBytes: sys, source: "system"}
	}
	return buildMemoryBudget{limitBytes: 0, source: "unknown"}
}

// cgroupMemoryLimitBytes reads the cgroup memory limit for this process.
// Returns (limit, true) when a numeric limit is found, (0, false) when there
// is no cgroup file or the value is the "unlimited" sentinel.
func cgroupMemoryLimitBytes() (int64, bool) {
	for _, p := range cgroupMemoryMaxPaths {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		s := strings.TrimSpace(string(raw))
		if s == "" || s == "max" {
			return 0, false // cgroup v2 unlimited
		}
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			continue
		}
		// cgroup v1 reports a near-int64-max sentinel when unlimited; treat
		// anything within a page of the max as "no limit".
		if v <= 0 || v >= (1<<62) {
			return 0, false
		}
		return v, true
	}
	return 0, false
}

// resolveBuildConcurrency decides parallel-vs-sequential and derives the
// child-compiler memory caps. When the user set --parallel/--no-parallel
// explicitly (userSetParallel), that choice is honoured verbatim and no caps
// are injected — an explicit flag means "I know what I'm doing". Otherwise a
// constrained memory budget flips the build to sequential and returns the env
// caps to apply to the Go and frontend child processes.
func resolveBuildConcurrency(userSetParallel, parallelDefault bool, budget buildMemoryBudget) (parallel bool, caps buildMemoryCaps) {
	if userSetParallel {
		return parallelDefault, buildMemoryCaps{}
	}
	if budget.constrained() {
		return false, deriveBuildMemoryCaps(budget.limitBytes)
	}
	return parallelDefault, buildMemoryCaps{}
}

// buildMemoryCaps are the environment caps forge applies to the child build
// processes under a constrained budget. A zero value means "apply nothing".
type buildMemoryCaps struct {
	goMemLimit  string // GOMEMLIMIT for `go build` (e.g. "3200MiB"); "" => unset
	goMaxProcs  string // GOMAXPROCS for `go build`; "" => unset
	nodeOptions string // NODE_OPTIONS heap cap for `next build`; "" => unset
}

func (c buildMemoryCaps) empty() bool {
	return c.goMemLimit == "" && c.goMaxProcs == "" && c.nodeOptions == ""
}

// deriveBuildMemoryCaps sizes the child-compiler caps from the memory limit.
// The Go build and the frontend build run sequentially under a constrained
// budget, so each may use most of the envelope — but each is capped below the
// hard limit to leave headroom for the resident daemon/tooling and to give
// the runtime a chance to back off (GOMEMLIMIT makes Go's GC work harder
// before the cgroup OOM-kills it; NODE_OPTIONS bounds V8's heap).
func deriveBuildMemoryCaps(limitBytes int64) buildMemoryCaps {
	// Reserve ~20% (min 512 MiB) for everything that isn't the compiler:
	// the reliant daemon, shells, the entrypoint. The compiler gets the
	// rest as its soft ceiling.
	reserve := limitBytes / 5
	if min := int64(512) << 20; reserve < min {
		reserve = min
	}
	budget := limitBytes - reserve
	if budget < (256 << 20) {
		budget = 256 << 20 // never hand the compiler an absurdly small cap
	}
	budgetMiB := budget >> 20

	caps := buildMemoryCaps{
		goMemLimit:  fmt.Sprintf("%dMiB", budgetMiB),
		nodeOptions: fmt.Sprintf("--max-old-space-size=%d", budgetMiB),
	}
	// Bound Go's build parallelism: `go build` fans out to GOMAXPROCS
	// (defaults to the host core count, which inside a pod is the whole
	// node, not the pod's CPU slice), and each compiler process holds its
	// own working set. Cap it so a fat node doesn't spawn dozens of
	// concurrent compilers under a small memory limit. Scale ~1 proc per
	// 1 GiB of compiler budget, clamped to [1, host cores].
	procs := int(budgetMiB / 1024)
	if procs < 1 {
		procs = 1
	}
	if hc := runtime.NumCPU(); procs > hc {
		procs = hc
	}
	caps.goMaxProcs = strconv.Itoa(procs)
	return caps
}

// applyGoMemoryCaps appends the Go child-process caps to env (GOMEMLIMIT,
// GOMAXPROCS) when set. Appended last so they win over any inherited value,
// matching buildGoTarget's "GoBuild.env overrides" ordering for the rest.
func applyGoMemoryCaps(env []string, caps buildMemoryCaps) []string {
	if caps.goMemLimit != "" {
		env = append(env, "GOMEMLIMIT="+caps.goMemLimit)
	}
	if caps.goMaxProcs != "" {
		env = append(env, "GOMAXPROCS="+caps.goMaxProcs)
	}
	return env
}

// describeBuildBudget renders the one-line human summary forge prints so a
// serialized/capped build is never a silent surprise.
func describeBuildBudget(budget buildMemoryBudget, caps buildMemoryCaps) string {
	if budget.limitBytes <= 0 {
		return "memory limit unknown; using parallel default"
	}
	gib := float64(budget.limitBytes) / float64(int64(1)<<30)
	if caps.empty() {
		return fmt.Sprintf("memory limit %.1fGiB (%s); parallel build", gib, budget.source)
	}
	return fmt.Sprintf("memory limit %.1fGiB (%s) <= %dGiB threshold; serializing build (GOMEMLIMIT=%s GOMAXPROCS=%s NODE_OPTIONS=%q)",
		gib, budget.source, lowMemoryThresholdBytes>>30, caps.goMemLimit, caps.goMaxProcs, caps.nodeOptions)
}
