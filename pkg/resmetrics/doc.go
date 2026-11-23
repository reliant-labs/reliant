// Package resmetrics samples the process's own CPU, memory, file descriptors,
// threads and IO, and its host's CPU, load, memory, swap, filesystems and
// cgroup v2 pressure, as a typed Snapshot and as OpenTelemetry instruments.
//
// observe.Setup starts it in every forge binary and registers the instruments
// on the meter provider, so the metrics reach the Prometheus /metrics reader
// and, when export is on, OTLP; FORGE_OBSERVE_RESOURCE_METRICS (or
// observe.Config.ResourceMetrics) turns either group off. An app that projects
// the numbers into its own protocol — a heartbeat, a status page — starts a
// Sampler itself and reads Latest or Subscribe; the Snapshot needs no OTel.
//
// # Shape
//
//	s, err := resmetrics.Start(ctx, resmetrics.Options{Groups: resmetrics.GroupAll})
//	snap := s.Latest()                       // cached; resampled when older than the interval
//	ch, stop := s.Subscribe(30 * time.Second) // a ticker, only while subscribed
//	reg, err := resmetrics.Register(meter, s)  // observable instruments over Latest
//
// A Sampler runs nothing in the background: Latest resamples when the cached
// sample is older than Options.Interval (DefaultInterval, at least
// MinInterval), which in practice means once per metrics collection.
//
// # Readers
//
// On linux the readers parse /proc and the process's cgroup v2 by hand; each
// file is its own reader, and one that fails (a missing file, a sandbox that
// denies /proc/self/io, a host on cgroup v1) leaves its metrics out of that
// sample and records why in Snapshot.Errors — it never fails the sample or
// Start.
//
// darwin and windows have no /proc; their readers ask the kernel directly,
// through golang.org/x/sys and, on darwin, a few libSystem calls x/sys does
// not wrap (proc_pidinfo, host_statistics), made the way x/sys makes its own:
// no cgo, so a CGO_ENABLED=0 cross-compile carries them. They fail one by one
// in the same way. A platform reports a metric only where its meaning holds,
// and leaves out what it cannot measure rather than reporting zero:
//
//   - darwin: no process disk IO (getrusage keeps block counts, not bytes),
//     no system process count, and no iowait or steal CPU mode;
//   - windows: no page faults (it does not split major from minor), context
//     switches, file descriptors or disk IO for the process — it reports its
//     open handles instead (forge.process.handle.count) — and no load
//     average, swap, system-wide file or process count, or iowait and steal.
//
// The ProcessStats and HostStats docs list the fields per platform. On any
// other platform a sample carries only what the Go runtime knows (start time,
// goroutines, the runtime's mapped memory, the logical CPU count).
//
// # Filesystems
//
// The host group reports up to four filesystems, named by role, never by
// path: by default the working directory's (cwd) and the temporary
// directory's (tmp). Options.Volumes replaces them; an app that writes
// somewhere else appends a VolumeData entry to DefaultVolumes.
//
// # Scope
//
// The host group reports the whole machine (ScopeHost) or, inside a
// Kubernetes pod (ScopeCgroup), only the container's cgroup: node-wide CPU,
// load, memory, swap and system-wide counts are left out there, since every
// pod on a node would otherwise report the same node under its own name.
//
// # Privacy and cardinality
//
// Only aggregates of the process itself and of its host are reported. No
// datapoint carries a command line, an executable, a user, a pid, a host name
// or a mount path; CPU is the aggregate line, not per CPU; filesystems are at
// most four, one per device, named by role (cwd, tmp). The metric
// declarations, with every attribute's closed value set, are in
// pkg/observe/metrics (resourceSpecs).
package resmetrics
