// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/hostslot"
	"github.com/reliant-labs/forge/pkg/resmetrics"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/machinehealth"
)

// resourceSampleInterval is how old the daemon's resource sample may be when
// a heartbeat reads it before it is taken again. It is under the heartbeat
// interval, so every periodic heartbeat carries a fresh sample and the CPU
// rates span about one heartbeat, while a heartbeat sent early (a memory or
// disk change, a second stream during a drain) reuses the cached one.
const resourceSampleInterval = 10 * time.Second

// newResources starts the daemon's one resource sampler and projects it for
// the heartbeat. The sampler runs no goroutine: a heartbeat's read takes the
// sample. It reads the whole machine (ScopeHost) even in a pod, because
// machine_stats has always been the kernel's view of the machine (load,
// MemTotal, MemAvailable), not the pod's; the pod's memory is the workspace
// cgroup's, which cgroupmem reports. Nothing here is exported over OTLP: the
// heartbeat is the daemon's only telemetry path.
func newResources() *machinehealth.Resources {
	s, err := resmetrics.Start(context.Background(), resmetrics.Options{
		Groups:   resmetrics.GroupAll,
		Scope:    resmetrics.ScopeHost,
		Interval: resourceSampleInterval,
	})
	if err != nil {
		logging.Warn(logPrefix+" Resource stats will not be reported", "error", err)
		return nil
	}
	return machinehealth.NewResources(s.Latest, hostslot.DetectMachine().CPUs)
}
