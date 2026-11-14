// Copyright (c) 2026 Reliant Labs
//go:build unix

package forkwatch

import (
	"context"
	"errors"
	"os/exec"
	"syscall"

	"github.com/reliant-labs/reliant/internal/proctree"
)

// refused reports whether a start failed because the kernel refused the new
// task: EAGAIN from fork/clone at RLIMIT_NPROC, a cgroup's pids.max or
// threads-max. os/exec wraps it in an *os.PathError ("fork/exec ...").
func refused(err error) bool { return errors.Is(err, syscall.EAGAIN) }

// defaultProbe starts `true` as an agent's command starts, through proctree's
// default limiter (looked up per probe: the daemon installs it after building
// the watchdog). Nil when there is no `true` on PATH.
func defaultProbe() func(ctx context.Context) error {
	path, err := exec.LookPath("true")
	if err != nil {
		return nil
	}
	return func(ctx context.Context) error { return start(ctx, path) }
}

// start runs path as a bounded tree and reports whether it started; it waits
// for it either way, so nothing is left behind.
func start(ctx context.Context, path string) error {
	cmd := exec.CommandContext(ctx, path)
	tree := proctree.Default().Prepare(cmd, proctree.Owner{Command: "forkwatch probe: " + path})
	err := cmd.Start()
	pid := 0
	if err == nil {
		pid = cmd.Process.Pid
	}
	tree.Started(pid) // releases the tree itself when pid is 0
	if err != nil {
		return err
	}
	_ = cmd.Wait() // it started; how it ended says nothing about the table
	tree.Release()
	return nil
}
