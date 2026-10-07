// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// git runs one git command and returns trimmed stdout. extraEnv entries are
// appended to the process environment.
func git(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), append([]string{"GIT_TERMINAL_PROMPT=0", "LC_ALL=C"}, extraEnv...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(stdout.String()), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// gitRaw runs one git command and returns stdout EXACTLY as written: no
// trimming. Anything whose bytes matter (a patch, a path list separated by NUL)
// must come through here, never through git().
func gitRaw(ctx context.Context, dir string, extraEnv []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), append([]string{"GIT_TERMINAL_PROMPT=0", "LC_ALL=C"}, extraEnv...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// gitStdin runs git with stdin and returns trimmed stdout.
func gitStdin(ctx context.Context, dir, stdin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(stdout.String()), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// commonDir returns the absolute git common dir of the checkout, i.e. the
// parent repository's .git. It is derived from the checkout itself so removal
// does not depend on the server knowing where the parent repo lives.
func commonDir(ctx context.Context, checkout string) (string, error) {
	return git(ctx, checkout, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
}

// samePath compares two paths after resolving symlinks (macOS /var vs
// /private/var), falling back to a cleaned comparison for paths that no longer
// exist.
func samePath(a, b string) bool {
	return resolvePath(a) == resolvePath(b)
}

func resolvePath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	// The leaf may be gone; resolve the deepest existing parent instead.
	dir, base := filepath.Split(filepath.Clean(p))
	if dir == "" || dir == p {
		return filepath.Clean(p)
	}
	return filepath.Join(resolvePath(filepath.Clean(dir)), base)
}

// within reports whether path is strictly inside root.
func within(root, path string) bool {
	rel, err := filepath.Rel(resolvePath(root), resolvePath(path))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func parentDir(p string) string { return filepath.Dir(p) }
