package worktreereclaim

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// retirementGitState binds the exact index/private administration and resolved
// refs to the classification that authorized removal. Git is invoked with
// --no-optional-locks, so a status refresh must not redefine this baseline.
// Only the owned lock file is excluded; every other private admin byte/name
// participates, including reflogs, pseudo-refs, refs/worktree and refs/bisect.
type retirementGitState [32]byte

func inspectRetirementGitState(ctx context.Context, checkout string) (retirementGitState, error) {
	admin := adminDir(checkout)
	if admin == "" {
		return retirementGitState{}, errors.New("missing private worktree administration")
	}
	hash := sha256.New()
	entries := 0
	total := int64(0)
	add := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		hash.Write(n[:])
		hash.Write([]byte(s))
	}
	err := filepath.WalkDir(admin, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(admin, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if rel == "locked" {
			return nil
		}
		entries++
		if entries > 10000 {
			return errors.New("private Git state inventory budget exceeded")
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return errors.New("unsupported private Git state entry")
		}
		add(filepath.ToSlash(rel))
		add(info.Mode().String())
		if info.IsDir() {
			return nil
		}
		if info.Size() > 16<<20 || total+info.Size() > 64<<20 {
			return errors.New("private Git state bytes budget exceeded")
		}
		// Existing no-follow reader binds content to a regular file; no unsafe
		// symlink/pipe fallback for Git state just because Git usually owns it.
		f, err := activityOpenRegular(path)
		if err != nil {
			return err
		}
		before, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}
		if before.Size() > 16<<20 || total+before.Size() > 64<<20 || !os.SameFile(info, before) {
			f.Close()
			return errors.New("private Git state identity/size changed before read")
		}
		data, readErr := io.ReadAll(io.LimitReader(f, before.Size()+1))
		after, statErr := f.Stat()
		f.Close()
		if readErr != nil {
			return readErr
		}
		if statErr != nil {
			return statErr
		}
		if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || int64(len(data)) != before.Size() {
			return errors.New("private Git state changed while reading")
		}
		total += int64(len(data))
		add(string(data))
		return nil
	})
	if err != nil {
		return retirementGitState{}, err
	}
	// Resolved symbolic HEAD plus all visible refs bind shared branch/packed-ref
	// changes that do not rewrite the literal private HEAD file. Sorted records
	// avoid depending on backend iteration order.
	head, err := git(ctx, checkout, nil, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return retirementGitState{}, err
	}
	add(head)
	refs, err := git(ctx, checkout, nil, "for-each-ref", "--format=%(refname) %(objectname) %(symref)")
	if err != nil {
		return retirementGitState{}, err
	}
	lines := strings.Split(refs, "\n")
	sort.Strings(lines)
	for _, line := range lines {
		add(line)
	}
	var out retirementGitState
	copy(out[:], hash.Sum(nil))
	return out, nil
}
