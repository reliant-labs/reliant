// Copyright (c) 2026 Reliant Labs

// Package machinehealth logs one structured sample of the machine's load every
// minute, so a fleet of cloud machines can be watched over days: is a fix to
// the heavy slots, the disk guard or the timeouts moving the needle, or not.
//
// Why a log line and not a metric. A managed daemon exports no metrics of its
// own: it has no OTLP endpoint or token in its pod. Its JSON log lines are the
// telemetry path it already has. The node agent parses every line of a ws-*
// pod into tenant zero's otel_logs (reliant.source=daemon), with the line's
// keys as LogAttributes, which control-plane's dashboards and alert rules
// query like any other column. One line a minute per machine is a few hundred
// bytes; the inputs are a handful of small /proc reads.
//
// What. Two messages, both at INFO:
//
//	machine health   load and load per CPU, CPU/memory/IO pressure (PSI),
//	                 the fullest volume, the heavy-slot queue (held, waiting,
//	                 the oldest waiter's wait), concurrent `go install
//	                 .../forge@` builds
//	machine volume   one per volume diskwatch reports: free ratio and bytes
//
// Commands that hit their timeout are not counted here: every one already logs
// `exec finished` with outcome_class=timeout (daemonruntime's log policy).
//
// The attribute names are a contract with control-plane's machine-health
// dashboard and its Machine* alert rules; rename one and rename it there.
//
//forge:exclude-contract: one concrete sampler over the machine's own /proc and the slot pool; its collaborators (DiskSource, SlotSource) are the narrow func types declared here, and tests drive it through a fake /proc
package machinehealth

import (
	"bytes"
	"context"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// DefaultInterval is how often a sample is logged.
const DefaultInterval = time.Minute

// Messages of the two log lines. The dashboards match on them.
const (
	HealthMessage = "machine health"
	VolumeMessage = "machine volume"
)

// DiskSource is the machine's latest disk report (diskwatch.Watcher.Report).
type DiskSource func() (*reliantv1.DiskReport, bool)

// Slots is the heavy-slot pool at one moment.
type Slots struct {
	// Capacity is the number of slots; 0 when slots are off.
	Capacity int
	Held     int
	Waiting  int
	// OldestWait is how long the longest waiter has waited.
	OldestWait time.Duration
}

// SlotSource reads the heavy-slot pool; ok is false when it cannot.
type SlotSource func() (Slots, bool)

// Config is the sampler's inputs. Zero values take the defaults.
type Config struct {
	Interval time.Duration
	// ProcRoot is /proc; tests point it at a fake tree.
	ProcRoot string
	// CPUs is the CPU count load is divided by (the cgroup quota, or nproc).
	CPUs  float64
	Disk  DiskSource
	Slots SlotSource
	// Log writes one line; logging.Info in production.
	Log func(msg string, args ...any)
	Now func() time.Time
}

// Sampler logs a sample every Interval.
type Sampler struct {
	cfg Config
}

// New builds a sampler. It logs nothing until Run.
func New(cfg Config) *Sampler {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.ProcRoot == "" {
		cfg.ProcRoot = "/proc"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Sampler{cfg: cfg}
}

// Run logs a sample every Interval until ctx ends. Where the machine has no
// /proc/loadavg (macOS, Windows) it returns at once: there is nothing to say.
func (s *Sampler) Run(ctx context.Context) {
	if s == nil || s.cfg.Log == nil {
		return
	}
	if _, err := os.Stat(filepath.Join(s.cfg.ProcRoot, "loadavg")); err != nil {
		return
	}
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.LogOnce()
		}
	}
}

// LogOnce takes one sample and logs it.
func (s *Sampler) LogOnce() {
	health, volumes := s.Sample()
	s.cfg.Log(HealthMessage, health...)
	for _, v := range volumes {
		s.cfg.Log(VolumeMessage, v...)
	}
}

