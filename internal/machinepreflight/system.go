// Copyright (c) 2026 Reliant Labs
package machinepreflight

import (
	"os"
	"os/exec"
)

// SystemEnv is the daemon process's own machine: its home, /tmp, uid and
// PATH. statFS, diskRefusal and load5 are the daemon's readers (diskwatch,
// the disk guard, machinehealth's /proc/loadavg); cpus is the CPU count load
// is divided by. A nil reader skips its check.
func SystemEnv(statFS func(string) (Disk, error), diskRefusal func() string, load5 func() (float64, bool), cpus float64) Env {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	env := Env{
		Home:        home,
		TmpDir:      TmpDir,
		StatFS:      statFS,
		DiskRefusal: diskRefusal,
		Owner:       FileOwner,
		UID:         ProcessUID,
		LookPath:    exec.LookPath,
		Load5:       load5,
		CPUs:        cpus,
	}
	if hasTmpCheck {
		env.Stat = os.Stat
		env.CreateTemp = createTemp
	} else {
		// Only the home's ownership reads Stat; Windows reports no owner.
		env.Stat = nil
		env.TmpDir = os.TempDir()
	}
	return env
}

// createTemp creates a file in dir and removes it.
func createTemp(dir string) error {
	f, err := os.CreateTemp(dir, ".reliant-preflight-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}
