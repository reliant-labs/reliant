package resmetrics

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"runtime"
	rtmetrics "runtime/metrics"
	"slices"
	"sync"
	"time"
)

// Group selects what a Sampler reads.
type Group uint8

const (
	// GroupProcess is the binary itself.
	GroupProcess Group = 1 << iota
	// GroupHost is the machine, or the container's cgroup (see Scope).
	GroupHost

	// GroupAll is both groups.
	GroupAll = GroupProcess | GroupHost
)

const (
	// DefaultInterval is how old a sample may be before Latest takes a new
	// one.
	DefaultInterval = 15 * time.Second
	// MinInterval is the shortest sampling interval; a shorter one is raised
	// to it.
	MinInterval = time.Second

	// maxVolumes caps the filesystems reported, so the series count is fixed.
	maxVolumes = 4
)

// processStart stands in for the kernel's process start time where it cannot
// be read: package initialisation runs during the binary's start-up.
var processStart = time.Now()

// Options configures a Sampler. The zero value samples both groups, with the
// whole machine as the host view, every DefaultInterval.
type Options struct {
	// Groups selects the groups to sample; zero means GroupAll.
	Groups Group
	// Scope is the host group's view: ScopeHost (the zero value) reports the
	// machine, ScopeCgroup only the container's share. A caller inside a
	// Kubernetes pod sets ScopeCgroup, so a pod does not report its node.
	Scope Scope
	// Interval is how old a sample may be before Latest takes a new one;
	// zero means DefaultInterval, and it is at least MinInterval.
	Interval time.Duration
	// Volumes are the filesystems the host group reports, in order, at most
	// four, one per device and one per role. Nil means DefaultVolumes; an
	// empty, non-nil slice reports none. Start rejects a role other than
	// VolumeCwd, VolumeTmp and VolumeData, since the role is the metric
	// attribute and its value set is closed.
	Volumes []Volume

	// procRoot and cgroupRoot replace the platform's /proc and cgroup2 mount
	// in tests; empty is the platform default (none outside linux).
	procRoot, cgroupRoot string
	// now replaces time.Now in tests.
	now func() time.Time
}

// The roles a Volume may have: the values of the forge.filesystem.role
// attribute on the system.filesystem.* metrics.
const (
	// VolumeCwd is the working directory's filesystem.
	VolumeCwd = "cwd"
	// VolumeTmp is the temporary directory's (os.TempDir).
	VolumeTmp = "tmp"
	// VolumeData is an app's own data: a cache, a database, a workspace.
	VolumeData = "data"
)

// volumeRoles is every role a Volume may have.
var volumeRoles = []string{VolumeCwd, VolumeTmp, VolumeData}

// Volume is a filesystem the host group reports, named in the metrics by its
// Role and never by its path.
type Volume struct {
	// Role is VolumeCwd, VolumeTmp or VolumeData.
	Role string
	// Path is any path on the filesystem. It is statted, never reported.
	Path string
}

// DefaultVolumes is what a Sampler reports when Options.Volumes is nil: the
// working directory's filesystem (when it can be read) and the temporary
// directory's. An app that writes elsewhere appends its own:
//
//	opts.Volumes = append(resmetrics.DefaultVolumes(), resmetrics.Volume{Role: resmetrics.VolumeData, Path: cacheDir})
func DefaultVolumes() []Volume {
	vols := []Volume{{Role: VolumeTmp, Path: os.TempDir()}}
	if wd, err := os.Getwd(); err == nil {
		vols = append([]Volume{{Role: VolumeCwd, Path: wd}}, vols...)
	}
	return vols
}

// Sampler reads the process and its host on demand and caches the result. It
// runs no goroutine of its own: a sample is taken when Latest finds the cached
// one older than the interval, or on a Subscribe tick. All methods are safe for
// concurrent use.
type Sampler struct {
	ctx  context.Context
	opts Options
	proc procFS

	mu   sync.Mutex
	last Snapshot
	prev counters
}

// counters are the last successfully read cumulative CPU times, for the
// rates. A reader that fails one tick leaves its previous value in place, so
// the next rate spans the gap rather than going missing.
type counters struct {
	procAt          time.Time
	procCPU         float64
	hostCPU         CPUModes
	haveProc, haveH bool
}

