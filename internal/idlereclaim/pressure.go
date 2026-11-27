// Copyright (c) 2026 Reliant Labs
package idlereclaim

import (
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/diskwatch"
)

// Disk pressure. On a schedule alone the idle reclaim ran every 3h with 48h
// and 72h floors, so a disk filling at ~40 GB an hour (a shared build
// machine under agent load) reached full before a pass found anything due.
// Under pressure a pass runs within minutes and then every
// PressurePassInterval, and the floors for what is regenerable drop to
// PressureStripIdle: a node_modules or a build output idle a day, with no
// process using its checkout, is put back by one install. Removing a scratch
// directory whole is not hurried: the system prompt promises every chat 7
// days, and what that removes is not regenerable.

const (
	// TriggerPressure is the trigger of a pass run because a volume holding
	// the scratch or worktree root is under pressure; it applies the
	// pressure floors.
	TriggerPressure = "pressure"
	// TriggerScheduled is the trigger of a routine pass.
	TriggerScheduled = "scheduled"

	// PressureStripIdle is the strip floor of a pressure pass, for scratch
	// output and worktree node_modules alike: a day idle.
	PressureStripIdle = 24 * time.Hour
	// PressurePassInterval is how soon a pass repeats while the pressure
	// lasts.
	PressurePassInterval = 30 * time.Minute

	// pressureCheckEvery is how often Run reads the pressure: one statfs per
	// root.
	pressureCheckEvery = time.Minute
	// pressureStartDelay replaces firstPassDelay when the disk is already
	// under pressure at startup.
	pressureStartDelay = 2 * time.Minute
)

// pressureFloors is c with the floors of a pressure pass: the strip and
// node_modules floors at most PressureStripIdle; the whole-directory floor
// as it is.
func (c Config) pressureFloors() Config {
	c.ScratchStripIdle = min(c.ScratchStripIdle, PressureStripIdle)
	c.WorktreeNodeModulesIdle = min(c.WorktreeNodeModulesIdle, PressureStripIdle)
	return c
}

// nextPass says whether a pass is due at now, and its trigger. started is
// when Run started; last is when the last pass started (zero: none yet).
func nextPass(pressure bool, started, last, now time.Time) (trigger string, due bool) {
	if pressure {
		if last.IsZero() && now.Sub(started) >= pressureStartDelay || !last.IsZero() && now.Sub(last) >= PressurePassInterval {
			return TriggerPressure, true
		}
	}
	if last.IsZero() {
		return TriggerScheduled, now.Sub(started) >= firstPassDelay
	}
	return TriggerScheduled, now.Sub(last) >= PassInterval
}

// VolumePressure returns a Deps.Pressure that reads the volumes holding
// paths with statfs and keeps diskwatch's hysteresis bit for each
// (diskwatch.NextPressure: the same rule the daemon's disk report and storage
// reaper use). A path that cannot be read is not under pressure.
func VolumePressure(paths ...string) func() bool {
	var mu sync.Mutex
	state := map[string]bool{}
	return func() bool {
		mu.Lock()
		defer mu.Unlock()
		pressed := false
		for _, p := range paths {
			if p == "" {
				continue
			}
			u, ok := statVolume(p)
			if !ok {
				continue
			}
			state[p] = diskwatch.NextPressure(state[p], u)
			pressed = pressed || state[p]
		}
		return pressed
	}
}
