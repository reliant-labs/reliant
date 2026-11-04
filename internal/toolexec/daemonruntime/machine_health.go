// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"os"
	"time"

	"github.com/reliant-labs/forge/pkg/hostslot"
	"github.com/reliant-labs/reliant/internal/diskwatch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/machinehealth"
)

// newMachineHealthSampler wires the minute sample to this daemon's disk watcher
// and to the heavy-slot pool its heavy commands queue on (the pool forge's
// lanes share, resolved from the same FORGE_* variables heavyjob.New reads).
func newMachineHealthSampler(disk *diskwatch.Watcher) *machinehealth.Sampler {
	env := os.Environ() //nolint:forbidigo // forge's FORGE_* slot settings, passed through to forge/pkg/hostslot; not this daemon's config
	return machinehealth.New(machinehealth.Config{
		CPUs:  hostslot.DetectMachine().CPUs,
		Disk:  disk.Report,
		Slots: func() (machinehealth.Slots, bool) { return heavySlots(env, time.Now()) },
		Log:   logging.Info,
	})
}

// heavySlots reads the heavy class's holders and queue. Status probes each
// slot's lock for an instant and takes none.
func heavySlots(env []string, now time.Time) (machinehealth.Slots, bool) {
	rep, err := hostslot.Status(hostslot.Heavy, hostslot.Options{Env: env})
	if err != nil {
		return machinehealth.Slots{}, false
	}
	s := machinehealth.Slots{Held: len(rep.Holders), Waiting: len(rep.Waiting)}
	if !rep.Capacity.Unlimited {
		s.Capacity = rep.Capacity.Slots
	}
	// Waiting is oldest first.
	if len(rep.Waiting) > 0 && !rep.Waiting[0].Since.IsZero() {
		s.OldestWait = now.Sub(rep.Waiting[0].Since)
	}
	return s, true
}
