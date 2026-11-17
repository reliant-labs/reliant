package hostslot

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Machine is what the host gives this process to work with.
//
// Inside a container or a VM sandbox the cgroup limit is the truth, not the
// kernel's totals: a kata VM sized at 32 GiB reports its own MemTotal, and a
// container's /proc/meminfo reports the HOST's. So a cgroup v2 cpu.max or
// memory.max on the path from this process's cgroup up to the root wins over
// nproc and MemTotal whenever it is lower.
type Machine struct {
	// CPUs is the usable CPU count (a cgroup quota may make it fractional).
	CPUs float64
	// CPUSource names where CPUs came from: "cgroup cpu.max" or "nproc".
	CPUSource string
	// MemBytes is usable memory; 0 when it could not be read.
	MemBytes uint64
	// MemSource names where MemBytes came from: "cgroup memory.max",
	// "/proc/meminfo", "hw.memsize".
	MemSource string
}

// DetectMachine reads the CPU and memory this process may use.
func DetectMachine() Machine {
	m := Machine{CPUs: float64(runtime.NumCPU()), CPUSource: "nproc"}
	if mem, src := totalMemory(); mem > 0 {
		m.MemBytes, m.MemSource = mem, src
	}
	if lim, ok := platformCgroupLimits(); ok {
		m = lim.apply(m)
	}
	return m
}

func (m Machine) String() string {
	mem := "unknown memory"
	if m.MemBytes > 0 {
		mem = fmt.Sprintf("%s GiB (%s)", trimFloat(float64(m.MemBytes)/gib), m.MemSource)
	}
	return fmt.Sprintf("%s CPUs (%s), %s", trimFloat(m.CPUs), m.CPUSource, mem)
}

const gib = float64(1 << 30)

// CPUsPerSlot and GiBPerSlot are the default budget of one heavy job.
//
// Memory is the budget that protects the machine: with the race detector and
// a linker or two in flight a Go build or test lane takes several GiB, and
// past that the jobs slow each other down more than they gain by overlapping
// — measured on a 16 vCPU / 32 GiB VM, where `go build ./...` went from
// 36–50 s beside 0–3 other heavy jobs to 101–155 s beside 8 or more, the loss
// being memory-cgroup reclaim rather than CPU.
//
// CPU is not, so its budget is two cores, not four. Sharing CPU degrades
// gracefully: on a 16-CPU / 128 GiB Mac four CPU-bound test lanes at once took
// 161 s against 44 s for one alone — the same throughput as running them in
// turn, not a collapse. And a heavy job is rarely CPU-bound for its whole run
// (a regenerate is mostly serial steps, a test lane waits on subprocesses):
// four concurrent control-plane merge gates on that Mac left the CPU ~33% idle
// at 4 slots while 304 s of slot wait piled up; at 8 slots the worst gate's
// wall fell 139 s -> 116 s and the summed wait to 60 s (a rerun on a busier
// box: 164 s -> 122 s, wait 309 s -> 137 s). Where memory is tight — the
// 16 vCPU / 32 GiB VM above — GiBPerSlot still decides, and the count is the
// same as before.
const (
	CPUsPerSlot = 2
	GiBPerSlot  = 8
)

// DefaultSlots is the slot count the machine supports:
// min(CPUs / CPUsPerSlot, memory / GiBPerSlot), never less than 1. CPUs round
// down (a quota is exact); memory rounds to nearest, because a machine sold
// as 32 GiB reports a little under it once the kernel takes its share.
func DefaultSlots(m Machine) int {
	n := int(m.CPUs / CPUsPerSlot)
	if m.MemBytes > 0 {
		if mem := int(math.Round(float64(m.MemBytes) / gib / GiBPerSlot)); mem < n {
			n = mem
		}
	}
	if n < 1 {
		n = 1
	}
	return n
}

// Capacity is how many slots a class has, and why.
type Capacity struct {
	Class string
	// Slots is the number of jobs of this class that may run at once. Zero
	// when Unlimited.
	Slots int
	// Unlimited means the limit is switched off (FORGE_<CLASS>_SLOTS=off):
	// Acquire returns at once and takes nothing.
	Unlimited bool
	// Source says where Slots came from, for the waiting line and
	// `forge slot status`.
	Source  string
	Machine Machine
}

// SlotsEnv is the variable that overrides a class's slot count:
// FORGE_HEAVY_SLOTS for "heavy". A positive integer sets the count; "off"
// (or "unlimited") turns the limit off. The machine-derived default is right
// for a developer machine; a workspace operator sets this per plan size, and
// a CI runner that runs one job per machine may set it to off.
func SlotsEnv(class string) string {
	return "FORGE_" + strings.ToUpper(strings.ReplaceAll(class, "-", "_")) + "_SLOTS"
}