// Sample returns the attributes of the health line and of each volume line.
// A reading that fails is left out rather than logged as zero, so a panel
// never averages a missing PSI file in as "no pressure".
func (s *Sampler) Sample() (health []any, volumes [][]any) {
	root := s.cfg.ProcRoot
	if l1, l5, l15, ok := readLoadavg(filepath.Join(root, "loadavg")); ok {
		health = append(health, "load1", round3(l1), "load5", round3(l5), "load15", round3(l15))
		if s.cfg.CPUs > 0 {
			health = append(health, "cpus", round3(s.cfg.CPUs),
				"load1_per_cpu", round3(l1/s.cfg.CPUs), "load5_per_cpu", round3(l5/s.cfg.CPUs))
		}
	}
	for _, res := range []string{"cpu", "memory", "io"} {
		some, full, ok := readPressure(filepath.Join(root, "pressure", res))
		if !ok {
			continue
		}
		health = append(health, "psi_"+res+"_some_avg60", some)
		// cpu's "full" line is always 0 outside a cgroup; logged where present.
		if full >= 0 {
			health = append(health, "psi_"+res+"_full_avg60", full)
		}
	}

	if s.cfg.Disk != nil {
		if rep, ok := s.cfg.Disk(); ok {
			minRatio, minLabel := math.Inf(1), ""
			for _, v := range rep.GetVolumes() {
				if v.GetTotalBytes() == 0 {
					continue
				}
				ratio := float64(v.GetFreeBytes()) / float64(v.GetTotalBytes())
				volumes = append(volumes, []any{
					"volume", v.GetLabel(),
					"free_ratio", round3(ratio),
					"free_bytes", v.GetFreeBytes(),
					"total_bytes", v.GetTotalBytes(),
				})
				if ratio < minRatio {
					minRatio, minLabel = ratio, v.GetLabel()
				}
			}
			if minLabel != "" {
				health = append(health, "disk_min_free_ratio", round3(minRatio), "disk_min_free_volume", minLabel)
			}
		}
	}

	if s.cfg.Slots != nil {
		if sl, ok := s.cfg.Slots(); ok {
			health = append(health,
				"heavy_slots", sl.Capacity,
				"heavy_held", sl.Held,
				"heavy_waiting", sl.Waiting,
				"heavy_oldest_wait_s", int64(sl.OldestWait.Seconds()))
		}
	}

	if n, ok := countForgeInstalls(root); ok {
		health = append(health, "forge_installs", n)
	}
	return health, volumes
}

// readLoadavg parses /proc/loadavg: "0.52 0.58 0.59 1/1234 5678".
func readLoadavg(path string) (l1, l5, l15 float64, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, 0, false
	}
	f := strings.Fields(string(data))
	if len(f) < 3 {
		return 0, 0, 0, false
	}
	var vals [3]float64
	for i := range vals {
		v, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return 0, 0, 0, false
		}
		vals[i] = v
	}
	return vals[0], vals[1], vals[2], true
}

// readPressure parses a PSI file's avg60 for "some" and "full":
//
//	some avg10=0.00 avg60=0.00 avg300=0.00 total=0
//	full avg10=0.00 avg60=0.00 avg300=0.00 total=0
//
// full is -1 when the file has no "full" line (older kernels' cpu).
func readPressure(path string) (some, full float64, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, false
	}
	full = -1
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		for _, kv := range f[1:] {
			v, found := strings.CutPrefix(kv, "avg60=")
			if !found {
				continue
			}
			x, err := strconv.ParseFloat(v, 64)
			if err != nil {
				break
			}
			switch f[0] {
			case "some":
				some, ok = x, true
			case "full":
				full = x
			}
		}
	}
	return some, full, ok
}

// forgeModule is what a `go install` of forge names: the module path, any of
// its commands, at a version.
const forgeModule = "github.com/reliant-labs/forge"

// countForgeInstalls counts running `go install github.com/reliant-labs/forge/...@<v>`
// processes: each one compiles forge from source, which on a busy machine is
// minutes of every core, and several at once (one per agent resolving the same
// pin) was one of the loads this sample exists to see.
func countForgeInstalls(root string) (int, bool) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, false
	}
	n := 0
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, e.Name(), "cmdline"))
		if err != nil || len(data) == 0 {
			continue
		}
		if isForgeInstall(bytes.Split(bytes.TrimRight(data, "\x00"), []byte{0})) {
			n++
		}
	}
	return n, true
}

func isForgeInstall(argv [][]byte) bool {
	if len(argv) < 3 || filepath.Base(string(argv[0])) != "go" {
		return false
	}
	install := false
	for _, a := range argv[1:] {
		s := string(a)
		switch {
		case s == "install":
			install = true
		case install && strings.HasPrefix(s, forgeModule) && strings.Contains(s, "@"):
			return true
		}
	}
	return false
}

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }
