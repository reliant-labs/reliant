//go:build !windows && !darwin && !linux

package cli

// readProcStamp: no start-time source is wired on this platform, so no pid
// can be re-verified at kill time. killProcessTree then never signals a
// process group (signalTreeTargets).
func readProcStamp(int) (procStamp, bool) { return procStamp{}, false }

func startMark() (int64, bool) { return 0, false }
