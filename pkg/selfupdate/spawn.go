package selfupdate

import (
	"fmt"
	"os"
	"os/exec"
)

// respawn starts path as a new process with argv, env, this process's working
// directory and its stdio, then exits this process with status 0: the swap
// on a platform with no exec(2) (Windows). start and exit are seams, so the
// logic is tested on every platform. It returns only if start failed, with
// this process still running.
func respawn(path string, argv, env []string, start func(*exec.Cmd) error, exit func(code int)) error {
	cmd := &exec.Cmd{Path: path, Args: argv, Env: env}
	if wd, err := os.Getwd(); err == nil {
		cmd.Dir = wd
	}
	// A process with no console has nil standard files; a nil *os.File in
	// the interface would not read as "none" to exec.
	if os.Stdin != nil {
		cmd.Stdin = os.Stdin
	}
	if os.Stdout != nil {
		cmd.Stdout = os.Stdout
	}
	if os.Stderr != nil {
		cmd.Stderr = os.Stderr
	}
	if err := start(cmd); err != nil {
		return fmt.Errorf("start %s: %w", path, err)
	}
	exit(0)
	return nil
}