// CapacityFor resolves a class's capacity: SlotsEnv(class) in env (normally
// os.Environ()) when set, otherwise DefaultSlots of this machine.
func CapacityFor(class string, env []string) (Capacity, error) {
	if err := validClass(class); err != nil {
		return Capacity{}, err
	}
	c := Capacity{Class: class, Machine: DetectMachine()}
	name := SlotsEnv(class)
	switch v := strings.TrimSpace(lookupEnv(env, name)); strings.ToLower(v) {
	case "":
		c.Slots = DefaultSlots(c.Machine)
		c.Source = c.defaultSource()
	case "off", "unlimited":
		c.Unlimited, c.Source = true, name+"="+v
	default:
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return Capacity{}, fmt.Errorf("%s=%q: want a positive number of slots, or off", name, v)
		}
		c.Slots, c.Source = n, name+"="+v
	}
	return c, nil
}

func (c Capacity) defaultSource() string {
	cpu := fmt.Sprintf("%s CPUs / %d", trimFloat(c.Machine.CPUs), CPUsPerSlot)
	if c.Machine.MemBytes == 0 {
		return cpu + " (memory unknown)"
	}
	return fmt.Sprintf("min(%s, %s GiB / %d)", cpu, trimFloat(float64(c.Machine.MemBytes)/gib), GiBPerSlot)
}

func (c Capacity) String() string {
	if c.Unlimited {
		return "unlimited (" + c.Source + ")"
	}
	return fmt.Sprintf("%d (%s)", c.Slots, c.Source)
}

func trimFloat(f float64) string {
	return strconv.FormatFloat(math.Round(f*10)/10, 'f', -1, 64)
}

// cgroupLimit is the tightest cgroup v2 limit on the path from a process's
// cgroup to the root. Zero means no limit.
type cgroupLimit struct {
	cpus float64
	mem  uint64
}

func (l cgroupLimit) apply(m Machine) Machine {
	if l.cpus > 0 && l.cpus < m.CPUs {
		m.CPUs, m.CPUSource = l.cpus, "cgroup cpu.max"
	}
	if l.mem > 0 && (m.MemBytes == 0 || l.mem < m.MemBytes) {
		m.MemBytes, m.MemSource = l.mem, "cgroup memory.max"
	}
	return m
}

// readCgroupV2 walks from the cgroup named in selfCgroup (the unified "0::"
// line of /proc/self/cgroup) up to root, the cgroup v2 mount, keeping the
// lowest cpu.max and memory.max. A limit can sit on any ancestor: under kata
// the sandbox's limit is on the root of the guest's hierarchy while the
// process's own cgroup says "max". When the named path does not exist under
// root — a cgroup namespace mounts the process's cgroup AS the root — the walk
// starts at root itself.
func readCgroupV2(root, selfCgroup string) (cgroupLimit, bool) {
	data, err := os.ReadFile(selfCgroup)
	if err != nil {
		return cgroupLimit{}, false
	}
	rel := ""
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "0::"); ok {
			rel, found = p, true
			break
		}
	}
	if !found {
		return cgroupLimit{}, false // cgroup v1: no unified hierarchy
	}
	root = filepath.Clean(root)
	dir := filepath.Join(root, filepath.FromSlash(rel))
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		dir = root
	}
	var lim cgroupLimit
	seen := false
	for {
		if cpus, ok := readCPUMax(filepath.Join(dir, "cpu.max")); ok {
			seen = true
			if lim.cpus == 0 || cpus < lim.cpus {
				lim.cpus = cpus
			}
		}
		if mem, ok := readMemoryMax(filepath.Join(dir, "memory.max")); ok {
			seen = true
			if lim.mem == 0 || mem < lim.mem {
				lim.mem = mem
			}
		}
		if dir == root || !strings.HasPrefix(dir, root) {
			break
		}
		dir = filepath.Dir(dir)
	}
	return lim, seen
}

// readCPUMax parses cpu.max ("<quota> <period>", or "max <period>" for no
// limit) into CPUs.
func readCPUMax(path string) (float64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	f := strings.Fields(string(data))
	if len(f) == 0 || f[0] == "max" {
		return 0, false
	}
	quota, err := strconv.ParseFloat(f[0], 64)
	if err != nil || quota <= 0 {
		return 0, false
	}
	period := 100000.0
	if len(f) > 1 {
		if p, err := strconv.ParseFloat(f[1], 64); err == nil && p > 0 {
			period = p
		}
	}
	return quota / period, true
}

// readMemoryMax parses memory.max (bytes, or "max" for no limit).
func readMemoryMax(path string) (uint64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v := strings.TrimSpace(string(data))
	if v == "" || v == "max" {
		return 0, false
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil || n == 0 {
		return 0, false
	}
	return n, true
}

// meminfoTotal parses MemTotal out of /proc/meminfo-shaped text.
func meminfoTotal(data []byte) uint64 {
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(line, "MemTotal:")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			return 0
		}
		kb, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}
