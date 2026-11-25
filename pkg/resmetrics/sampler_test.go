package resmetrics

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestSnapshotReadsEveryFixtureFile(t *testing.T) {
	f := newFixture(t)
	snap := f.start(Options{}).Latest()
	if err := snap.Err(); err != nil {
		t.Fatalf("a complete fixture reported reader errors: %v", err)
	}
	page := uint64(os.Getpagesize())

	p := snap.Process
	if p == nil {
		t.Fatal("no process group")
	}
	if want := time.Unix(fixtureBoot+100, 0); !p.StartTime.Equal(want) {
		t.Errorf("StartTime = %v, want %v (btime + starttime/USER_HZ)", p.StartTime, want)
	}
	if got := p.Uptime(snap.At); got != 900*time.Second {
		t.Errorf("Uptime = %v, want 15m0s", got)
	}
	if p.CPU == nil || !near(p.CPU.UserSeconds, 2.5) || !near(p.CPU.SystemSeconds, 0.5) {
		t.Errorf("CPU = %+v, want user 2.5s system 0.5s", p.CPU)
	}
	if p.CPU != nil && p.CPU.Utilization != nil {
		t.Error("the first sample has a utilization; there is nothing to take a rate over")
	}
	if p.Faults == nil || *p.Faults != (PageFaults{Minor: 11, Major: 2}) {
		t.Errorf("Faults = %+v, want minor 11 major 2", p.Faults)
	}
	if p.Memory == nil || p.Memory.RSS != 300*page || p.Memory.Virtual != 1000*page {
		t.Errorf("Memory = %+v, want rss %d virtual %d", p.Memory, 300*page, 1000*page)
	}
	if p.Memory != nil && p.Memory.GoGap != int64(300*page)-int64(p.GoMappedBytes) {
		t.Errorf("GoGap = %d, want rss minus GoMappedBytes (%d)", p.Memory.GoGap, p.GoMappedBytes)
	}
	if p.GoMappedBytes == 0 || p.Goroutines == 0 {
		t.Errorf("runtime fields unset: mapped %d goroutines %d", p.GoMappedBytes, p.Goroutines)
	}
	if p.Threads == nil || *p.Threads != 7 {
		t.Errorf("Threads = %v, want 7", p.Threads)
	}
	if p.ContextSwitches == nil || *p.ContextSwitches != (ContextSwitches{Voluntary: 40, Involuntary: 3}) {
		t.Errorf("ContextSwitches = %+v", p.ContextSwitches)
	}
	if p.FDCount == nil || *p.FDCount != 3 {
		t.Errorf("FDCount = %v, want 3", p.FDCount)
	}
	if p.FDLimit == nil || *p.FDLimit != 1024 {
		t.Errorf("FDLimit = %v, want the soft limit 1024", p.FDLimit)
	}
	if p.DiskIO == nil || *p.DiskIO != (DiskIO{ReadBytes: 4096, WriteBytes: 8192}) {
		t.Errorf("DiskIO = %+v", p.DiskIO)
	}

	h := snap.Host
	if h == nil {
		t.Fatal("no host group")
	}
	if h.Scope != ScopeHost {
		t.Errorf("Scope = %q, want host (the zero Options)", h.Scope)
	}
	// user+nice, system+irq+softirq.
	if want := (CPUModes{User: 11, System: 3.5, Idle: 50, IOWait: 0.5, Steal: 0.1}); h.CPU == nil || !modesNear(h.CPU.Seconds, want) {
		t.Errorf("host CPU = %+v, want %+v", h.CPU, want)
	}
	if h.Load == nil || *h.Load != (LoadAverage{One: 0.5, Five: 0.25, Fifteen: 0.1}) {
		t.Errorf("Load = %+v", h.Load)
	}
	if want := (HostMemory{Total: 8000 << 10, Free: 2000 << 10, Available: 5000 << 10, Cached: 2000 << 10, Used: 4000 << 10}); h.Memory == nil || *h.Memory != want {
		t.Errorf("Memory = %+v, want %+v", h.Memory, want)
	}
	if h.Swap == nil || *h.Swap != (Swap{Total: 1000 << 10, Free: 400 << 10}) {
		t.Errorf("Swap = %+v", h.Swap)
	}
	if h.MemoryLimit == nil || *h.MemoryLimit != 2097152 {
		t.Errorf("MemoryLimit = %v, want the cgroup's memory.max (below MemTotal)", h.MemoryLimit)
	}
	if want := min(runtime.NumCPU(), 2); h.LogicalCPUs != want {
		t.Errorf("LogicalCPUs = %d, want %d (the 1.5-core quota, rounded up)", h.LogicalCPUs, want)
	}
	if h.FileDescriptors == nil || *h.FileDescriptors != (CountLimit{Count: 960, Limit: 9000}) {
		t.Errorf("FileDescriptors = %+v", h.FileDescriptors)
	}
	if h.Processes == nil || *h.Processes != (CountLimit{Count: 2, Limit: 32768}) {
		t.Errorf("Processes = %+v, want 2 numeric /proc entries against pid_max", h.Processes)
	}
	if volumesSupported {
		if len(h.Filesystems) != 1 || h.Filesystems[0].Role != "cwd" {
			t.Errorf("Filesystems = %+v, want one (cwd): both fixture volumes are on one device", h.Filesystems)
		}
	}

	c := snap.Cgroup
	if c == nil {
		t.Fatal("no cgroup")
	}
	if ws, ok := c.WorkingSet(); !ok || ws != 1048576-48576 {
		t.Errorf("WorkingSet = %d, %v; want memory.current minus inactive_file", ws, ok)
	}
	if c.OOMKills == nil || *c.OOMKills != 1 {
		t.Errorf("OOMKills = %v", c.OOMKills)
	}
	if c.CPU == nil || c.CPU.ThrottledPeriods != 7 || c.CPU.Periods != 100 || !near(c.CPU.ThrottledSeconds, 0.25) || !near(c.CPU.UsageSeconds, 3) {
		t.Errorf("CPU = %+v", c.CPU)
	}
	if c.CPULimit == nil || !near(*c.CPULimit, 1.5) {
		t.Errorf("CPULimit = %v, want 1.5", c.CPULimit)
	}
	if ps := c.Pressure.CPU; ps == nil || !near(ps.Some.Avg10, 0.125) || !near(ps.Some.TotalSeconds, 2) || ps.Full == nil {
		t.Errorf("CPU pressure = %+v, want some avg10 0.125 (a fraction) and 2s", ps)
	}
	if ps := c.Pressure.Memory; ps == nil || ps.Full == nil || !near(ps.Full.Avg10, 0.005) || !near(ps.Full.TotalSeconds, 0.25) {
		t.Errorf("memory pressure = %+v", ps)
	}
	if c.PidsCurrent == nil || *c.PidsCurrent != 12 || c.PidsMax != nil {
		t.Errorf("pids = %v / %v, want 12 and no limit (max)", c.PidsCurrent, c.PidsMax)
	}
}

