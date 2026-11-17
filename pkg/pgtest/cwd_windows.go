//go:build windows

package pgtest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// releaseCallerCwd restarts the just-booted server with pg_ctl run from dir,
// so nothing the server leaves running holds the CALLER's working directory.
//
// On Windows pg_ctl cannot exec the postmaster; it launches it under
// `CMD /D /C "postgres ..."`, and that cmd.exe stays alive as the
// postmaster's parent for the server's whole life with pg_ctl's working
// directory — which embedded-postgres never sets, so it is whatever this
// process's cwd was at boot. A process's working directory cannot be removed
// on Windows. The shared server outlives the call that booted it (see
// pool.go), so a `go test` that chdir'd into t.TempDir() before its first
// database lost the TempDir cleanup, and a `forge` run pinned the project
// directory it was started from until the last pool user detached.
//
// The library offers no working-directory option, and chdir'ing this process
// around the boot would move the cwd under every other goroutine. So the
// server is stopped and started again by pg_ctl directly, from dir (forge's
// own runtime directory, removed at teardown once the server is gone).
// Teardown already goes through pg_ctl by data directory, never the
// library's handle, so nothing downstream notices the restart.
func releaseCallerCwd(dir string, port uint32, params map[string]string) error {
	pgCtl := filepath.Join(dir, "bin", pgCtlBinary)
	data := filepath.Join(dir, "data")

	stop := exec.Command(pgCtl, "stop", "-w", "-D", data, "-m", "fast")
	stop.Dir = dir
	if out, err := stop.CombinedOutput(); err != nil {
		return fmt.Errorf("pg_ctl stop: %w: %s", err, strings.TrimSpace(string(out)))
	}

	logPath := filepath.Join(dir, "postgres.log")
	start := exec.Command(pgCtl, "start", "-w", "-D", data, "-l", logPath, "-o", pgCtlOptions(port, params))
	start.Dir = dir
	// No pipes: the cmd.exe wrapper and the postmaster inherit pg_ctl's stdio
	// and would hold a pipe open — and Run with it — for the server's life.
	// pg_ctl's own chatter is discarded; the server's output goes to -l.
	if err := start.Run(); err != nil {
		log, _ := os.ReadFile(logPath)
		return fmt.Errorf("pg_ctl start: %w: %s", err, strings.TrimSpace(string(log)))
	}
	return nil
}
