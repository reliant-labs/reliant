package resmetrics

import (
	"context"
	"errors"
	"fmt"
	"math"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/reliant-labs/forge/pkg/observe/metrics"
)

// Register declares the resource instruments of s's groups on meter and one
// callback that observes them from s.Latest(), so every reader of the meter
// provider (a Prometheus scrape, an OTLP export) sees the same sample, and a
// collection takes a new sample only when the cached one is older than the
// interval. A metric whose reader failed is left out of that collection.
//
// Names, units, descriptions and instrument kinds come from the declarations
// in pkg/observe/metrics (BuiltinSpec), the one list of forge's own metrics.
func Register(meter metric.Meter, s *Sampler) (metric.Registration, error) {
	r := &registrar{meter: meter}
	var observe []func(Snapshot, metric.Observer)
	if s.Groups()&GroupProcess != 0 {
		observe = append(observe, r.process())
	}
	if s.Groups()&GroupHost != 0 {
		observe = append(observe, r.host(), r.cgroup())
	}
	if err := errors.Join(r.errs...); err != nil {
		return nil, fmt.Errorf("resmetrics: %w", err)
	}
	return meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		snap := s.Latest()
		for _, f := range observe {
			f(snap, o)
		}
		return nil
	}, r.all...)
}

type registrar struct {
	meter metric.Meter
	all   []metric.Observable
	errs  []error
}

// float creates the float64 instrument the declaration of name calls for.
func (r *registrar) float(name string) metric.Float64Observable {
	spec := metrics.BuiltinSpec(name)
	unit, desc := metric.WithUnit(spec.Unit), metric.WithDescription(spec.Description)
	var inst metric.Float64Observable
	var err error
	switch spec.Type {
	case metrics.Counter:
		inst, err = r.meter.Float64ObservableCounter(name, unit, desc)
	case metrics.UpDownCounter:
		inst, err = r.meter.Float64ObservableUpDownCounter(name, unit, desc)
	default:
		inst, err = r.meter.Float64ObservableGauge(name, unit, desc)
	}
	r.errs = append(r.errs, err)
	r.all = append(r.all, inst)
	return inst
}

// int creates the int64 instrument the declaration of name calls for.
func (r *registrar) int(name string) metric.Int64Observable {
	spec := metrics.BuiltinSpec(name)
	unit, desc := metric.WithUnit(spec.Unit), metric.WithDescription(spec.Description)
	var inst metric.Int64Observable
	var err error
	switch spec.Type {
	case metrics.Counter:
		inst, err = r.meter.Int64ObservableCounter(name, unit, desc)
	case metrics.UpDownCounter:
		inst, err = r.meter.Int64ObservableUpDownCounter(name, unit, desc)
	default:
		inst, err = r.meter.Int64ObservableGauge(name, unit, desc)
	}
	r.errs = append(r.errs, err)
	r.all = append(r.all, inst)
	return inst
}

func attrs(kv ...string) metric.ObserveOption {
	a := make([]attribute.KeyValue, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		a = append(a, attribute.String(kv[i], kv[i+1]))
	}
	return metric.WithAttributeSet(attribute.NewSet(a...))
}

