// Copyright (c) 2025 Reliant Labs
package provetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// gitTimeout bounds one git invocation. cat-file --filters can run a smudge
// filter (git-lfs fetching an object), so it is not a sub-second budget.
const gitTimeout = 2 * time.Minute

// stateDirName is prove_test's directory under the repository's git dir. The
// git dir is per worktree, outside the working tree (no build or watcher
// sees it), and survives a crash — which is everything the journal needs.
const stateDirName = "reliant-prove-test"

// repo is the git repository every named file belongs to.
type repo struct {
	top     string // working tree root, symlinks resolved
	journal journal
}

// openRepo finds the one repository that holds every file and fills in each
// file's repository-relative path, returning the files deduplicated by it.
// Files spanning repositories are refused: a baseline revision names a commit
// in one of them.
func openRepo(ctx context.Context, files []*resolvedFile) (*repo, []*resolvedFile, error) {
	topByDir := map[string]string{}
	var top string
	seen := make(map[string]bool, len(files))
	unique := make([]*resolvedFile, 0, len(files))
	for _, f := range files {
		dir := filepath.Dir(f.abs)
		for {
			if _, err := os.Stat(dir); err == nil {
				break
			}
			dir = filepath.Dir(dir)
		}
		fileTop, ok := topByDir[dir]
		if !ok {
			out, err := runGit(ctx, dir, "rev-parse", "--show-toplevel")
			if err != nil {
				return nil, nil, fmt.Errorf("%s is not inside a git repository (prove_test reverts files to a git revision): %w", f.abs, err)
			}
			if fileTop, err = filepath.EvalSymlinks(strings.TrimSpace(string(out))); err != nil {
				return nil, nil, err
			}
			topByDir[dir] = fileTop
		}
		if top == "" {
			top = fileTop
		} else if fileTop != top {
			return nil, nil, fmt.Errorf("the files span two git repositories (%s and %s); call prove_test once per repository", top, fileTop)
		}
		rel, err := filepath.Rel(top, f.abs)
		if err != nil || strings.HasPrefix(rel, "..") {
			return nil, nil, fmt.Errorf("%s is outside its repository %s", f.abs, top)
		}
		f.rel = canonicalCase(top, filepath.ToSlash(rel))
		f.abs = filepath.Join(top, filepath.FromSlash(f.rel))
		if seen[f.rel] {
			continue
		}
		seen[f.rel] = true
		unique = append(unique, f)
	}

	gitDir, err := runGit(ctx, top, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, nil, err
	}
	return &repo{
		top:     top,
		journal: journal{dir: filepath.Join(strings.TrimSpace(string(gitDir)), stateDirName)},
	}, unique, nil
}

// resolveCommit turns a revision into a commit id.
func (r *repo) resolveCommit(ctx context.Context, rev string) (string, error) {
	if strings.HasPrefix(rev, "-") {
		return "", fmt.Errorf("invalid baseline revision %q", rev)
	}
	out, err := runGit(ctx, r.top, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("baseline %q is not a commit in %s", rev, r.top)
	}
	return strings.TrimSpace(string(out)), nil
}

// baselineState reads a file as it is at commit. A path absent there is a
// valid baseline — the fix added the file — and comes back with Exists false.
func (r *repo) baselineState(ctx context.Context, commit, rel, rev string) (fileState, error) {
	out, err := runGit(ctx, r.top, "ls-tree", "-z", commit, "--", rel)
	if err != nil {
		return fileState{}, err
	}
	entry := strings.TrimSuffix(string(out), "\x00")
	if entry == "" {
		return fileState{}, nil
	}
	// "<mode> SP <type> SP <oid> TAB <path>"
	meta, _, ok := strings.Cut(entry, "\t")
	fields := strings.Fields(meta)
	if !ok || len(fields) != 3 {
		return fileState{}, fmt.Errorf("unexpected git ls-tree output for %s: %q", rel, entry)
	}
	mode, kind, oid := fields[0], fields[1], fields[2]
	switch {
	case kind == "tree":
		return fileState{}, fmt.Errorf("%s is a directory at %s; name individual files", rel, rev)
	case kind == "commit":
		return fileState{}, fmt.Errorf("%s is a submodule at %s; prove_test reverts files, not submodules", rel, rev)
	case mode == "120000":
		return fileState{}, fmt.Errorf("%s is a symlink at %s; prove_test does not revert symlinks", rel, rev)
	case kind != "blob":
		return fileState{}, fmt.Errorf("%s has unexpected type %q at %s", rel, kind, rev)
	}
	// --filters applies the same smudge and end-of-line conversion a checkout
	// would, so the before-run sees the bytes git would have written — not a
	// raw LF blob on a CRLF checkout, or an LFS pointer instead of the file.
	content, err := runGit(ctx, r.top, "cat-file", "--filters", "--path="+rel, oid)
	if err != nil {
		return fileState{}, err
	}
	perm := os.FileMode(0o644)
	if mode == "100755" {
		perm = 0o755
	}
	return fileState{Exists: true, Mode: perm, Content: content, SHA256: hashBytes(content), Size: int64(len(content))}, nil
}

func runGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_OPTIONAL_LOCKS=0",    // never take the index lock from a read
		"GIT_LITERAL_PATHSPECS=1", // a path is a path, not a glob
		"GIT_TERMINAL_PROMPT=0",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

// heldLocks are the per-file locks one proof holds, keyed by journal key.
type heldLocks struct {
	byKey map[string]*fileLock
}

func (h *heldLocks) has(key string) bool {
	_, ok := h.byKey[key]
	return ok
}

func (h *heldLocks) release() {
	for _, l := range h.byKey {
		l.release()
	}
}

// lockFiles takes every file's lock, in key order so two proofs contending for
// an overlapping set cannot deadlock. The locks are OS file locks: they
// exclude another goroutine and another process alike, and the OS drops them
// when the holder dies — which is exactly the signal recovery relies on.
func (r *repo) lockFiles(ctx context.Context, files []*resolvedFile, wait time.Duration) (*heldLocks, error) {
	relByKey := make(map[string]string, len(files))
	keys := make([]string, 0, len(files))
	for _, f := range files {
		key := journalKey(f.rel)
		if _, dup := relByKey[key]; dup {
			continue // a second lock on the same file would wait on itself
		}
		relByKey[key] = f.rel
		keys = append(keys, key)
	}
	sort.Strings(keys)

	held := &heldLocks{byKey: make(map[string]*fileLock, len(keys))}
	deadline := time.Now().Add(wait)
	for _, key := range keys {
		l, err := r.journal.lockWait(ctx, key, deadline)
		if err != nil {
			held.release()
			if errors.Is(err, errLockBusy) {
				return nil, fmt.Errorf("another prove_test run is still using %s (waited %s). Nothing was changed; retry when it finishes", relByKey[key], wait)
			}
			return nil, fmt.Errorf("locking %s: %w", relByKey[key], err)
		}
		held.byKey[key] = l
	}
	return held, nil
}
