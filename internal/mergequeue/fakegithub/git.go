// Copyright (c) 2025 Reliant Labs
package fakegithub

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/reliant/internal/mergequeue/gitport"
)

// githubIdentity commits what the fake's GitHub writes (squash merges).
var githubIdentity = gitport.Identity{Name: "GitHub", Email: "noreply@github.com"}

// seedIdentity commits what tests write through the seeding helpers.
var seedIdentity = gitport.Identity{Name: "Fake Author", Email: "author@example.com"}

const zeroSHA = "0000000000000000000000000000000000000000"

// gitResult is one git invocation's outcome.
type gitResult struct {
	stdout string
	stderr string
	code   int
	err    error
}

// rawGit runs git with dir as its working directory (a bare repo's git dir
// or a worktree), hermetic env, and optional stdin and extra env.
func rawGit(dir string, id gitport.Identity, stdin string, extraEnv []string, args ...string) gitResult {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitport.HermeticEnv(id, extraEnv...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := gitResult{stdout: out.String(), stderr: errb.String(), err: err}
	if err != nil {
		res.code = -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.code = ee.ExitCode()
		}
	}
	return res
}

func (r gitResult) wrap(args ...string) error {
	if r.err == nil {
		return nil
	}
	return fmt.Errorf("fakegithub: git %s: %w: %s", strings.Join(args, " "), r.err, strings.TrimSpace(r.stderr))
}

// git runs a command in the repo's git dir and returns trimmed stdout.
func (r *Repo) git(args ...string) (string, error) {
	res := rawGit(r.dir, seedIdentity, "", nil, args...)
	if err := res.wrap(args...); err != nil {
		return "", err
	}
	return strings.TrimSpace(res.stdout), nil
}

// resolve returns the commit rev names, or "" when it names none.
func (r *Repo) resolve(rev string) string {
	res := rawGit(r.dir, seedIdentity, "", nil, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	if res.err != nil {
		return ""
	}
	return strings.TrimSpace(res.stdout)
}

// refSHA returns what a fully qualified ref points at, or "".
func (r *Repo) refSHA(ref string) string {
	res := rawGit(r.dir, seedIdentity, "", nil, "rev-parse", "--verify", "--quiet", ref)
	if res.err != nil {
		return ""
	}
	return strings.TrimSpace(res.stdout)
}

// updateRef moves ref from old ("" = must not exist) to sha, atomically.
func (r *Repo) updateRef(ref, sha, old string) error {
	if old == "" {
		old = zeroSHA
	}
	_, err := r.git("update-ref", ref, sha, old)
	return err
}

// writeCommit writes a commit object.
func (r *Repo) writeCommit(id gitport.Identity, tree string, parents []string, msg string) (string, error) {
	args := []string{"commit-tree", tree}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	args = append(args, "-F", "-")
	res := rawGit(r.dir, id, msg, nil, args...)
	if err := res.wrap(args...); err != nil {
		return "", err
	}
	return strings.TrimSpace(res.stdout), nil
}

// Deleted, as a file's content in [Repo.Commit], deletes the path.
const Deleted = "\x00fakegithub:deleted\x00"

// editTree returns the tree that is base (a tree or commit; "" for empty)
// with files applied, using a private index file so nothing is checked out.
func (r *Repo) editTree(base string, files map[string]string) (string, error) {
	idxDir, err := os.MkdirTemp(r.g.root, "index-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(idxDir) }()
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(idxDir, "index")}
	run := func(stdin string, args ...string) (string, error) {
		res := rawGit(r.dir, seedIdentity, stdin, env, args...)
		if err := res.wrap(args...); err != nil {
			return "", err
		}
		return strings.TrimSpace(res.stdout), nil
	}
	if base != "" {
		if _, err := run("", "read-tree", base); err != nil {
			return "", err
		}
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var info strings.Builder
	for _, p := range paths {
		content := files[p]
		if content == Deleted {
			fmt.Fprintf(&info, "0 %s\t%s\n", zeroSHA, p)
			continue
		}
		blob, err := run(content, "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&info, "100644 %s\t%s\n", blob, p)
	}
	if info.Len() > 0 {
		if _, err := run(info.String(), "update-index", "--index-info"); err != nil {
			return "", err
		}
	}
	return run("", "write-tree")
}

// githubSquash computes the tree GitHub's squash merge of head into baseTip
// would write. It deliberately takes a different path through git than
// gitport.Squash (porcelain `git merge --squash` in a checked-out worktree,
// not in-memory merge-tree), so the equivalence test between the two is a
// real check that the queue's predicted tree is the tree GitHub lands.
//
// conflicts is non-empty when the merge does not apply cleanly.
func (r *Repo) githubSquash(baseTip, head string) (tree string, conflicts []string, err error) {
	key := baseTip + ".." + head
	if c, ok := r.squashCache[key]; ok {
		return c.tree, c.conflicts, nil
	}
	wtParent, err := os.MkdirTemp(r.g.root, "merge-")
	if err != nil {
		return "", nil, err
	}
	wt := filepath.Join(wtParent, "wt")
	defer func() {
		_ = rawGit(r.dir, githubIdentity, "", nil, "worktree", "remove", "--force", wt)
		_ = os.RemoveAll(wtParent)
	}()
	if res := rawGit(r.dir, githubIdentity, "", nil, "worktree", "add", "--detach", "--quiet", wt, baseTip); res.err != nil {
		return "", nil, res.wrap("worktree", "add")
	}
	merge := rawGit(wt, githubIdentity, "", nil, "merge", "--squash", "--no-commit", "--quiet", head)
	if merge.err != nil {
		res := rawGit(wt, githubIdentity, "", nil, "diff", "--name-only", "--diff-filter=U")
		for _, p := range strings.Split(strings.TrimSpace(res.stdout), "\n") {
			if p != "" {
				conflicts = append(conflicts, p)
			}
		}
		if len(conflicts) == 0 {
			return "", nil, merge.wrap("merge", "--squash", head)
		}
		sort.Strings(conflicts)
		r.squashCache[key] = squashOutcome{conflicts: conflicts}
		return "", conflicts, nil
	}
	res := rawGit(wt, githubIdentity, "", nil, "write-tree")
	if err := res.wrap("write-tree"); err != nil {
		return "", nil, err
	}
	tree = strings.TrimSpace(res.stdout)
	r.squashCache[key] = squashOutcome{tree: tree}
	return tree, nil, nil
}

type squashOutcome struct {
	tree      string
	conflicts []string
}
