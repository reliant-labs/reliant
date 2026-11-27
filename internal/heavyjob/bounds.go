// Copyright (c) 2026 Reliant Labs
package heavyjob

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/pkg/hostslot"
)

// DefaultCPUWeight is the cpu.weight a slotted command's cgroup runs at,
// against the kernel's default of 100 for every other command the daemon
// runs: an agent's interactive shell, a dev server, git. cpu.weight only
// divides CPU that two cgroups both want, so an idle machine still gives a
// build every CPU its cap allows; on a saturated one a command at 100 gets
// four times the CPU of a build beside it, so the shells and servers agents
// are waiting on stay responsive. cpu.max (the share) bounds how much a build
// may take; the weight decides who yields when that is more than is free.
const DefaultCPUWeight = 25

// kernelCPUWeight is cgroup v2's default cpu.weight.
const kernelCPUWeight = 100

// DefaultMemoryHigh is the memory.high a slotted command's cgroup runs under
// when the machine has memBytes of memory shared by slots heavy slots: 1.5x
// one slot's part of the memory, and never less than 1.5x forge's per-slot
// budget (hostslot.GiBPerSlot), so a slot pool configured wider than the
// memory supports does not starve each build of it. 12 GiB on a 126 GiB
// machine with 16 slots.
//
// memory.high, not memory.max: past it the kernel reclaims the cgroup's page
// cache and slows its allocations, and never OOM-kills a test run half way
// through. A build that needs more than its part runs slower rather than
// taking the memory every other job on the machine is using. The headroom is
// the same 1.5x as the CPU share's (DefaultShare): not every slot is at its
// peak at once.
func DefaultMemoryHigh(memBytes uint64, slots int) uint64 {
	if slots < 1 {
		slots = 1
	}
	part := uint64(hostslot.GiBPerSlot) << 30
	if memBytes > 0 {
		part = max(part, memBytes/uint64(slots))
	}
	return part / 2 * 3
}

// MemoryHighOff is the Config.MemoryHigh that bounds no slotted command's
// memory.
const MemoryHighOff = -1

// ParseMemoryHigh reads the daemon's heavy_memory_high setting into a
// Config.MemoryHigh: "" is 0 (derive it, DefaultMemoryHigh), "off" is
// MemoryHighOff, and a size is that many bytes — a plain number of bytes, or
// one with a binary suffix: 12G, 12GiB, 512M, 512MiB (K, M, G and T; the i
// and B optional, any case).
func ParseMemoryHigh(s string) (int64, error) {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "":
		return 0, nil
	case "off":
		return MemoryHighOff, nil
	}
	num := strings.TrimRight(strings.ToLower(s), "kmgtib")
	unit := strings.ToLower(s[len(num):])
	unit = strings.TrimSuffix(strings.TrimSuffix(unit, "b"), "i")
	shift, ok := map[string]uint{"": 0, "k": 10, "m": 20, "g": 30, "t": 40}[unit]
	n, err := strconv.ParseUint(strings.TrimSpace(num), 10, 64)
	if !ok || err != nil || n == 0 || n > (1<<62)>>shift {
		return 0, fmt.Errorf("memory size %q: want off, a number of bytes, or one with a K, M, G or T suffix (12G, 12GiB)", s)
	}
	return int64(n << shift), nil
}

// The variables a Gate passes a configured weight and memory bound to its
// wrapper in. Variables, not flags, for the reason the lane is one: a wrapper
// binary older than the daemon (a daemon downgraded in place) neither rejects
// them nor needs to know them. Absent, the wrapper derives the defaults. The
// command does not inherit them (childEnv).
const (
	cpuWeightEnv  = "RELIANT_HEAVY_SLOT_CPU_WEIGHT"
	memoryHighEnv = "RELIANT_HEAVY_SLOT_MEMORY_HIGH"
)

// bounds is what a slotted command's wrapper lowers on its command cgroup
// while the command runs (cgroup_bounds_linux.go). A zero field lowers
// nothing.
type bounds struct {
	// weight is the cpu.weight to lower the cgroup to.
	weight int
	// memoryHigh is the memory.high, in bytes, to lower the cgroup to.
	memoryHigh uint64
}

// boundsOf is the bounds for a command holding slot, from the wrapper's
// environment env (the Gate's configuration) and the slot pool's capacity.
// None when there is no pool to share: the command runs without a slot, or
// with the limit switched off.
func boundsOf(slot *hostslot.Slot, env []string) bounds {
	if slot == nil || slot.Unlimited() || slot.Capacity().Slots < 1 {
		return bounds{}
	}
	b := bounds{weight: DefaultCPUWeight}
	if v, _ := lookup(env, cpuWeightEnv); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			b.weight = n
		}
	}
	// The kernel's default or more lowers nothing: that is how the weight
	// is switched off.
	if b.weight >= kernelCPUWeight {
		b.weight = 0
	}
	capy := slot.Capacity()
	b.memoryHigh = DefaultMemoryHigh(capy.Machine.MemBytes, capy.Slots)
	if v, _ := lookup(env, memoryHighEnv); v != "" {
		if n, err := ParseMemoryHigh(v); err == nil && n != 0 {
			b.memoryHigh = uint64(max(n, 0))
		}
	}
	return b
}

// boundsEnv is the wrapper environment that passes a Gate's configured
// weight and memory bound on: nothing for what is derived.
func boundsEnv(weight int, memoryHigh int64) []string {
	var env []string
	if weight > 0 {
		env = append(env, cpuWeightEnv+"="+strconv.Itoa(weight))
	}
	switch {
	case memoryHigh < 0:
		env = append(env, memoryHighEnv+"=off")
	case memoryHigh > 0:
		env = append(env, memoryHighEnv+"="+strconv.FormatInt(memoryHigh, 10))
	}
	return env
}
