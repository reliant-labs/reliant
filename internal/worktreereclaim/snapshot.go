// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"
)

// Snapshot saves a checkout's work to a NEW local ref under refDir WITHOUT
// touching its HEAD, its index or its files: the tree is built in a temporary
// index, written with commit-tree, and pointed at by a ref in the shared
// repository, where it outlives the worktree. Untracked files are included;
// ignored files are not (data-bearing ones hold the worktree before this runs).
//
// It refuses work it cannot save faithfully: a gitlink that HEAD does not
// already have (a nested repository recorded as one sha, losing its content),
// or more untracked bytes than maxSnapshotBytes. It returns the ref and tree.
// Nothing is pushed.
func Snapshot(ctx context.Context, checkout, refDir, worktreeID string, nested []string) (ref, tree string, err error) {
	if n := untrackedBytes(ctx, checkout); n > maxSnapshotBytes {
		return "", "", fmt.Errorf("%w: %d MB of untracked files", errTooLarge, n>>20)
	}
	tmp, err := os.MkdirTemp("", "reliant-snapshot-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(tmp)

	tree, err = workTree(ctx, checkout, tmp, nested)
	if err != nil {
		return "", "", err
	}
	if err := checkGitlinks(ctx, checkout, tree); err != nil {
		return "", "", err
	}
	msg := fmt.Sprintf("reliant: snapshot of worktree %s before cleanup (%s)", worktreeID, time.Now().UTC().Format(time.RFC3339))
	commit, err := git(ctx, checkout, nil, "-c", "user.name=reliant", "-c", "user.email=reliant@localhost",
		"commit-tree", tree, "-p", "HEAD", "-m", msg)
	if err != nil {
		return "", "", err
	}
	ref, err = createRef(ctx, checkout, refDir, commit)
	if err != nil {
		return "", "", err
	}
	if err := verifySnapshot(ctx, checkout, ref, tree, tmp, nested); err != nil {
		return "", "", err
	}
	return ref, tree, nil
}

var errTooLarge = fmt.Errorf("too large to snapshot")

// createRef creates refDir<nanos>-<random> and never overwrites: update-ref
// with an empty old value fails when the ref exists, and a new suffix is tried.
func createRef(ctx context.Context, checkout, refDir, commit string) (string, error) {
	var last error
	for attempt := 0; attempt < 8; attempt++ {
		var b [4]byte
		_, _ = rand.Read(b[:])
		ref := fmt.Sprintf("%s%d-%s", refDir, time.Now().UnixNano(), hex.EncodeToString(b[:]))
		if _, err := git(ctx, checkout, nil, "update-ref", ref, commit, ""); err == nil {
			return ref, nil
		} else {
			last = err
		}
	}
	return "", fmt.Errorf("could not create a snapshot ref: %w", last)
}

// checkGitlinks fails when tree holds a gitlink HEAD's tree does not hold at
// the same path and sha. A gitlink records only a commit id, so "saving" a
// nested repository that way would let its history and files be deleted.
func checkGitlinks(ctx context.Context, checkout, tree string) error {
	have, err := gitlinks(ctx, checkout, tree)
	if err != nil {
		return err
	}
	if len(have) == 0 {
		return nil
	}
	want, err := gitlinks(ctx, checkout, "HEAD")
	if err != nil {
		return err
	}
	for path, sha := range have {
		if want[path] != sha {
			return fmt.Errorf("nested repository at %s would be saved as a bare commit id, not its content", path)
		}
	}
	return nil
}

func gitlinks(ctx context.Context, checkout, treeish string) (map[string]string, error) {
	out, err := git(ctx, checkout, nil, "ls-tree", "-r", "-z", treeish)
	if err != nil {
		return nil, err
	}
	links := map[string]string{}
	for _, e := range strings.Split(out, "\x00") {
		meta, path, ok := strings.Cut(e, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) == 3 && f[0] == "160000" {
			links[path] = f[2]
		}
	}
	return links, nil
}

// verifySnapshot proves the saved ref still matches the working tree: it hashes
// the files as they are NOW into a fresh index and compares the resulting tree
// with the saved one, so a file that changed, appeared or vanished after the
// snapshot was taken shows up as a different tree id.
func verifySnapshot(ctx context.Context, checkout, ref, wantTree, tmp string, nested []string) error {
	saved, err := git(ctx, checkout, nil, "rev-parse", "--verify", ref+"^{tree}")
	if err != nil {
		return fmt.Errorf("snapshot ref %s unreadable: %w", ref, err)
	}
	if saved != wantTree {
		return fmt.Errorf("snapshot ref %s holds tree %s, expected %s", ref, saved, wantTree)
	}
	now, err := workTree(ctx, checkout, tmp, nested)
	if err != nil {
		return err
	}
	if now != saved {
		return fmt.Errorf("working tree no longer matches snapshot %s (tree %s, now %s)", ref, saved, now)
	}
	return nil
}

// existingSnapshotRefs lists the refs a worktree's earlier clean-ups saved.
func existingSnapshotRefs(ctx context.Context, repoPath, worktreeID string) []string {
	if repoPath == "" {
		return nil
	}
	out, err := git(ctx, repoPath, nil, "for-each-ref", "--format=%(refname)", strings.TrimSuffix(SnapshotRefPrefix(worktreeID), "/"))
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}
