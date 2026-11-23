package resmetrics

// The /proc readers. They are plain file parsers over an injected root, so they
// compile on every platform and are tested against fixture trees; only linux
// points them at the real /proc (platform_linux.go). Each reads one file and
// returns its own error, so one unreadable file costs one metric.
//
// Every read is of the process's own /proc/self, of a system-wide aggregate
// file, or a listing of /proc that opens nothing: no other process's files
// are read.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// userHZ is the unit of the clock-tick fields in /proc (USER_HZ). The kernel
// fixes it at 100 for user space on every architecture Go supports; sysconf
// would only read it back.
const userHZ = 100

type procFS struct{ root string }

func (p procFS) path(elem ...string) string {
	return filepath.Join(append([]string{p.root}, elem...)...)
}

func (p procFS) read(elem ...string) ([]byte, error) { return os.ReadFile(p.path(elem...)) }

// selfStat is the parts of /proc/self/stat the sampler reads.
type selfStat struct {
	userSeconds, systemSeconds float64
	minorFaults, majorFaults   uint64
	// startTicks is the process start, in clock ticks after boot.
	startTicks uint64
}

func (p procFS) selfStat() (selfStat, error) {
	b, err := p.read("self", "stat")
	if err != nil {
		return selfStat{}, err
	}
	// The command name is in parentheses and may itself hold spaces or
	// parentheses, so the fields start after the LAST ')'.
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return selfStat{}, errors.New("stat: no command field")
	}
	f := strings.Fields(string(b[i+1:]))
	// f[0] is field 3 (state); field n of proc(5) is f[n-3].
	if len(f) < 20 {
		return selfStat{}, fmt.Errorf("stat: %d fields after the command, want at least 20", len(f))
	}
	var s selfStat
	var errs []error
	u := func(n int) uint64 {
		v, err := strconv.ParseUint(f[n-3], 10, 64)
		errs = append(errs, err)
		return v
	}
	s.minorFaults = u(10)
	s.majorFaults = u(12)
	s.userSeconds = float64(u(14)) / userHZ
	s.systemSeconds = float64(u(15)) / userHZ
	s.startTicks = u(22)
	if err := errors.Join(errs...); err != nil {
		return selfStat{}, fmt.Errorf("stat: %w", err)
	}
	return s, nil
}

// selfStatm returns the resident and virtual size in bytes.
func (p procFS) selfStatm() (rss, virtual uint64, err error) {
	b, err := p.read("self", "statm")
	if err != nil {
		return 0, 0, err
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0, 0, errors.New("statm: fewer than 2 fields")
	}
	size, err1 := strconv.ParseUint(f[0], 10, 64)
	res, err2 := strconv.ParseUint(f[1], 10, 64)
	if err := errors.Join(err1, err2); err != nil {
		return 0, 0, fmt.Errorf("statm: %w", err)
	}
	page := uint64(os.Getpagesize())
	return res * page, size * page, nil
}

// selfStatus returns the thread count and context switches from
// /proc/self/status.
func (p procFS) selfStatus() (threads int64, cs ContextSwitches, err error) {
	b, err := p.read("self", "status")
	if err != nil {
		return 0, ContextSwitches{}, err
	}
	kv := keyValues(b, ":")
	var errs []error
	threads, err = kvInt(kv, "Threads")
	errs = append(errs, err)
	cs.Voluntary, err = kvUint(kv, "voluntary_ctxt_switches")
	errs = append(errs, err)
	cs.Involuntary, err = kvUint(kv, "nonvoluntary_ctxt_switches")
	errs = append(errs, err)
	if err := errors.Join(errs...); err != nil {
		return 0, ContextSwitches{}, fmt.Errorf("status: %w", err)
	}
	return threads, cs, nil
}

// selfFDCount lists /proc/self/fd. The listing's own descriptor is counted,
// as every fd counter that lists this directory counts it.
func (p procFS) selfFDCount() (int64, error) {
	ents, err := os.ReadDir(p.path("self", "fd"))
	if err != nil {
		return 0, err
	}
	return int64(len(ents)), nil
}

// selfFDLimit returns the soft open-files limit from /proc/self/limits.
func (p procFS) selfFDLimit() (int64, error) {
	b, err := p.read("self", "limits")
	if err != nil {
		return 0, err
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		rest, ok := strings.CutPrefix(sc.Text(), "Max open files")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			break
		}
		if f[0] == "unlimited" {
			return 0, errors.New("limits: open files is unlimited")
		}
		v, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("limits: %w", err)
		}
		return v, nil
	}
	return 0, errors.New("limits: no Max open files line")
}

// selfIO returns the storage bytes read and written from /proc/self/io. The
// file is unreadable without ptrace access in some sandboxes; that is one
// reader's error.
func (p procFS) selfIO() (DiskIO, error) {
	b, err := p.read("self", "io")
	if err != nil {
		return DiskIO{}, err
	}
	kv := keyValues(b, ":")
	r, err1 := kvUint(kv, "read_bytes")
	w, err2 := kvUint(kv, "write_bytes")
	if err := errors.Join(err1, err2); err != nil {
		return DiskIO{}, fmt.Errorf("io: %w", err)
	}
	return DiskIO{ReadBytes: r, WriteBytes: w}, nil
}

// procStat is the aggregate cpu line and the boot time of /proc/stat.
type procStat struct {
	cpu      CPUModes
	bootTime time.Time
}

