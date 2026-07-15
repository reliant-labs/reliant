// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"errors"
	"fmt"
	"os/exec"

	"github.com/reliant-labs/reliant/internal/cgroupmem"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/osutil"
)

// memReader reads the container cgroup's memory accounting. On hosts without
// cgroup v2 memory files (macOS, uncontained daemons) every operation
// degrades to a no-op — snapshots are invalid and no error is ever
// classified as OOM.
var memReader = cgroupmem.NewReader(cgroupmem.DefaultRoot)

// steerOOMKiller raises the started command's oom_score_adj so that when the
// pod cgroup runs out of memory the kernel kills the workload, not the
// daemon (see osutil.ChildOOMScoreAdj). Call immediately after Start; the
// child's descendants inherit the score on fork. Best-effort: a failure is
// logged and never fails the spawn.
func steerOOMKiller(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := osutil.AdjustChildOOMScore(cmd.Process.Pid); err != nil {
		logging.Debug(logPrefix+" Failed to adjust child oom_score_adj",
			"pid", cmd.Process.Pid, "error", err)
	}
}

// wrapChildOOMKill decorates a child-process failure with the structured
// out-of-memory explanation when the child's death is attributable to the
// kernel OOM killer (SIGKILL-shaped exit + oom_kill counter advanced since
// the pre-exec snapshot). Otherwise it returns err unchanged. Used by the
// fs/daemon command handlers so their RPC error paths surface an actionable
// message instead of a bare "signal: killed".
func wrapChildOOMKill(err error, snap cgroupmem.OOMSnapshot) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return err
	}
	if oom, msg := memReader.CheckOOMKill(exitErr.ExitCode(), snap); oom {
		return fmt.Errorf("%s (%w)", msg, err)
	}
	return err
}
