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
// Start. Elsewhere a sample carries only what the Go runtime knows (start
// time, goroutines, the runtime's mapped memory, the logical CPU count); the
// darwin and windows readers are a later phase.
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
