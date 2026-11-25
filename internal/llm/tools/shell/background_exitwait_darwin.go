// Copyright (c) 2026 Reliant Labs
//go:build darwin

package shell

import (
	"time"

	"github.com/reliant-labs/reliant/internal/osutil"
)

// fileOutputSupported: a child can write its output to files and be waited
// for without being reaped (waitExited), so it can outlive this image.
const fileOutputSupported = true

// exitPollInterval is how often waitExited looks at the process table. macOS
// has no waitid(WNOWAIT) in x/sys; a zombie is what an exited, unreaped child
// is, and kinfo_proc shows it.
var exitPollInterval = 100 * time.Millisecond

// waitExited blocks until pid, a child of this process, has exited, WITHOUT
// reaping it: its exit status stays for whoever reaps it — this image, or the
// next one after a self-update's exec.
func waitExited(pid int) error {
	for {
		id, ok := osutil.IdentifyProcess(pid)
		if !ok || id.Exited {
			return nil
		}
		time.Sleep(exitPollInterval)
	}
}