// i64 converts a kernel counter, which never reaches 2^63, without wrapping.
func i64(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

func (r *registrar) process() func(Snapshot, metric.Observer) {
	cpuTime := r.float("process.cpu.time")
	cpuUtil := r.float("process.cpu.utilization")
	rss := r.int("process.memory.usage")
	virt := r.int("process.memory.virtual")
	gap := r.int("forge.process.memory.go_gap")
	fds := r.int("process.unix.file_descriptor.count")
	fdLimit := r.int("forge.process.file_descriptor.limit")
	fdUtil := r.float("forge.process.file_descriptor.utilization")
	threads := r.int("process.thread.count")
	switches := r.int("process.context_switches")
	faults := r.int("process.paging.faults")
	diskIO := r.int("process.disk.io")
	uptime := r.float("process.uptime")

	user, system := attrs("cpu.mode", "user"), attrs("cpu.mode", "system")
	voluntary, involuntary := attrs("process.context_switch_type", "voluntary"), attrs("process.context_switch_type", "involuntary")
	major, minor := attrs("system.paging.fault.type", "major"), attrs("system.paging.fault.type", "minor")
	read, write := attrs("disk.io.direction", "read"), attrs("disk.io.direction", "write")

	return func(snap Snapshot, o metric.Observer) {
		p := snap.Process
		if p == nil {
			return
		}
		o.ObserveFloat64(uptime, p.Uptime(snap.At).Seconds())
		if c := p.CPU; c != nil {
			o.ObserveFloat64(cpuTime, c.UserSeconds, user)
			o.ObserveFloat64(cpuTime, c.SystemSeconds, system)
			if c.Utilization != nil {
				o.ObserveFloat64(cpuUtil, *c.Utilization)
			}
		}
		if m := p.Memory; m != nil {
			o.ObserveInt64(rss, i64(m.RSS))
			o.ObserveInt64(virt, i64(m.Virtual))
			o.ObserveInt64(gap, m.GoGap)
		}
		if p.FDCount != nil {
			o.ObserveInt64(fds, *p.FDCount)
		}
		if p.FDLimit != nil {
			o.ObserveInt64(fdLimit, *p.FDLimit)
			if p.FDCount != nil && *p.FDLimit > 0 {
				o.ObserveFloat64(fdUtil, float64(*p.FDCount)/float64(*p.FDLimit))
			}
		}
		if p.Threads != nil {
			o.ObserveInt64(threads, *p.Threads)
		}
		if cs := p.ContextSwitches; cs != nil {
			o.ObserveInt64(switches, i64(cs.Voluntary), voluntary)
			o.ObserveInt64(switches, i64(cs.Involuntary), involuntary)
		}
		if f := p.Faults; f != nil {
			o.ObserveInt64(faults, i64(f.Major), major)
			o.ObserveInt64(faults, i64(f.Minor), minor)
		}
		if io := p.DiskIO; io != nil {
			o.ObserveInt64(diskIO, i64(io.ReadBytes), read)
			o.ObserveInt64(diskIO, i64(io.WriteBytes), write)
		}
	}
}

func (r *registrar) host() func(Snapshot, metric.Observer) {
	scope := r.int("forge.resource.scope")
	cpuTime := r.float("system.cpu.time")
	cpuUtil := r.float("system.cpu.utilization")
	logical := r.int("system.cpu.logical.count")
	load1 := r.float("system.cpu.load_average.1m")
	load5 := r.float("system.cpu.load_average.5m")
	load15 := r.float("system.cpu.load_average.15m")
	memUsage := r.int("system.memory.usage")
	memLimit := r.int("system.memory.limit")
	memAvail := r.int("system.linux.memory.available")
	paging := r.int("system.paging.usage")
	fsUsage := r.int("system.filesystem.usage")
	fsUtil := r.float("system.filesystem.utilization")
	sysFDs := r.int("forge.system.file_descriptor.count")
	sysFDLimit := r.int("forge.system.file_descriptor.limit")
	procs := r.int("forge.system.process.count")
	procLimit := r.int("forge.system.process.limit")

	mode := func(m string) metric.ObserveOption { return attrs("cpu.mode", m) }
	observeModes := func(o metric.Observer, inst metric.Float64Observable, m CPUModes) {
		o.ObserveFloat64(inst, m.User, mode("user"))
		o.ObserveFloat64(inst, m.System, mode("system"))
		o.ObserveFloat64(inst, m.Idle, mode("idle"))
		o.ObserveFloat64(inst, m.IOWait, mode("iowait"))
		o.ObserveFloat64(inst, m.Steal, mode("steal"))
	}

	return func(snap Snapshot, o metric.Observer) {
		h := snap.Host
		if h == nil {
			return
		}
		o.ObserveInt64(scope, 1, attrs("scope", string(h.Scope)))
		o.ObserveInt64(logical, int64(h.LogicalCPUs))
		if c := h.CPU; c != nil {
			observeModes(o, cpuTime, c.Seconds)
			if c.Utilization != nil {
				observeModes(o, cpuUtil, *c.Utilization)
			}
		}
		if l := h.Load; l != nil {
			o.ObserveFloat64(load1, l.One)
			o.ObserveFloat64(load5, l.Five)
			o.ObserveFloat64(load15, l.Fifteen)
		}
		if m := h.Memory; m != nil {
			o.ObserveInt64(memUsage, i64(m.Used), attrs("system.memory.state", "used"))
			o.ObserveInt64(memUsage, i64(m.Free), attrs("system.memory.state", "free"))
			o.ObserveInt64(memUsage, i64(m.Cached), attrs("system.memory.state", "cached"))
			o.ObserveInt64(memAvail, i64(m.Available))
		}
		if h.MemoryLimit != nil {
			o.ObserveInt64(memLimit, i64(*h.MemoryLimit))
		}
		if sw := h.Swap; sw != nil {
			used := uint64(0)
			if sw.Total > sw.Free {
				used = sw.Total - sw.Free
			}
			o.ObserveInt64(paging, i64(used), attrs("system.paging.state", "used"))
			o.ObserveInt64(paging, i64(sw.Free), attrs("system.paging.state", "free"))
		}
		for _, fs := range h.Filesystems {
			o.ObserveInt64(fsUsage, i64(fs.UsedBytes), attrs("system.filesystem.state", "used", "forge.filesystem.role", fs.Role))
			o.ObserveInt64(fsUsage, i64(fs.FreeBytes), attrs("system.filesystem.state", "free", "forge.filesystem.role", fs.Role))
			if total := fs.UsedBytes + fs.FreeBytes; total > 0 {
				o.ObserveFloat64(fsUtil, float64(fs.UsedBytes)/float64(total), attrs("forge.filesystem.role", fs.Role))
			}
		}
		if fd := h.FileDescriptors; fd != nil {
			o.ObserveInt64(sysFDs, i64(fd.Count))
			o.ObserveInt64(sysFDLimit, i64(fd.Limit))
		}
		if p := h.Processes; p != nil {
			o.ObserveInt64(procs, i64(p.Count))
			o.ObserveInt64(procLimit, i64(p.Limit))
		}
	}
}

func (r *registrar) cgroup() func(Snapshot, metric.Observer) {
	workingSet := r.int("forge.cgroup.memory.working_set")
	memLimit := r.int("forge.cgroup.memory.limit")
	oomKills := r.int("forge.cgroup.memory.oom_kills")
	stall := r.float("forge.cgroup.pressure.stall_time")
	avg10 := r.float("forge.cgroup.pressure.avg10")
	throttledPeriods := r.int("forge.cgroup.cpu.throttled_periods")
	throttledTime := r.float("forge.cgroup.cpu.throttled_time")
	pids := r.int("forge.cgroup.pids.count")
	pidsLimit := r.int("forge.cgroup.pids.limit")

	observePSI := func(o metric.Observer, resource string, psi *PSI) {
		if psi == nil {
			return
		}
		lines := []struct {
			kind string
			l    *PSILine
		}{{"some", &psi.Some}, {"full", psi.Full}}
		for _, line := range lines {
			if line.l == nil {
				continue
			}
			a := attrs("resource", resource, "kind", line.kind)
			o.ObserveFloat64(stall, line.l.TotalSeconds, a)
			o.ObserveFloat64(avg10, line.l.Avg10, a)
		}
	}

	return func(snap Snapshot, o metric.Observer) {
		c := snap.Cgroup
		if c == nil {
			return
		}
		if ws, ok := c.WorkingSet(); ok {
			o.ObserveInt64(workingSet, i64(ws))
		}
		if c.MemoryMax != nil {
			o.ObserveInt64(memLimit, i64(*c.MemoryMax))
		}
		if c.OOMKills != nil {
			o.ObserveInt64(oomKills, i64(*c.OOMKills))
		}
		observePSI(o, "cpu", c.Pressure.CPU)
		observePSI(o, "memory", c.Pressure.Memory)
		observePSI(o, "io", c.Pressure.IO)
		if cpu := c.CPU; cpu != nil {
			o.ObserveInt64(throttledPeriods, i64(cpu.ThrottledPeriods))
			o.ObserveFloat64(throttledTime, cpu.ThrottledSeconds)
		}
		if c.PidsCurrent != nil {
			o.ObserveInt64(pids, i64(*c.PidsCurrent))
		}
		if c.PidsMax != nil {
			o.ObserveInt64(pidsLimit, i64(*c.PidsMax))
		}
	}
}