// Start validates opts and takes the first sample, which is the baseline the
// first CPU utilization is measured from. Cancelling ctx ends every
// subscription; Latest keeps working.
func Start(ctx context.Context, opts Options) (*Sampler, error) {
	if opts.Groups == 0 {
		opts.Groups = GroupAll
	}
	if opts.Groups&^GroupAll != 0 {
		return nil, fmt.Errorf("resmetrics: unknown group bits %#x", uint8(opts.Groups&^GroupAll))
	}
	switch opts.Scope {
	case "":
		opts.Scope = ScopeHost
	case ScopeHost, ScopeCgroup:
	default:
		return nil, fmt.Errorf("resmetrics: unknown scope %q (want %q or %q)", opts.Scope, ScopeHost, ScopeCgroup)
	}
	for _, v := range opts.Volumes {
		if !slices.Contains(volumeRoles, v.Role) {
			return nil, fmt.Errorf("resmetrics: unknown volume role %q (want one of %v)", v.Role, volumeRoles)
		}
	}
	if opts.Interval == 0 {
		opts.Interval = DefaultInterval
	}
	opts.Interval = max(opts.Interval, MinInterval)
	if opts.procRoot == "" {
		opts.procRoot = defaultProcRoot
	}
	if opts.cgroupRoot == "" {
		opts.cgroupRoot = defaultCgroupRoot
	}
	if opts.now == nil {
		opts.now = time.Now
	}
	s := &Sampler{ctx: ctx, opts: opts, proc: procFS{root: opts.procRoot}}
	s.mu.Lock()
	s.last = s.sample(opts.now())
	s.mu.Unlock()
	return s, nil
}

// Groups reports the groups this sampler reads.
func (s *Sampler) Groups() Group { return s.opts.Groups }

// Latest returns the most recent sample, taking a new one first when the
// cached one is older than the interval. The Snapshot and everything it points
// to is shared with other callers: read it, do not modify it.
func (s *Sampler) Latest() Snapshot { return s.fresh(s.opts.Interval) }

// fresh returns a sample no older than maxAge.
func (s *Sampler) fresh(maxAge time.Duration) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opts.now()
	if now.Sub(s.last.At) >= maxAge {
		s.last = s.sample(now)
	}
	return s.last
}

// Subscribe delivers a fresh sample now and then every interval (at least
// MinInterval) until the returned stop function is called or Start's context
// ends; then the channel is closed. A receiver that falls behind gets the
// newest sample, not a backlog. Each subscription runs one goroutine.
func (s *Sampler) Subscribe(every time.Duration) (<-chan Snapshot, func()) {
	every = max(every, MinInterval)
	ch := make(chan Snapshot, 1)
	done := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(done) }) }
	send := func(snap Snapshot) {
		select {
		case ch <- snap:
			return
		default:
		}
		select { // drop the unread older sample
		case <-ch:
		default:
		}
		select {
		case ch <- snap:
		default:
		}
	}
	send(s.fresh(MinInterval))
	go func() {
		defer close(ch)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-done:
				return
			case <-t.C:
				send(s.fresh(MinInterval))
			}
		}
	}()
	return ch, stop
}

// sample reads every enabled reader once. The caller holds s.mu.
func (s *Sampler) sample(now time.Time) Snapshot {
	snap := Snapshot{At: now}
	errs := map[string]error{}
	fail := func(name string, err error) {
		if err != nil {
			errs[name] = err
		}
	}
	linux := s.opts.procRoot != ""
	wantProc := s.opts.Groups&GroupProcess != 0
	wantHost := s.opts.Groups&GroupHost != 0
	nodeWide := wantHost && s.opts.Scope == ScopeHost

	var st procStat
	stOK := false
	if linux && (wantProc || nodeWide) {
		var err error
		st, err = s.proc.stat()
		fail("/proc/stat", err)
		stOK = err == nil
	}

	next := s.prev
	if wantProc {
		snap.Process = s.sampleProcess(now, linux, st, stOK, &next, fail)
	}
	if wantHost {
		snap.Cgroup = s.sampleCgroup(linux, fail)
		snap.Host = s.sampleHost(linux, nodeWide, st, stOK, snap.Cgroup, &next, fail)
	}
	s.prev = next
	if len(errs) > 0 {
		snap.Errors = errs
	}
	return snap
}

func (s *Sampler) sampleProcess(now time.Time, linux bool, st procStat, stOK bool, next *counters, fail func(string, error)) *ProcessStats {
	p := &ProcessStats{StartTime: processStart, Goroutines: runtime.NumGoroutine(), GoMappedBytes: goMappedBytes()}
	if !linux {
		if nativeReaders {
			readProcess(p, fail)
			if p.CPU != nil {
				s.processRate(now, p.CPU, next)
			}
			if p.Memory != nil {
				p.Memory.GoGap = int64(p.Memory.RSS) - int64(p.GoMappedBytes) //nolint:gosec // memory sizes fit in int64
			}
		}
		return p
	}
	if self, err := s.proc.selfStat(); err != nil {
		fail("/proc/self/stat", err)
	} else {
		if stOK {
			p.StartTime = st.bootTime.Add(time.Duration(self.startTicks) * time.Second / userHZ)
		}
		p.CPU = &ProcessCPU{UserSeconds: self.userSeconds, SystemSeconds: self.systemSeconds}
		s.processRate(now, p.CPU, next)
		p.Faults = &PageFaults{Minor: self.minorFaults, Major: self.majorFaults}
	}
	if rss, virt, err := s.proc.selfStatm(); err != nil {
		fail("/proc/self/statm", err)
	} else {
		p.Memory = &ProcessMemory{RSS: rss, Virtual: virt, GoGap: int64(rss) - int64(p.GoMappedBytes)} //nolint:gosec // memory sizes fit in int64
	}
	if threads, cs, err := s.proc.selfStatus(); err != nil {
		fail("/proc/self/status", err)
	} else {
		p.Threads, p.ContextSwitches = &threads, &cs
	}
	if n, err := s.proc.selfFDCount(); err != nil {
		fail("/proc/self/fd", err)
	} else {
		p.FDCount = &n
	}
	if n, err := s.proc.selfFDLimit(); err != nil {
		fail("/proc/self/limits", err)
	} else {
		p.FDLimit = &n
	}
	if io, err := s.proc.selfIO(); err != nil {
		fail("/proc/self/io", err)
	} else {
		p.DiskIO = &io
	}
	return p
}

// sampleCgroup reads the process's cgroup v2; nil when there is none to read.
func (s *Sampler) sampleCgroup(linux bool, fail func(string, error)) *CgroupStats {
	if !linux || s.opts.cgroupRoot == "" {
		return nil
	}
	rel, err := s.proc.selfCgroup()
	if err != nil {
		fail("/proc/self/cgroup", err)
		return nil
	}
	dir := filepath.Join(s.opts.cgroupRoot, filepath.FromSlash(rel))
	if _, err := os.Stat(filepath.Join(dir, "cgroup.controllers")); err != nil {
		// Not a cgroup v2 hierarchy (cgroup v1, or nothing mounted): there is
		// nothing here to read, which is not a failure.
		return nil
	}
	fs := cgroupFS{dir: dir}
	c := &CgroupStats{}
	read := func(name string, f func() error) { fail("cgroup/"+name, f()) }
	read("memory.current", func() error {
		v, err := fs.uintValue("memory.current")
		if err == nil {
			c.MemoryCurrent = &v
		}
		return err
	})
	read("memory.stat", func() error {
		v, err := fs.memoryStat()
		if err == nil {
			c.MemoryStat = &v
		}
		return err
	})
	read("memory.max", func() (err error) { c.MemoryMax, err = fs.maxValue("memory.max"); return err })
	read("memory.events", func() error {
		v, err := fs.oomKills()
		if err == nil {
			c.OOMKills = &v
		}
		return err
	})
	read("cpu.stat", func() error {
		v, err := fs.cpuStat()
		if err == nil {
			c.CPU = &v
		}
		return err
	})
	read("cpu.max", func() (err error) { c.CPULimit, err = fs.cpuMax(); return err })
	read("cpu.pressure", func() (err error) { c.Pressure.CPU, err = fs.pressure("cpu"); return err })
	read("memory.pressure", func() (err error) { c.Pressure.Memory, err = fs.pressure("memory"); return err })
	read("io.pressure", func() (err error) { c.Pressure.IO, err = fs.pressure("io"); return err })
	read("pids.current", func() error {
		v, err := fs.uintValue("pids.current")
		if err == nil {
			c.PidsCurrent = &v
		}
		return err
	})
	read("pids.max", func() (err error) { c.PidsMax, err = fs.maxValue("pids.max"); return err })
	return c
}

func (s *Sampler) sampleHost(linux, nodeWide bool, st procStat, stOK bool, cg *CgroupStats, next *counters, fail func(string, error)) *HostStats {
	h := &HostStats{Scope: s.opts.Scope, LogicalCPUs: runtime.NumCPU()}
	if cg != nil && cg.CPULimit != nil {
		if quota := int(math.Ceil(*cg.CPULimit)); quota > 0 && quota < h.LogicalCPUs {
			h.LogicalCPUs = quota
		}
	}
	if cg != nil && cg.MemoryMax != nil {
		v := *cg.MemoryMax
		h.MemoryLimit = &v
	}
	if linux {
		if mem, swap, err := s.proc.meminfo(); err != nil {
			fail("/proc/meminfo", err)
		} else {
			if h.MemoryLimit == nil || mem.Total < *h.MemoryLimit {
				total := mem.Total
				h.MemoryLimit = &total
			}
			if nodeWide {
				h.Memory, h.Swap = &mem, &swap
			}
		}
	}
	if !linux && nativeReaders {
		readHost(h, nodeWide, fail)
		if h.CPU != nil {
			s.hostRate(h.CPU, next)
		}
	}
	if linux && nodeWide {
		if stOK {
			h.CPU = &HostCPU{Seconds: st.cpu}
			s.hostRate(h.CPU, next)
		}
		if l, err := s.proc.loadavg(); err != nil {
			fail("/proc/loadavg", err)
		} else {
			h.Load = &l
		}
		if fd, err := s.proc.fileNR(); err != nil {
			fail("/proc/sys/fs/file-nr", err)
		} else {
			h.FileDescriptors = &fd
		}
		if p, err := s.proc.processes(); err != nil {
			fail("/proc", err)
		} else {
			h.Processes = &p
		}
	}
	h.Filesystems = s.filesystems(fail)
	return h
}

