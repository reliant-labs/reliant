package resmetrics

// The cgroup v2 readers, over an injected directory (the process's own cgroup
// under the cgroup2 mount). Like the /proc readers they are plain file parsers
// and compile everywhere; each file is its own reader with its own error.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type cgroupFS struct{ dir string }

func (c cgroupFS) read(name string) ([]byte, error) { return os.ReadFile(filepath.Join(c.dir, name)) }

// maxValue reads a file holding a number or "max": nil for "max".
func (c cgroupFS) maxValue(name string) (*uint64, error) {
	b, err := c.read(name)
	if err != nil {
		return nil, err
	}
	s := strings.TrimSpace(string(b))
	if s == "max" {
		return nil, nil
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return &n, nil
}

func (c cgroupFS) uintValue(name string) (uint64, error) {
	return readUintFile(filepath.Join(c.dir, name))
}

func (c cgroupFS) memoryStat() (CgroupMemoryStat, error) {
	b, err := c.read("memory.stat")
	if err != nil {
		return CgroupMemoryStat{}, err
	}
	kv := keyValues(b, " ")
	var s CgroupMemoryStat
	var errs []error
	for key, dst := range map[string]*uint64{
		"anon": &s.Anon, "file": &s.File, "active_file": &s.ActiveFile,
		"inactive_file": &s.InactiveFile, "slab_reclaimable": &s.SlabReclaimable,
	} {
		v, err := kvUint(kv, key)
		errs = append(errs, err)
		*dst = v
	}
	if err := errors.Join(errs...); err != nil {
		return CgroupMemoryStat{}, fmt.Errorf("memory.stat: %w", err)
	}
	return s, nil
}

// oomKills reads oom_kill from memory.events (hierarchical: it counts kills in
// child cgroups too).
func (c cgroupFS) oomKills() (uint64, error) {
	b, err := c.read("memory.events")
	if err != nil {
		return 0, err
	}
	v, err := kvUint(keyValues(b, " "), "oom_kill")
	if err != nil {
		return 0, fmt.Errorf("memory.events: %w", err)
	}
	return v, nil
}

func (c cgroupFS) cpuStat() (CgroupCPU, error) {
	b, err := c.read("cpu.stat")
	if err != nil {
		return CgroupCPU{}, err
	}
	kv := keyValues(b, " ")
	usage, err := kvUint(kv, "usage_usec")
	if err != nil {
		return CgroupCPU{}, fmt.Errorf("cpu.stat: %w", err)
	}
	s := CgroupCPU{UsageSeconds: float64(usage) / 1e6}
	// The throttling keys exist only when the cpu controller is enabled for
	// the cgroup; without it there is no quota to be throttled by, and zero is
	// the truth.
	s.Periods, _ = kvUint(kv, "nr_periods")
	s.ThrottledPeriods, _ = kvUint(kv, "nr_throttled")
	throttled, _ := kvUint(kv, "throttled_usec")
	s.ThrottledSeconds = float64(throttled) / 1e6
	return s, nil
}

// cpuMax reads cpu.max ("<quota> <period>" or "max <period>") as cores: nil
// when there is no quota.
func (c cgroupFS) cpuMax() (*float64, error) {
	b, err := c.read("cpu.max")
	if err != nil {
		return nil, err
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return nil, errors.New("cpu.max: fewer than 2 fields")
	}
	if f[0] == "max" {
		return nil, nil
	}
	quota, err1 := strconv.ParseUint(f[0], 10, 64)
	period, err2 := strconv.ParseUint(f[1], 10, 64)
	if err := errors.Join(err1, err2); err != nil {
		return nil, fmt.Errorf("cpu.max: %w", err)
	}
	if period == 0 {
		return nil, errors.New("cpu.max: zero period")
	}
	cores := float64(quota) / float64(period)
	return &cores, nil
}

// pressure parses a PSI file:
//
//	some avg10=0.00 avg60=0.00 avg300=0.00 total=12345
//	full avg10=0.00 avg60=0.00 avg300=0.00 total=6789
//
// avg10 is a percentage and becomes a fraction; total is microseconds.
func (c cgroupFS) pressure(resource string) (*PSI, error) {
	name := resource + ".pressure"
	b, err := c.read(name)
	if err != nil {
		return nil, err
	}
	var psi PSI
	var haveSome bool
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		var l PSILine
		var haveAvg, haveTotal bool
		for _, kv := range f[1:] {
			k, v, _ := strings.Cut(kv, "=")
			switch k {
			case "avg10":
				pct, err := strconv.ParseFloat(v, 64)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", name, err)
				}
				l.Avg10, haveAvg = pct/100, true
			case "total":
				us, err := strconv.ParseUint(v, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", name, err)
				}
				l.TotalSeconds, haveTotal = float64(us)/1e6, true
			}
		}
		if !haveAvg || !haveTotal {
			return nil, fmt.Errorf("%s: %q line lacks avg10 or total", name, f[0])
		}
		switch f[0] {
		case "some":
			psi.Some, haveSome = l, true
		case "full":
			full := l
			psi.Full = &full
		}
	}
	if !haveSome {
		return nil, fmt.Errorf("%s: no some line", name)
	}
	return &psi, nil
}
