package testflakes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/reliant-labs/forge/internal/goexec"
)

// The seams every exec goes through, so tests never start go, git or gh.

// Runner runs argv in dir with env (the whole environment), its stdout and
// stderr to the writers, and returns its exit status. err is set only when
// the command could not start at all.
type Runner func(ctx context.Context, dir string, argv, env []string, stdout, stderr io.Writer) (int, error)

// Git runs `git <args>` in dir and returns its trimmed stdout.
type Git func(ctx context.Context, dir string, args ...string) (string, error)

// GH runs `gh <args>` and returns its stdout.
type GH func(ctx context.Context, args ...string) ([]byte, error)

// ListPackages returns the absolute directories of the module's packages
// (`go list -f '{{.Dir}}' ./...` in dir).
type ListPackages func(ctx context.Context, dir string) ([]string, error)

// ExecRunner is the Runner that starts the process.
func ExecRunner(ctx context.Context, dir string, argv, env []string, stdout, stderr io.Writer) (int, error) {
	if len(argv) == 0 {
		return 126, errors.New("no command to run")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, stdout, stderr
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if code := ee.ExitCode(); code >= 0 {
			return code, nil
		}
		return 128 + 9, nil // killed by a signal
	}
	if errors.Is(err, exec.ErrNotFound) {
		return 127, err
	}
	return 126, err
}

// ExecGit is the Git seam that runs git.
func ExecGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// ExecGH is the GH seam that runs gh.
func ExecGH(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("gh %s: %w: %s", firstArgs(args, 3), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func firstArgs(args []string, n int) string {
	if len(args) > n {
		args = args[:n]
	}
	return strings.Join(args, " ")
}

// ExecListPackages is the ListPackages seam that runs go list.
func ExecListPackages(ctx context.Context, dir string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-f", "{{.Dir}}", "./...")
	cmd.Dir = dir
	cmd.Env = goexec.Env()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list ./...: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var dirs []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			dirs = append(dirs, l)
		}
	}
	return dirs, nil
}