// processRate sets c's utilization from the CPU time of the previous sample
// that read it, and records this one for the next.
func (s *Sampler) processRate(now time.Time, c *ProcessCPU, next *counters) {
	total := c.UserSeconds + c.SystemSeconds
	if dt := now.Sub(s.prev.procAt).Seconds(); s.prev.haveProc && dt > 0 {
		u := max(total-s.prev.procCPU, 0) / dt
		c.Utilization = &u
	}
	next.procAt, next.procCPU, next.haveProc = now, total, true
}

// hostRate sets c's per-mode utilization from the CPU times of the previous
// sample that read them, and records these for the next. Unavailable modes
// contribute neither a delta nor time to the utilization denominator.
func (s *Sampler) hostRate(c *HostCPU, next *counters) {
	cur := c.Seconds
	if prev := s.prev; prev.haveH {
		d := CPUModes{
			User: cur.User - prev.hostCPU.User, System: cur.System - prev.hostCPU.System,
			Idle: cur.Idle - prev.hostCPU.Idle,
		}
		if !c.NoIOWaitSteal {
			d.IOWait = cur.IOWait - prev.hostCPU.IOWait
			d.Steal = cur.Steal - prev.hostCPU.Steal
		}
		if total := d.User + d.System + d.Idle + d.IOWait + d.Steal; total > 0 {
			c.Utilization = &CPUModes{
				User: max(d.User, 0) / total, System: max(d.System, 0) / total, Idle: max(d.Idle, 0) / total,
				IOWait: max(d.IOWait, 0) / total, Steal: max(d.Steal, 0) / total,
			}
		}
	}
	next.hostCPU, next.haveH = cur, true
}

// filesystems stats the volumes the process writes to, one per device and
// one per role, at most maxVolumes, in the order given (cwd first by default).
func (s *Sampler) filesystems(fail func(string, error)) []Filesystem {
	if !volumesSupported {
		return nil
	}
	vols := s.opts.Volumes
	if vols == nil {
		vols = []Volume{{Role: VolumeTmp, Path: os.TempDir()}}
		if wd, err := os.Getwd(); err != nil {
			fail("filesystem/cwd", err)
		} else {
			vols = append([]Volume{{Role: VolumeCwd, Path: wd}}, vols...)
		}
	}
	var out []Filesystem
	seenDev := map[uint64]bool{}
	seenRole := map[string]bool{}
	for _, v := range vols {
		if len(out) == maxVolumes {
			break
		}
		if seenRole[v.Role] {
			continue
		}
		dev, used, free, err := statVolume(v.Path)
		if err != nil {
			fail("filesystem/"+v.Role, err)
			continue
		}
		if seenDev[dev] {
			continue
		}
		seenDev[dev], seenRole[v.Role] = true, true
		out = append(out, Filesystem{Role: v.Role, UsedBytes: used, FreeBytes: free})
	}
	return out
}

var goMemorySamples = []string{"/memory/classes/total:bytes", "/memory/classes/heap/released:bytes"}

// goMappedBytes is the memory the Go runtime has mapped and not released.
func goMappedBytes() uint64 {
	samples := make([]rtmetrics.Sample, len(goMemorySamples))
	for i, name := range goMemorySamples {
		samples[i].Name = name
	}
	rtmetrics.Read(samples)
	var v [2]uint64
	for i, smp := range samples {
		if smp.Value.Kind() == rtmetrics.KindUint64 {
			v[i] = smp.Value.Uint64()
		}
	}
	if v[1] > v[0] {
		return 0
	}
	return v[0] - v[1]
}

// Err joins the snapshot's reader errors in a stable order, for a log line;
// nil when every reader succeeded.
func (snap Snapshot) Err() error {
	if len(snap.Errors) == 0 {
		return nil
	}
	errs := make([]error, 0, len(snap.Errors))
	for _, n := range slices.Sorted(maps.Keys(snap.Errors)) {
		errs = append(errs, fmt.Errorf("%s: %w", n, snap.Errors[n]))
	}
	return errors.Join(errs...)
}