func (p procFS) stat() (procStat, error) {
	b, err := p.read("stat")
	if err != nil {
		return procStat{}, err
	}
	var s procStat
	var haveCPU, haveBoot bool
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "cpu":
			// user nice system idle iowait irq softirq steal [guest guest_nice];
			// guest time is already inside user and nice.
			if len(f) < 9 {
				return procStat{}, fmt.Errorf("stat: cpu line has %d fields, want at least 9", len(f))
			}
			var t [8]float64
			for i := range t {
				v, err := strconv.ParseUint(f[i+1], 10, 64)
				if err != nil {
					return procStat{}, fmt.Errorf("stat: cpu: %w", err)
				}
				t[i] = float64(v) / userHZ
			}
			s.cpu = CPUModes{User: t[0] + t[1], System: t[2] + t[5] + t[6], Idle: t[3], IOWait: t[4], Steal: t[7]}
			haveCPU = true
		case "btime":
			if len(f) < 2 {
				continue
			}
			v, err := strconv.ParseInt(f[1], 10, 64)
			if err != nil {
				return procStat{}, fmt.Errorf("stat: btime: %w", err)
			}
			s.bootTime = time.Unix(v, 0)
			haveBoot = true
		}
	}
	if !haveCPU || !haveBoot {
		return procStat{}, errors.New("stat: no aggregate cpu or btime line")
	}
	return s, nil
}

func (p procFS) loadavg() (LoadAverage, error) {
	b, err := p.read("loadavg")
	if err != nil {
		return LoadAverage{}, err
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return LoadAverage{}, errors.New("loadavg: fewer than 3 fields")
	}
	var v [3]float64
	for i := range v {
		if v[i], err = strconv.ParseFloat(f[i], 64); err != nil {
			return LoadAverage{}, fmt.Errorf("loadavg: %w", err)
		}
	}
	return LoadAverage{One: v[0], Five: v[1], Fifteen: v[2]}, nil
}

// meminfo returns the machine's memory and swap from /proc/meminfo.
func (p procFS) meminfo() (HostMemory, Swap, error) {
	b, err := p.read("meminfo")
	if err != nil {
		return HostMemory{}, Swap{}, err
	}
	kv := keyValues(b, ":")
	kb := func(key string, errs *[]error) uint64 {
		v, ok := kv[key]
		if !ok {
			*errs = append(*errs, fmt.Errorf("no %s", key))
			return 0
		}
		n, err := strconv.ParseUint(strings.TrimSuffix(v, " kB"), 10, 64)
		if err != nil {
			*errs = append(*errs, fmt.Errorf("%s: %w", key, err))
		}
		return n * 1024
	}
	var errs []error
	m := HostMemory{Total: kb("MemTotal", &errs), Free: kb("MemFree", &errs), Available: kb("MemAvailable", &errs)}
	m.Cached = kb("Buffers", &errs) + kb("Cached", &errs)
	if _, ok := kv["SReclaimable"]; ok {
		m.Cached += kb("SReclaimable", &errs)
	}
	s := Swap{Total: kb("SwapTotal", &errs), Free: kb("SwapFree", &errs)}
	if err := errors.Join(errs...); err != nil {
		return HostMemory{}, Swap{}, fmt.Errorf("meminfo: %w", err)
	}
	if m.Free+m.Cached <= m.Total {
		m.Used = m.Total - m.Free - m.Cached
	}
	return m, s, nil
}

// fileNR returns the system's allocated file handles and their maximum.
func (p procFS) fileNR() (CountLimit, error) {
	b, err := p.read("sys", "fs", "file-nr")
	if err != nil {
		return CountLimit{}, err
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return CountLimit{}, errors.New("file-nr: fewer than 3 fields")
	}
	alloc, err1 := strconv.ParseUint(f[0], 10, 64)
	free, err2 := strconv.ParseUint(f[1], 10, 64)
	maxFiles, err3 := strconv.ParseUint(f[2], 10, 64)
	if err := errors.Join(err1, err2, err3); err != nil {
		return CountLimit{}, fmt.Errorf("file-nr: %w", err)
	}
	if free > alloc {
		free = alloc
	}
	return CountLimit{Count: alloc - free, Limit: maxFiles}, nil
}

// processes counts the numeric entries of /proc, opening none of them, against
// the lower of pid_max and threads-max.
func (p procFS) processes() (CountLimit, error) {
	ents, err := os.ReadDir(p.root)
	if err != nil {
		return CountLimit{}, err
	}
	var n uint64
	for _, e := range ents {
		if isDigits(e.Name()) {
			n++
		}
	}
	var errs []error
	var limit uint64
	for _, name := range []string{"pid_max", "threads-max"} {
		v, err := readUintFile(p.path("sys", "kernel", name))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if limit == 0 || v < limit {
			limit = v
		}
	}
	if limit == 0 {
		return CountLimit{}, fmt.Errorf("process limit: %w", errors.Join(errs...))
	}
	return CountLimit{Count: n, Limit: limit}, nil
}

// selfCgroup returns the process's cgroup v2 path (the "0::" line of
// /proc/self/cgroup), relative to the cgroup2 mount. Inside a cgroup
// namespace, which every container runtime uses by default on cgroup v2, it is
// "/" and the mount is the container's own cgroup.
func (p procFS) selfCgroup() (string, error) {
	b, err := p.read("self", "cgroup")
	if err != nil {
		return "", err
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "0::"); ok {
			return rest, nil
		}
	}
	return "", errors.New("cgroup: no cgroup v2 (0::) line")
}

// keyValues splits "key<sep> value" lines into a map, values trimmed.
func keyValues(b []byte, sep string) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), sep)
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

func kvUint(kv map[string]string, key string) (uint64, error) {
	v, ok := kv[key]
	if !ok {
		return 0, fmt.Errorf("no %s", key)
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func kvInt(kv map[string]string, key string) (int64, error) {
	n, err := kvUint(kv, key)
	return int64(n), err
}

func readUintFile(path string) (uint64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return n, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
