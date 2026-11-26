package resmetrics

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fixture is a /proc tree and a cgroup2 mount in t.TempDir(), so every linux
// reader runs hermetically on any OS, and a clock the test advances.
type fixture struct {
	t                *testing.T
	proc, cgroupRoot string
	now              time.Time
}

const fixtureBoot = 1_700_000_000

// newFixture writes a complete tree: every reader succeeds against it.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{t: t, proc: filepath.Join(dir, "proc"), cgroupRoot: filepath.Join(dir, "cgroup"), now: time.Unix(fixtureBoot+1000, 0)}
	f.writeProc(map[string]string{
		// minflt=11 majflt=2 utime=250 stime=50 (ticks) starttime=10000 (100s after boot).
		"self/stat":              "4242 (my (odd) cmd) S 1 4242 4242 0 -1 4194560 11 0 2 0 250 50 0 0 20 0 7 0 10000 123456789 2048 18446744073709551615\n",
		"self/statm":             "1000 300 100 50 0 400 0\n",
		"self/status":            "Name:\tcmd\nThreads:\t7\nvoluntary_ctxt_switches:\t40\nnonvoluntary_ctxt_switches:\t3\n",
		"self/limits":            "Limit                     Soft Limit           Hard Limit           Units\nMax open files            1024                 4096                 files\n",
		"self/io":                "rchar: 1\nwchar: 2\nread_bytes: 4096\nwrite_bytes: 8192\n",
		"self/cgroup":            "0::/kubepods/pod1/ctr\n",
		"self/fd/0":              "",
		"self/fd/1":              "",
		"self/fd/2":              "",
		"stat":                   "cpu  1000 100 300 5000 50 20 30 10 0 0\ncpu0 500 50 150 2500 25 10 15 5 0 0\nintr 1\nbtime 1700000000\n",
		"loadavg":                "0.50 0.25 0.10 2/300 4242\n",
		"meminfo":                "MemTotal:       8000 kB\nMemFree:        2000 kB\nMemAvailable:   5000 kB\nBuffers:         500 kB\nCached:         1000 kB\nSReclaimable:    500 kB\nSwapTotal:      1000 kB\nSwapFree:        400 kB\n",
		"sys/fs/file-nr":         "960\t0\t9000\n",
		"sys/kernel/pid_max":     "32768\n",
		"sys/kernel/threads-max": "60000\n",
		"1/stat":                 "",
		"4242/stat":              "",
	})
	f.writeCgroup(map[string]string{
		"cgroup.controllers": "cpu memory pids io\n",
		"memory.current":     "1048576\n",
		"memory.stat":        "anon 600000\nfile 400000\nactive_file 100000\ninactive_file 48576\nslab_reclaimable 2000\n",
		"memory.max":         "2097152\n",
		"memory.events":      "low 0\nhigh 0\nmax 4\noom 1\noom_kill 1\n",
		"cpu.stat":           "usage_usec 3000000\nuser_usec 2000000\nsystem_usec 1000000\nnr_periods 100\nnr_throttled 7\nthrottled_usec 250000\n",
		"cpu.max":            "150000 100000\n",
		"cpu.pressure":       "some avg10=12.50 avg60=1.00 avg300=0.50 total=2000000\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
		"memory.pressure":    "some avg10=1.00 avg60=0.00 avg300=0.00 total=500000\nfull avg10=0.50 avg60=0.00 avg300=0.00 total=250000\n",
		"io.pressure":        "some avg10=0.00 avg60=0.00 avg300=0.00 total=100\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=50\n",
		"pids.current":       "12\n",
		"pids.max":           "max\n",
	})
	return f
}

func (f *fixture) writeProc(files map[string]string) { f.write(f.proc, files) }

func (f *fixture) writeCgroup(files map[string]string) {
	f.write(filepath.Join(f.cgroupRoot, "kubepods", "pod1", "ctr"), files)
}

func (f *fixture) write(root string, files map[string]string) {
	f.t.Helper()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *fixture) removeProc(name string) {
	f.t.Helper()
	if err := os.RemoveAll(filepath.Join(f.proc, filepath.FromSlash(name))); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) removeCgroup(name string) {
	f.t.Helper()
	if err := os.Remove(filepath.Join(f.cgroupRoot, "kubepods", "pod1", "ctr", name)); err != nil {
		f.t.Fatal(err)
	}
}

// options points a Sampler at the fixture. The two volumes are t.TempDir()
// directories, so they share a device and only the first is reported.
func (f *fixture) options(o Options) Options {
	o.procRoot, o.cgroupRoot = f.proc, f.cgroupRoot
	o.Volumes = []Volume{{Role: VolumeCwd, Path: f.t.TempDir()}, {Role: VolumeTmp, Path: f.t.TempDir()}}
	o.now = func() time.Time { return f.now }
	return o
}

func (f *fixture) start(o Options) *Sampler {
	f.t.Helper()
	s, err := Start(context.Background(), f.options(o))
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

// advance moves the clock past the interval, so the next Latest resamples.
func (f *fixture) advance(d time.Duration) { f.now = f.now.Add(d) }
