// Copyright (c) 2025 Reliant Labs
package candidate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/reliant-labs/reliant/internal/mergequeue/gitport"
)

// localGit runs the few git commands the regen path needs and gitport.Port
// does not carry (a merge resolved toward one side, a diff of two trees, a
// two-parent commit, a checkout), against the port's git directory with the
// port's hermetic environment. None of them talks to a remote.
type localGit struct {
	dir string
	id  gitport.Identity
}

func (g localGit) run(ctx context.Context, workDir, stdin string, args ...string) (stdout string, code int, err error) {
	full := args
	if workDir == "" {
		full = append([]string{"--git-dir=" + g.dir}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = workDir
	cmd.Env = gitport.HermeticEnv(g.id)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	if err != nil {
		code = -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		err = fmt.Errorf("candidate: git %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return out.String(), code, err
}

// mergeTheirs merges theirs into ours in memory with conflicting hunks taken
// from theirs (-X theirs). conflicts is non-empty when a conflict remains that
// a strategy option cannot settle (modify/delete, a file against a directory).
func (g localGit) mergeTheirs(ctx context.Context, ours, theirs string) (tree string, conflicts []string, err error) {
	out, code, err := g.run(ctx, "", "", "merge-tree", "--write-tree", "-z", "--name-only", "--no-messages", "-Xtheirs", ours, theirs)
	if err != nil && code != 1 {
		return "", nil, err
	}
	fields := strings.Split(strings.TrimRight(out, "\x00"), "\x00")
	tree = strings.TrimSpace(fields[0])
	if code == 0 {
		return tree, nil, nil
	}
	return "", uniqueSorted(fields[1:]), nil
}

// changedPaths lists every path whose entry differs between two trees, with
// rename detection off, so a rename names both its old and its new path.
func (g localGit) changedPaths(ctx context.Context, a, b string) ([]string, error) {
	out, _, err := g.run(ctx, "", "", "diff-tree", "-r", "-z", "--name-only", "--no-renames", a, b)
	if err != nil {
		return nil, err
	}
	return uniqueSorted(strings.Split(out, "\x00")), nil
}

// commitTree writes a commit of tree with parents, in order.
func (g localGit) commitTree(ctx context.Context, tree string, parents []string, msg string) (string, error) {
	args := []string{"commit-tree", tree}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	args = append(args, "-F", "-")
	out, _, err := g.run(ctx, "", msg, args...)
	return strings.TrimSpace(out), err
}

// checkout is a linked worktree of the port's repository at a commit.
type checkout struct {
	g      localGit
	parent string
	dir    string
}

// newCheckout checks commit out into a new directory beside the git
// directory, so a sandbox that carries the command's own temp directory into
// its private /tmp carries the repository the checkout points at too.
func (g localGit) newCheckout(ctx context.Context, commit string) (*checkout, error) {
	parent, err := os.MkdirTemp(filepath.Dir(g.dir), "regen-")
	if err != nil {
		return nil, fmt.Errorf("candidate: regen checkout: %w", err)
	}
	c := &checkout{g: g, parent: parent, dir: filepath.Join(parent, "src")}
	if _, _, err := g.run(ctx, "", "", "worktree", "add", "--detach", "--quiet", c.dir, commit); err != nil {
		c.remove(ctx)
		return nil, err
	}
	return c, nil
}

// tree records the checkout's files, as `git add -A` sees them (ignored files
// are not part of a tree), and returns the tree.
func (c *checkout) tree(ctx context.Context) (string, error) {
	if _, _, err := c.g.run(ctx, c.dir, "", "add", "-A"); err != nil {
		return "", err
	}
	out, _, err := c.g.run(ctx, c.dir, "", "write-tree")
	return strings.TrimSpace(out), err
}

func (c *checkout) remove(ctx context.Context) {
	// Cleanup runs even when the build's context is done.
	ctx = context.WithoutCancel(ctx)
	_, _, _ = c.g.run(ctx, "", "", "worktree", "remove", "--force", c.dir)
	_ = os.RemoveAll(c.parent)
	_, _, _ = c.g.run(ctx, "", "", "worktree", "prune")
}

func uniqueSorted(in []string) []string {
	var out []string
	for _, p := range in {
		if p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}
