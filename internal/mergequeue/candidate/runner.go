// Copyright (c) 2025 Reliant Labs
package candidate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/reliant-labs/reliant/internal/credsandbox"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

// Runner runs a repository's regen command in a checkout. A command that ran
// and failed is a *[CommandError]; any other error means it could not be run
// at all, and fails the build rather than ejecting the entry.
type Runner interface {
	Run(ctx context.Context, dir, command string) (output string, err error)
}

// CommandError is a regen command that ran and exited non-zero (or was
// refused by the sandbox, or timed out).
type CommandError struct {
	ExitCode int
	Output   string
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("candidate: regen exited %d", e.ExitCode)
}

// DefaultRegenTimeout bounds one regen run when [SandboxRunner.Timeout] is
// zero.
const DefaultRegenTimeout = 30 * time.Minute

// SandboxRunner runs the command as `bash -c <command>` with
// `credentials: none`, exactly as a credentials-none run node's command runs on
// a daemon: the credential-free environment (daemonpolicy.ChildEnv) inside the
// credential sandbox (credsandbox.Wrap), where the daemon's credential files are
// absent. It writes only its own checkout. The program must have called
// credsandbox.Dispatch (the reliant binary does), or the sandbox is not in
// force and only the environment is scrubbed, as everywhere else.
type SandboxRunner struct {
	// Env is added to the command's credential-free environment.
	Env map[string]string
	// Timeout bounds the run; zero is DefaultRegenTimeout.
	Timeout time.Duration
}

// Run implements Runner.
func (r SandboxRunner) Run(ctx context.Context, dir, command string) (string, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultRegenTimeout
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = dir
	cmd.Env = daemonpolicy.ChildEnv(ctx, r.Env, daemonpolicy.CredentialsNone)
	if refusal := credsandbox.Wrap(cmd, daemonpolicy.CredentialsNone, credsandbox.Writable{}); refusal != "" {
		return refusal, &CommandError{ExitCode: credsandbox.RefusedExitCode, Output: refusal}
	}
	defer credsandbox.Release(cmd)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = 10 * time.Second
	err := cmd.Run()
	switch {
	case err == nil:
		return out.String(), nil
	case parent.Err() != nil:
		// The build was cancelled: not the command's failure.
		return out.String(), fmt.Errorf("candidate: run regen: %w", parent.Err())
	case ctx.Err() != nil:
		return out.String(), &CommandError{ExitCode: -1, Output: fmt.Sprintf("%s\nregen did not finish within %s", out.String(), timeout)}
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.String(), &CommandError{ExitCode: ee.ExitCode(), Output: out.String()}
	}
	return out.String(), fmt.Errorf("candidate: run regen: %w", err)
}