func TestHostRateUsesOnlyMeasuredModes(t *testing.T) {
	for _, noIOWaitSteal := range []bool{false, true} {
		t.Run(map[bool]string{false: "linux", true: "native"}[noIOWaitSteal], func(t *testing.T) {
			s := &Sampler{prev: counters{
				haveH:   true,
				hostCPU: CPUModes{User: 10, System: 20, Idle: 30, IOWait: 40, Steal: 50},
			}}
			c := &HostCPU{
				Seconds:       CPUModes{User: 12, System: 23, Idle: 35, IOWait: 44, Steal: 56},
				NoIOWaitSteal: noIOWaitSteal,
			}
			want := CPUModes{User: 0.1, System: 0.15, Idle: 0.25, IOWait: 0.2, Steal: 0.3}
			if noIOWaitSteal {
				// A previous source populated modes the native reader cannot measure.
				c.Seconds.IOWait, c.Seconds.Steal = 0, 0
				want = CPUModes{User: 0.2, System: 0.3, Idle: 0.5}
			}
			var next counters
			s.hostRate(c, &next)
			if c.Utilization == nil || !modesNear(*c.Utilization, want) {
				t.Fatalf("utilization = %+v, want %+v", c.Utilization, want)
			}
			if !next.haveH || next.hostCPU != c.Seconds {
				t.Fatalf("next counters = %+v, want current CPU times", next)
			}
		})
	}
}

func modesNear(a, b CPUModes) bool {
	return near(a.User, b.User) && near(a.System, b.System) && near(a.Idle, b.Idle) && near(a.IOWait, b.IOWait) && near(a.Steal, b.Steal)
}

