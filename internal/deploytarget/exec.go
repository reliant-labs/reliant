package deploytarget

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/reliant-labs/forge/internal/envutil"
)

// commandRunner is the indirection point the providers use to run
// external commands (sh, docker, docker-compose, user-supplied CLIs).
// Tests swap in a fake that records calls and returns canned output;
// the production implementation shells out via os/exec.
//
// Run streams stdout/stderr to the parent process. RunWithEnv layers
// the supplied key=value map onto os.Environ() before exec (used to
// thread `env_file` contents into the child). Output captures combined
// output (used for the health-check checks that need to inspect the
// result); OutputWithEnv does both.
//
// OutputWithEnv exists because a compose subcommand that INSPECTS
// (`compose ps`) re-reads and re-interpolates the compose file exactly
// like one that ACTS (`compose up`) — so it needs the same overlay, or it
// resolves a different config than the one just deployed.
type commandRunner interface {
	Run(ctx context.Context, name string, args ...string) error
	RunWithEnv(ctx context.Context, env map[string]string, name string, args ...string) error
	// RunInDir runs name with cmd.Dir set to dir (empty inherits the cwd) and
	// an optional env overlay. No shell is involved, so dir needs no quoting
	// and no `sh` binary is required (Windows).
	RunInDir(ctx context.Context, dir string, env map[string]string, name string, args ...string) error
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	OutputWithEnv(ctx context.Context, env map[string]string, name string, args ...string) ([]byte, error)
}

// outputWithEnv runs an output-capturing command with an env overlay,
// falling through to the plain Output when there is nothing to overlay so
// the common (no-env) call keeps its existing shape in test recordings.
func outputWithEnv(ctx context.Context, runner commandRunner, env map[string]string, name string, args ...string) ([]byte, error) {
	if len(env) == 0 {
		return runner.Output(ctx, name, args...)
	}
	return runner.OutputWithEnv(ctx, env, name, args...)
}

// execRunner is the production commandRunner. Run pipes through to
// the parent stdout/stderr so users see deploy / docker progress in
// real time; Output captures combined stdout+stderr for inspection.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) error {
	return execRunner{}.RunWithEnv(ctx, nil, name, args...)
}

func (execRunner) RunWithEnv(ctx context.Context, env map[string]string, name string, args ...string) error {
	return execRunner{}.RunInDir(ctx, "", env, name, args...)
}

func (execRunner) RunInDir(ctx context.Context, dir string, env map[string]string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if len(env) > 0 {
		cmd.Env = envutil.MergeExtraWins(os.Environ(), env)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func (execRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return execRunner{}.OutputWithEnv(ctx, nil, name, args...)
}

func (execRunner) OutputWithEnv(ctx context.Context, env map[string]string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if len(env) > 0 {
		cmd.Env = envutil.MergeExtraWins(os.Environ(), env)
	}
	if err := cmd.Run(); err != nil {
		return buf.Bytes(), fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return buf.Bytes(), nil
}

// defaultRunner is the package-level commandRunner used by the
// providers when their Runner field is nil. Tests construct providers
// with their own Runner.
var defaultRunner commandRunner = execRunner{}
