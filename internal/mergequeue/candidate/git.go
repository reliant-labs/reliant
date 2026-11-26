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
	return g.runEnv(ctx, workDir, stdin, nil, args...)
}

// runEnv is run with extra environment (GIT_INDEX_FILE, say).
func (g localGit) runEnv(ctx context.Context, workDir, stdin string, extra []string, args ...string) (stdout string, code int, err error) {
	full := args
	if workDir == "" {
		full = append([]string{"--git-dir=" + g.dir}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = workDir
	cmd.Env = append(gitport.HermeticEnv(g.id), extra...)
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

// checkout is a standalone copy of a commit's files, for the regen command.
//
// It is deliberately NOT a linked worktree of the port's repository (review
// B1 on #3794): a linked worktree's .git points into the shared git directory,
// which the regen command could then rewrite (refs, objects, config) and so
// alter the batch built from it. The copy lives in its own temp directory,
// away from the git directory, with a private throwaway repository of its own
// (some generators ask git about the tree). The tree regen leaves is read by
// this process, through a private index, never by the command.
type checkout struct {
	g      localGit
	parent string
	dir    string
	index  string // the private index tree() reads the copy through
}

// newCheckout copies commit's files into a new directory under the system
// temp directory.
func (g localGit) newCheckout(ctx context.Context, commit string) (*checkout, error) {
	parent, err := os.MkdirTemp("", "reliant-regen-")
	if err != nil {
		return nil, fmt.Errorf("candidate: regen checkout: %w", err)
	}
	c := &checkout{g: g, parent: parent, dir: filepath.Join(parent, "src"), index: filepath.Join(parent, "index")}
	fail := func(err error) (*checkout, error) {
		c.remove(ctx)
		return nil, err
	}
	if err := os.Mkdir(c.dir, 0o755); err != nil {
		return fail(fmt.Errorf("candidate: regen checkout: %w", err))
	}
	idx := []string{"GIT_INDEX_FILE=" + c.index}
	if _, _, err := g.runEnv(ctx, "", "", idx, "--work-tree="+c.dir, "read-tree", commit); err != nil {
		return fail(err)
	}
	if _, _, err := g.runEnv(ctx, "", "", idx, "--work-tree="+c.dir, "checkout-index", "--all", "--force"); err != nil {
		return fail(err)
	}
	// A private repository for the command, holding only this copy.
	priv := localGit{dir: filepath.Join(c.dir, ".git"), id: g.id}
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"add", "--all"},
		{"commit", "--quiet", "--no-verify", "--allow-empty", "-m", "regen base"},
	} {
		if _, _, err := priv.run(ctx, c.dir, "", args...); err != nil {
			return fail(err)
		}
	}
	return c, nil
}

// tree records the copy's files, as `git add -A` sees them (ignored files are
// not part of a tree, and the copy's own .git never is), into the port's
// repository through the private index, and returns the tree.
func (c *checkout) tree(ctx context.Context) (string, error) {
	idx := []string{"GIT_INDEX_FILE=" + c.index}
	if _, _, err := c.g.runEnv(ctx, "", "", idx, "--work-tree="+c.dir, "add", "--all"); err != nil {
		return "", err
	}
	out, _, err := c.g.runEnv(ctx, "", "", idx, "write-tree")
	return strings.TrimSpace(out), err
}

func (c *checkout) remove(ctx context.Context) {
	_ = ctx
	_ = os.RemoveAll(c.parent)
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