// Each file is its own reader: one that fails costs its own fields and an
// Errors entry, never the sample.
func TestAFailedReaderDropsOnlyItsOwnFields(t *testing.T) {
	f := newFixture(t)
	f.removeProc("self/io")
	f.removeProc("meminfo")
	f.writeProc(map[string]string{"loadavg": "garbage\n"})
	f.removeCgroup("memory.events")
	snap := f.start(Options{}).Latest()

	for _, name := range []string{"/proc/self/io", "/proc/meminfo", "/proc/loadavg", "cgroup/memory.events"} {
		if snap.Errors[name] == nil {
			t.Errorf("no Errors entry for %s; got %v", name, snap.Err())
		}
	}
	if len(snap.Errors) != 4 {
		t.Errorf("Errors = %v, want exactly the four broken readers", snap.Err())
	}
	if snap.Process.DiskIO != nil || snap.Host.Memory != nil || snap.Host.Swap != nil || snap.Host.Load != nil || snap.Cgroup.OOMKills != nil {
		t.Error("a failed reader's fields are still set")
	}
	if snap.Process.CPU == nil || snap.Process.Memory == nil || snap.Host.CPU == nil || snap.Cgroup.MemoryCurrent == nil {
		t.Error("a failed reader took other readers' fields with it")
	}
	if snap.Host.MemoryLimit == nil || *snap.Host.MemoryLimit != 2097152 {
		t.Errorf("MemoryLimit = %v, want memory.max without meminfo", snap.Host.MemoryLimit)
	}
}

// With no /proc at all (a sandbox, or the readers pointed nowhere), Start
// still succeeds and the runtime-derived fields are still reported.
func TestNoProcStillSamples(t *testing.T) {
	s, err := Start(context.Background(), Options{procRoot: t.TempDir() + "/missing", cgroupRoot: t.TempDir(), Volumes: []Volume{}})
	if err != nil {
		t.Fatal(err)
	}
	snap := s.Latest()
	if snap.Process == nil || snap.Process.Goroutines == 0 || snap.Process.StartTime.IsZero() {
		t.Errorf("Process = %+v, want the runtime fields", snap.Process)
	}
	if snap.Host == nil || snap.Host.LogicalCPUs != runtime.NumCPU() {
		t.Errorf("Host = %+v, want the logical CPU count", snap.Host)
	}
	if snap.Process.CPU != nil || snap.Cgroup != nil {
		t.Error("readers over a missing /proc reported values")
	}
	if snap.Errors["/proc/self/stat"] == nil {
		t.Errorf("Errors = %v, want the missing files named", snap.Err())
	}
}

func TestRatesAreDeltasBetweenSamples(t *testing.T) {
	f := newFixture(t)
	s := f.start(Options{Interval: 10 * time.Second})

	// 4s later utime +300 ticks, stime +100: 4 CPU-seconds in 4 wall seconds.
	f.writeProc(map[string]string{
		"self/stat": "4242 (cmd) S 1 4242 4242 0 -1 4194560 11 0 2 0 550 150 0 0 20 0 7 0 10000 123456789 2048 18446744073709551615\n",
		"stat":      "cpu  1200 100 400 5600 50 20 30 10 0 0\nbtime 1700000000\n",
	})
	f.advance(4 * time.Second)
	if snap := s.Latest(); snap.Process.CPU.Utilization != nil {
		t.Fatal("Latest resampled before the interval passed")
	}
	f.advance(6 * time.Second)
	snap := s.Latest()
	if u := snap.Process.CPU.Utilization; u == nil || !near(*u, 0.4) {
		t.Errorf("process utilization = %v, want 4 CPU-seconds over 10s = 0.4 cores", u)
	}
	// Deltas: user 200 ticks, system 100, idle 600, others 0, of 900.
	want := CPUModes{User: 2.0 / 9, System: 1.0 / 9, Idle: 6.0 / 9}
	if u := snap.Host.CPU.Utilization; u == nil || !modesNear(*u, want) {
		t.Errorf("host utilization = %+v, want %+v", u, want)
	}
}

func TestCgroupScopeLeavesOutTheNode(t *testing.T) {
	f := newFixture(t)
	snap := f.start(Options{Groups: GroupHost, Scope: ScopeCgroup}).Latest()
	if snap.Process != nil {
		t.Error("GroupHost sampled the process")
	}
	h := snap.Host
	if h.Scope != ScopeCgroup {
		t.Errorf("Scope = %q", h.Scope)
	}
	if h.CPU != nil || h.Load != nil || h.Memory != nil || h.Swap != nil || h.FileDescriptors != nil || h.Processes != nil {
		t.Errorf("node-wide numbers reported in cgroup scope: %+v", h)
	}
	if snap.Cgroup == nil || h.MemoryLimit == nil || *h.MemoryLimit != 2097152 {
		t.Error("cgroup scope lost the container's own numbers")
	}
	if volumesSupported && len(h.Filesystems) == 0 {
		t.Error("cgroup scope dropped the process's own volumes")
	}
}

