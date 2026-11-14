// Copyright (c) 2025 Reliant Labs
//go:build windows

package terminal

import "fmt"

// startShell starts session's shell on its ConPTY. Windows has no credential
// sandbox: a credentials-none shell gets the credentials-none environment
// only (env, built by the caller).
func startShell(session *Session, shell string, args []string, dir string, env []string, _ SessionOptions) error {
	cmd := session.PTY.Command(shell, args...)
	cmd.Dir, cmd.Env = dir, env
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start command: %w", err)
	}
	session.CMD = cmd
	return nil
}
