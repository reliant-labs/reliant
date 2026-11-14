// Copyright (c) 2025 Reliant Labs
//go:build !windows

package terminal

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"

	"github.com/aymanbagabas/go-pty"

	"github.com/reliant-labs/reliant/internal/credsandbox"
)

// startShell starts session's shell on its PTY. A credentials-none shell is
// started as a plain exec.Cmd on the PTY's slave, so the credential sandbox
// (which needs the descriptors go-pty's Cmd cannot pass) can wrap it: the
// sandbox's init becomes the session leader with the PTY as its controlling
// terminal, and runs the shell in the terminal's foreground
// (credsandbox.WrapTerminal). Where the sandbox is not in force the shell
// still starts that way, with the credentials-none environment only.
func startShell(session *Session, shell string, args []string, dir string, env []string, opts SessionOptions) error {
	if !opts.CredentialsNone {
		cmd := session.PTY.Command(shell, args...)
		cmd.Dir, cmd.Env = dir, env
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to start command: %w", err)
		}
		session.CMD = cmd
		return nil
	}
	upty, ok := session.PTY.(pty.UnixPty)
	if !ok {
		return errors.New("failed to start command: the terminal is not a Unix PTY")
	}
	slave := upty.Slave()
	cmd := exec.Command(shell, args...)
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	// The PTY as controlling terminal (descriptor 0 in the child), in a
	// session of its own: what go-pty's Cmd sets.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if refusal := credsandbox.WrapTerminal(cmd); refusal != "" {
		return fmt.Errorf("credentials-none terminal not started: %s", refusal)
	}
	err := cmd.Start()
	// The sandbox's descriptors are the started process's now, or nobody's.
	credsandbox.Release(cmd)
	if err != nil {
		return fmt.Errorf("failed to start command: %w", err)
	}
	session.sandboxed = cmd
	return nil
}