func TestNoCgroupV2IsNotAnError(t *testing.T) {
	f := newFixture(t)
	f.removeCgroup("cgroup.controllers") // a cgroup v1 mount, say
	snap := f.start(Options{}).Latest()
	if snap.Cgroup != nil {
		t.Error("Cgroup is set without a cgroup v2 hierarchy")
	}
	if err := snap.Err(); err != nil {
		t.Errorf("a host without cgroup v2 reported errors: %v", err)
	}
	if snap.Host.MemoryLimit == nil || *snap.Host.MemoryLimit != 8000<<10 {
		t.Errorf("MemoryLimit = %v, want MemTotal without a cgroup", snap.Host.MemoryLimit)
	}
}

func TestStartRejectsUnknownOptions(t *testing.T) {
	if _, err := Start(context.Background(), Options{Groups: 1 << 5}); err == nil {
		t.Error("an unknown group bit was accepted")
	}
	if _, err := Start(context.Background(), Options{Scope: "node"}); err == nil {
		t.Error("an unknown scope was accepted")
	}
	s, err := Start(context.Background(), Options{Interval: time.Millisecond, procRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if s.opts.Interval != MinInterval {
		t.Errorf("Interval = %v, want it raised to %v", s.opts.Interval, MinInterval)
	}
}

func TestSubscribeDeliversAtOnceAndClosesOnStop(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := Start(ctx, f.options(Options{}))
	if err != nil {
		t.Fatal(err)
	}
	ch, stop := s.Subscribe(time.Hour)
	if snap, ok := <-ch; !ok || snap.Process == nil {
		t.Fatal("no sample on subscribe")
	}
	stop()
	stop() // idempotent
	if _, ok := <-ch; ok {
		t.Error("the channel delivered after stop")
	}

	ch2, _ := s.Subscribe(time.Hour)
	<-ch2
	cancel()
	if _, ok := <-ch2; ok {
		t.Error("the channel stayed open after Start's context ended")
	}
}

// On linux the real /proc of this test process must read cleanly: a reader
// that breaks against a real kernel is not caught by the fixtures.
func TestRealProcSelf(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the /proc readers run on linux only")
	}
	s, err := Start(context.Background(), Options{Groups: GroupProcess})
	if err != nil {
		t.Fatal(err)
	}
	p := s.Latest().Process
	if p.CPU == nil || p.Memory == nil || p.Threads == nil || p.FDCount == nil || p.FDLimit == nil {
		t.Fatalf("real /proc/self left fields unread: %v", s.Latest().Err())
	}
	if p.Memory.RSS == 0 || *p.Threads < 1 || *p.FDCount < 3 || *p.FDLimit < *p.FDCount {
		t.Errorf("implausible real values: %+v threads %d fds %d/%d", p.Memory, *p.Threads, *p.FDCount, *p.FDLimit)
	}
	if up := p.Uptime(time.Now()); up < 0 || up > 24*time.Hour {
		t.Errorf("uptime %v: the start time is wrong", up)
	}
}

// Options.Volumes names the filesystems by a closed set of roles: an unknown
// role is refused, a volume that cannot be read is an error that does not use
// up its role, and nil means DefaultVolumes.
func TestVolumes(t *testing.T) {
	if _, err := Start(context.Background(), Options{Volumes: []Volume{{Role: "/home/me", Path: t.TempDir()}}}); err == nil {
		t.Error("a role outside the closed set was accepted")
	}

	f := newFixture(t)
	o := f.options(Options{})
	o.Volumes = []Volume{{Role: VolumeData, Path: filepath.Join(t.TempDir(), "missing")}, {Role: VolumeData, Path: t.TempDir()}}
	s, err := Start(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	snap := s.Latest()
	if !volumesSupported {
		if snap.Host.Filesystems != nil {
			t.Errorf("Filesystems = %+v on a platform without statfs", snap.Host.Filesystems)
		}
		return
	}
	if fs := snap.Host.Filesystems; len(fs) != 1 || fs[0].Role != VolumeData || fs[0].UsedBytes+fs[0].FreeBytes == 0 {
		t.Errorf("Filesystems = %+v, want the readable data volume", fs)
	}
	if snap.Errors["filesystem/data"] == nil {
		t.Errorf("the unreadable volume was not reported: %v", snap.Err())
	}

	roles := map[string]bool{}
	for _, v := range DefaultVolumes() {
		roles[v.Role] = true
	}
	if !roles[VolumeTmp] || !roles[VolumeCwd] || len(roles) != 2 {
		t.Errorf("DefaultVolumes = %+v, want cwd and tmp", DefaultVolumes())
	}
}
