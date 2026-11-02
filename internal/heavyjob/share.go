// Copyright (c) 2025 Reliant Labs
package heavyjob

import (
	"math"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/pkg/hostslot"
)

// shareVars are the variables a slotted command's CPU share is exported as:
// GOMAXPROCS bounds every Go process the command runs (the go command, the
// compiler and linker it spawns, each test binary — and the go command's
// package parallelism, -p, which defaults to its own GOMAXPROCS), and
// UV_THREADPOOL_SIZE bounds node's libuv pool. GOFLAGS is deliberately not
// among them: with GOMAXPROCS set, an appended -p would only restate the
// default.
var shareVars = []string{gomaxprocsVar, "UV_THREADPOOL_SIZE"}

const gomaxprocsVar = "GOMAXPROCS"

// childEnv is the environment the wrapper runs its command with: its own,
// except that when the gate started it on one P (childGOMAXPROCS non-nil) the
// command gets its own GOMAXPROCS back — the value it had, or none — rather
// than the wrapper's 1.
func childEnv(env []string, childGOMAXPROCS *string) []string {
	if childGOMAXPROCS == nil {
		return env
	}
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); k != gomaxprocsVar {
			out = append(out, kv)
		}
	}
	if *childGOMAXPROCS != "" {
		out = append(out, gomaxprocsVar+"="+*childGOMAXPROCS)
	}
	return out
}

// minShare is the smallest share: a build with one CPU serializes its own
// compile and link.
const minShare = 2

// DefaultShare is the number of CPUs one heavy command may use when cpus CPUs
// are shared by slots heavy slots: max(2, 2*cpus/slots). Twice the even split,
// because a heavy job is rarely CPU-bound for its whole run — hostslot sizes
// the pool on the same assumption — so jobs that each take the even split
// leave the machine idle between their busy phases.
func DefaultShare(cpus, slots int) int {
	if slots < 1 {
		slots = 1
	}
	return max(minShare, 2*cpus/slots)
}

// shareOf is the share for a command holding slot: want when the daemon
// configured one, otherwise DefaultShare of the slot pool's machine and
// capacity. Zero — export nothing — when there is no pool to share: the
// command runs without a slot, or with the limit switched off.
func shareOf(slot *hostslot.Slot, want int) int {
	if want > 0 {
		return want
	}
	if slot == nil || slot.Unlimited() || slot.Capacity().Slots < 1 {
		return 0
	}
	capy := slot.Capacity()
	return DefaultShare(max(1, int(math.Floor(capy.Machine.CPUs))), capy.Slots)
}

// shareEnv is what to append to env so a command runs on share CPUs: each
// shareVars variable env does not already set. A command that set its own
// (GOMAXPROCS=16 go test ...) keeps it, and so does a nested slotted command,
// which inherits its parent's.
func shareEnv(env []string, share int) []string {
	if share < 1 {
		return nil
	}
	var out []string
	for _, key := range shareVars {
		if v, _ := lookup(env, key); v != "" {
			continue
		}
		out = append(out, key+"="+strconv.Itoa(share))
	}
	return out
}
